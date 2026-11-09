"""An isolated, labelled local topology can be planned without touching Docker (#1942).

The shared topology uses fixed names, one subnet and one DNS container, so two runs on one
machine collide. `scripts/topology_plan.py` computes everything a private run needs from
its name alone. These tests hold that plan to its promises with no Docker and no topology.
"""

import importlib.util
import ipaddress
import json
import re
import shutil
import subprocess
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]


def load():
    spec = importlib.util.spec_from_file_location("topology_plan", ROOT / "scripts/topology_plan.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


plan_module = load()

# What the shared topology owns. A private run must not name, join or restart any of it.
SHARED = (
    "testbed_mailnet",
    "testbed_certs",
    "testbed_dnsmasq_fragments",
    "testbed-dnsmasq",
    "mailref-mail1",
    "mailref-mail2",
    "10.99.",
)


def tasks_text():
    paths = [ROOT / "chores.yml", *sorted((ROOT / "tasks").glob("*.yml")), *sorted((ROOT / "tasks").glob("*.sh"))]
    return {path.relative_to(ROOT).as_posix(): path.read_text() for path in paths}


class NamesTests(unittest.TestCase):
    def test_every_name_is_made_from_the_run_name(self):
        plan = plan_module.plan("p1904")
        for value in plan["names"].values():
            self.assertIn("p1904", value)

    def test_two_runs_share_no_resource_name(self):
        first = set(plan_module.plan("alpha")["names"].values())
        second = set(plan_module.plan("beta")["names"].values())
        self.assertEqual(first & second, set())

    def test_no_name_is_one_the_shared_topology_owns(self):
        for value in plan_module.plan("p1904")["names"].values():
            for shared in SHARED[:-1]:
                self.assertNotEqual(value, shared)
                self.assertFalse(value.startswith("testbed"), value)
                self.assertFalse(value.startswith("mailref-"), value)

    def test_the_testbed_prefix_cannot_match_the_instance_containers(self):
        """The testbed removes containers named `<its project>-*` that it does not manage."""
        names = plan_module.plan("p1904")["names"]
        self.assertFalse(names["instance_project"].startswith(names["testbed_project"] + "-"))

    def test_a_bad_run_name_is_refused(self):
        for bad in ("", "Upper", "has space", "a" * 25, "-lead", "testbed", "x_y", "a/b"):
            with self.assertRaises(ValueError, msg=bad):
                plan_module.plan(bad)


class LabelTests(unittest.TestCase):
    def test_the_label_is_the_loop_label_for_the_run(self):
        plan = plan_module.plan("p1904")
        self.assertEqual(plan["label"], "rest-mail-loop=p1904")
        self.assertEqual(plan["environment"]["RESTMAIL_LABEL"], "rest-mail-loop=p1904")

    def test_every_create_command_carries_the_label(self):
        commands = plan_module.plan("p1904")["commands"]
        self.assertTrue(commands)
        for command in commands:
            if re.search(r"docker (run|create|network create|volume create)\b", command):
                self.assertIn('--label "rest-mail-loop=p1904"', command, command)

    def test_the_commands_make_a_network_two_volumes_and_a_resolver(self):
        text = "\n".join(plan_module.plan("p1904")["commands"])
        self.assertEqual(text.count("docker network create"), 1)
        self.assertEqual(text.count("docker volume create"), 2)
        self.assertEqual(text.count("docker run -d"), 1)


class SubnetTests(unittest.TestCase):
    def test_the_subnet_is_not_the_shared_one(self):
        subnet = ipaddress.ip_network(plan_module.plan("p1904")["subnet"])
        self.assertFalse(subnet.overlaps(ipaddress.ip_network("10.99.0.0/16")))

    def test_the_same_name_gets_the_same_subnet(self):
        self.assertEqual(plan_module.plan("p1904")["subnet"], plan_module.plan("p1904")["subnet"])

    def test_a_taken_subnet_is_skipped(self):
        free = plan_module.plan("p1904")["subnet"]
        moved = plan_module.plan("p1904", existing_subnets=[free])["subnet"]
        self.assertNotEqual(free, moved)
        self.assertFalse(ipaddress.ip_network(free).overlaps(ipaddress.ip_network(moved)))

    def test_a_wider_taken_range_is_skipped_too(self):
        taken = plan_module.plan("p1904")["subnet"].rsplit(".", 3)[0] + ".0.0.0/14"
        moved = plan_module.plan("p1904", existing_subnets=[taken])["subnet"]
        self.assertFalse(ipaddress.ip_network(taken, strict=False).overlaps(ipaddress.ip_network(moved)))

    def test_a_full_pool_is_an_error(self):
        with self.assertRaises(RuntimeError):
            plan_module.plan("p1904", existing_subnets=["10.100.0.0/16"], pool=["10.100.0.0/16"])

    def test_every_address_is_inside_the_subnet_and_unique(self):
        plan = plan_module.plan("p1904")
        subnet = ipaddress.ip_network(plan["subnet"])
        addresses = list(plan["addresses"].values())
        self.assertEqual(len(addresses), len(set(addresses)))
        for address in addresses:
            self.assertIn(ipaddress.ip_address(address), subnet)
        self.assertNotIn(plan["dns_ip"], [a for k, a in plan["addresses"].items() if k != "dns"])

    def test_shifting_keeps_the_host_part_and_refuses_other_ranges(self):
        plan = plan_module.plan("p1904")
        prefix = ".".join(plan["subnet"].split(".")[:2])
        self.assertEqual(plan_module.shift("10.99.0.20", plan["subnet"]), f"{prefix}.0.20")
        with self.assertRaises(ValueError):
            plan_module.shift("192.168.0.20", plan["subnet"])

    def test_shifting_text_moves_every_shared_address(self):
        plan = plan_module.plan("p1904")
        moved = plan_module.shift_text("POSTFIX_DB_HOST=10.99.0.41\nmynetworks=10.99.0.0/16\nx=110.99.0.1", plan["subnet"])
        prefix = ".".join(plan["subnet"].split(".")[:2])
        self.assertIn(f"POSTFIX_DB_HOST={prefix}.0.41", moved)
        self.assertIn(f"mynetworks={prefix}.0.0/16", moved)
        self.assertIn("x=110.99.0.1", moved)
        self.assertNotIn("=10.99.", moved)


class ManifestTests(unittest.TestCase):
    """The instance manifest carries the names and addresses, so a private run needs its own."""

    def render(self):
        plan = plan_module.plan("p1904")
        text = (ROOT / "internal/instance/testdata/manifest.yml").read_text()
        return plan, text, plan_module.render_manifest(text, plan)

    def test_the_manifest_takes_the_run_s_names(self):
        plan, _, rendered = self.render()
        document = yaml.safe_load(rendered)
        self.assertEqual(document["project"], plan["names"]["instance_project"])
        self.assertEqual(document["network"], plan["names"]["network"])
        self.assertEqual(document["certs_volume"], plan["names"]["certs_volume"])
        self.assertEqual(document["testbed_dns_ip"], plan["dns_ip"])

    def test_no_shared_address_or_name_is_left_in_it(self):
        _, _, rendered = self.render()
        self.assertNotIn("10.99.", rendered)
        self.assertNotIn("testbed_mailnet", rendered)
        self.assertNotIn("testbed_certs", rendered)

    def test_only_the_placement_changes(self):
        _, text, rendered = self.render()
        before, after = yaml.safe_load(text), yaml.safe_load(rendered)
        for key in ("domain", "hostname", "registry", "image_tag"):
            self.assertEqual(before[key], after[key])

    def test_a_manifest_that_lacks_a_placement_line_is_refused(self):
        with self.assertRaises(ValueError):
            plan_module.render_manifest("domain: restmail.test\n", plan_module.plan("p1904"))


class SharedResolverTests(unittest.TestCase):
    def test_the_plan_names_nothing_the_shared_topology_owns(self):
        plan = plan_module.plan("p1904")
        blob = json.dumps(plan)
        for shared in SHARED:
            self.assertNotIn(shared, blob)

    def test_the_resolver_and_its_fragments_are_the_run_s_own(self):
        plan = plan_module.plan("p1904")
        environment = plan["environment"]
        self.assertEqual(environment["DNSMASQ_CONTAINER"], plan["names"]["dnsmasq"])
        self.assertEqual(environment["DNSMASQ_FRAGMENTS_VOLUME"], plan["names"]["fragments_volume"])
        self.assertEqual(environment["RESTMAIL_NETWORK"], plan["names"]["network"])
        self.assertEqual(environment["RESTMAIL_CERTS_VOLUME"], plan["names"]["certs_volume"])
        self.assertEqual(environment["RESTMAIL_TESTBED_DNS_IP"], plan["dns_ip"])
        self.assertEqual(environment["TESTBED_PROJECT"], plan["names"]["testbed_project"])
        self.assertEqual(environment["RESTMAIL_PROJECT"], plan["names"]["instance_project"])

    def test_the_contract_endpoint_is_set_so_the_shared_stack_is_never_started(self):
        plan = plan_module.plan("p1904")
        self.assertEqual(plan["environment"]["CONTRACT_ENDPOINT"], f"http://{plan['addresses']['api']}:8080")


class PortTests(unittest.TestCase):
    def test_the_plan_publishes_no_fixed_host_port(self):
        for command in plan_module.plan("p1904")["commands"]:
            self.assertNotRegex(command, r"(?:^|\s)(?:-p|--publish)[ =]\S*\d+:\d+")

    def test_a_publish_flag_lets_docker_choose_the_host_port(self):
        self.assertEqual(plan_module.publish(8080), "-p 127.0.0.1::8080")

    def test_the_task_files_publish_no_fixed_host_port(self):
        # The one fixed publish: the ACME HTTP-01 issuance for a real install's certificate
        # (cert_provider=acme), which needs port 80. The E2E topology never runs it.
        allowed = {("chores.yml", "-p 80:80 \\")}
        pattern = re.compile(r"(?:^|\s)(?:-p|--publish)[ =]\"?(?:\d{1,3}(?:\.\d{1,3}){3}:)?\d+:\d+")
        for name, text in tasks_text().items():
            for number, line in enumerate(text.splitlines(), 1):
                if line.strip().startswith("#"):
                    continue
                if (name, line.strip()) in allowed:
                    continue
                self.assertIsNone(pattern.search(line), f"{name}:{number} publishes a fixed host port: {line.strip()}")


class TaskFileTests(unittest.TestCase):
    def setUp(self):
        self.chores = yaml.safe_load((ROOT / "chores.yml").read_text())

    def test_the_testbed_project_can_be_set_without_editing_the_file(self):
        project = self.chores["includes"]["testbed"]["vars"]["PROJECT"]
        self.assertIn("TESTBED_PROJECT", project)
        self.assertIn('"testbed"', project)

    DEFAULTS = {
        "mail1": (("POSTGRES", "10.99.0.41"), ("POSTFIX", "10.99.0.11"), ("DOVECOT", "10.99.0.111"), ("RSPAMD", "10.99.0.17"), ("FAIL2BAN", "10.99.0.14")),
        "mail2": (("POSTGRES", "10.99.0.42"), ("POSTFIX", "10.99.0.12"), ("DOVECOT", "10.99.0.112"), ("RSPAMD", "10.99.0.23"), ("FAIL2BAN", "10.99.0.24")),
    }

    def script(self, instance):
        commands = self.chores["tasks"][f"e2e:{instance}"]["cmds"]
        return next(command for command in commands if isinstance(command, str) and "reference-mailserver" in command)

    def test_each_reference_server_takes_its_addresses_from_variables(self):
        for instance, roles in self.DEFAULTS.items():
            script = self.script(instance)
            for role, _ in roles:
                self.assertIn(f"{role}_IP={{{{.{instance.upper()}_{role}_IP | default", script)

    def test_each_reference_server_keeps_its_old_addresses_by_default(self):
        for instance, roles in self.DEFAULTS.items():
            script = self.script(instance)
            for role, address in roles:
                self.assertIn(f'.{instance.upper()}_{role}_IP | default "{address}"', script)

    def test_the_defaults_are_the_plan_s_shared_addresses(self):
        for instance, roles in self.DEFAULTS.items():
            for role, address in roles:
                self.assertEqual(plan_module.ROLES[f"{instance}_{role.lower()}"], address)

    def test_every_shared_address_the_task_files_hold_is_one_the_plan_can_move(self):
        """A literal the plan does not know would stay on the shared subnet in a private run."""
        known = set(plan_module.SHARED_ADDRESSES)
        for name, text in tasks_text().items():
            for number, line in enumerate(text.splitlines(), 1):
                if line.strip().startswith("#"):
                    continue
                for address in re.findall(r"\b10\.99\.\d{1,3}\.\d{1,3}\b", line):
                    self.assertIn(address, known, f"{name}:{number} holds {address}, which the plan does not move")

    def test_a_topology_namespace_exposes_the_plan(self):
        self.assertIn("topology", self.chores["includes"])
        tasks = yaml.safe_load((ROOT / "tasks/topology.yml").read_text())["tasks"]
        self.assertIn("plan", tasks)
        self.assertIn("substrate:up", tasks)
        self.assertIn("substrate:down", tasks)


@unittest.skipUnless(shutil.which("chore") and (ROOT / ".workspace/testbed").is_dir() and (ROOT / "config/restmail.test").is_dir(), "needs chore and the pinned testbed clone (chore testbed:init)")
class ExpansionTests(unittest.TestCase):
    """What chore would run, read with --dry: no Docker is started."""

    def expand(self, task, plan):
        arguments = [f"{key}={value}" for key, value in plan["environment"].items() if key != "RESTMAIL_LABEL"]
        result = subprocess.run(
            ["chore", "--dry", task, *arguments, "CONFIG=restmail.test"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
            env={**__import__("os").environ, "RESTMAIL_LABEL": plan["label"]},
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def test_the_testbed_substrate_is_the_runs_own(self):
        plan = plan_module.plan("p1904")
        text = self.expand("testbed:bootstrap", plan)
        self.assertIn(plan["names"]["network"], text)
        self.assertIn(plan["subnet"], text)
        self.assertNotIn("testbed_mailnet", text)
        self.assertNotIn("10.99.", text)

    def test_the_dns_registration_restarts_the_runs_own_resolver(self):
        plan = plan_module.plan("p1904")
        text = self.expand("instance:dns:register", plan)
        self.assertIn(f"docker restart {plan['names']['dnsmasq']}", text)
        self.assertNotIn("testbed-dnsmasq", text)
        self.assertNotIn("testbed_dnsmasq_fragments", text)


class CommandLineTests(unittest.TestCase):
    def run_plan(self, *arguments):
        return subprocess.run(
            ["python3", "-B", "scripts/topology_plan.py", *arguments], cwd=ROOT, capture_output=True, text=True, check=False
        )

    def test_the_default_output_is_json(self):
        result = self.run_plan("p1904", "--existing-subnets", "")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["label"], "rest-mail-loop=p1904")

    def test_commands_prints_only_the_docker_commands(self):
        result = self.run_plan("p1904", "--existing-subnets", "", "--commands")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('docker network create --label "rest-mail-loop=p1904"', result.stdout)
        self.assertNotIn("{", result.stdout)

    def test_environment_prints_one_assignment_per_line(self):
        result = self.run_plan("p1904", "--existing-subnets", "", "--environment")
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = result.stdout.splitlines()
        self.assertIn("RESTMAIL_LABEL=rest-mail-loop=p1904", lines)
        for line in lines:
            self.assertRegex(line, r"^[A-Z0-9_]+=\S+$")

    def test_create_and_resolver_split_the_commands(self):
        both = self.run_plan("p1904", "--existing-subnets", "", "--commands").stdout.splitlines()
        create = self.run_plan("p1904", "--existing-subnets", "", "--create").stdout.splitlines()
        resolver = self.run_plan("p1904", "--existing-subnets", "", "--resolver").stdout.splitlines()
        self.assertEqual(create + resolver, both)
        self.assertEqual(len(resolver), 1)
        self.assertTrue(resolver[0].startswith("docker run -d"))
        self.assertTrue(all(line.startswith(("docker network create", "docker volume create")) for line in create))

    def test_a_bad_name_exits_non_zero(self):
        self.assertNotEqual(self.run_plan("Bad Name", "--existing-subnets", "").returncode, 0)

    def test_existing_subnets_come_from_the_flag(self):
        free = json.loads(self.run_plan("p1904", "--existing-subnets", "").stdout)["subnet"]
        moved = json.loads(self.run_plan("p1904", "--existing-subnets", free).stdout)["subnet"]
        self.assertNotEqual(free, moved)


if __name__ == "__main__":
    unittest.main()
