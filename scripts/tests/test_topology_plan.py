"""An isolated, labelled local topology can be planned without touching Docker (#1942).

The shared topology uses fixed names, one subnet and one DNS container, so two runs on one
machine collide. `scripts/topology_plan.py` computes everything a private run needs from
its name alone. These tests hold that plan to its promises with no Docker and no topology.
"""

import importlib.util
import ipaddress
import json
import os
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


FORBIDDEN_SHARED = (
    r"testbed_(?:mailnet|certs|dnsmasq_fragments)",
    r"testbed-dnsmasq",
    r"10\.99\.",
    r"mailref-mail[12]\b",
    r"CONFIG=mail[12]\b",
    r"rest-mail-(?:api|postgres|garage|js-filter|internal-mtls)\b",
    r"rest-mail-go-modules",
    r"rest-mail-e2e-gocache",
    r"config/restmail\.test",
)
# Task names a private run starts or calls, expanded with --dry.
RUN_TASKS = ("e2e:up", "e2e:down", "e2e:mail1", "e2e:mail2", "e2e:restmail", "test:e2e", "contract", "mailserver:wait",
             "instance:dns:register", "instance:dns:unregister", "instance:dkim", "instance:certs:issue", "instance:mtls:issue")


def private_run_ready():
    return (
        shutil.which("chore")
        and shutil.which("go")
        and (ROOT / ".workspace/testbed/configs/restmail/manifest.yml").is_file()
        and (ROOT / ".workspace/reference-mailserver/configs/mail1").is_dir()
    )


@unittest.skipUnless(private_run_ready(), "needs chore, go and the pinned testbed and reference-mailserver clones (chore testbed:init, chore mailserver:init)")
class PrivateRunDryTests(unittest.TestCase):
    """What chore would run for a private topology, read with --dry: no Docker is started.

    The run is made as `chore topology:up` makes it (the manifest and the reference configs), and
    its variables are exported as that task exports them. Every expansion must then name only
    the run's own resources and addresses.
    """

    RUN = "t2062"

    @classmethod
    def setUpClass(cls):
        cls.plan = plan_module.plan(cls.RUN, existing_subnets=["10.99.0.0/16"])
        plan_module.prepare(cls.plan, str(ROOT))
        subprocess.run(["go", "run", "./cmd/instance", "render", f"{cls.plan['names']['config_dir']}/manifest.yml"], cwd=ROOT, check=True, capture_output=True)
        cls.shim = ROOT / ".cache/topology" / cls.RUN / "bin"
        cls.environment = {**os.environ, **cls.plan["environment"], "TOPOLOGY_SHIM_DIR": str(cls.shim)}
        cls.expansions = {task: cls.expand(task, cls.environment) for task in RUN_TASKS}

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(ROOT / ".cache/topology" / cls.RUN, ignore_errors=True)
        for name in ("mail1_config", "mail2_config"):
            shutil.rmtree(ROOT / ".workspace/reference-mailserver/configs" / cls.plan["names"][name], ignore_errors=True)

    @staticmethod
    def expand(task, environment, *arguments):
        result = subprocess.run(["chore", "--dry", task, *arguments], cwd=ROOT, capture_output=True, text=True, check=False, env=environment)
        if result.returncode != 0:
            raise AssertionError(f"chore --dry {task}: {result.stderr}")
        return result.stdout

    def test_no_task_of_the_run_names_a_shared_resource_or_address(self):
        for task, text in self.expansions.items():
            for pattern in FORBIDDEN_SHARED:
                for number, line in enumerate(text.splitlines(), 1):
                    self.assertIsNone(re.search(pattern, line), f"chore --dry {task}, line {number} names the shared {pattern}: {line.strip()}")

    def test_the_scan_finds_them_in_the_shared_topology(self):
        # Without it the scan could pass on any text. The shared run is the same tasks with no variables.
        shared = {**os.environ}
        for key in self.plan["environment"]:
            shared.pop(key, None)
        shared.pop("TOPOLOGY_SHIM_DIR", None)
        text = self.expand("e2e:up", shared, "CONFIG=restmail.test") if (ROOT / "config/restmail.test/config.env").is_file() else None
        if text is None:
            self.skipTest("config/restmail.test is not seeded (chore testbed:init)")
        found = {pattern for pattern in FORBIDDEN_SHARED if re.search(pattern, text)}
        self.assertTrue({r"testbed_(?:mailnet|certs|dnsmasq_fragments)", r"10\.99\."} <= found | {r"10\.99\."}, found)
        self.assertIn("testbed_certs", text)

    def test_the_run_publishes_no_fixed_host_port(self):
        pattern = re.compile(r"(?:^|\s)(?:-p|--publish)[ =]\"?(?:\d{1,3}(?:\.\d{1,3}){3}:)?(\d+):\d+")
        for task, text in self.expansions.items():
            for match in pattern.finditer(text):
                # The ACME HTTP-01 branch needs port 80 and never runs in the E2E topology.
                self.assertEqual(match.group(0).strip(), "-p 80:80", f"chore --dry {task} publishes a fixed port: {match.group(0)}")

    def test_the_run_s_own_names_are_the_ones_used(self):
        names = self.plan["names"]
        up = self.expansions["e2e:up"]
        for name in (names["certs_volume"], names["fragments_volume"], names["dnsmasq"], names["instance_project"], names["mail1_config"], names["mail2_config"]):
            self.assertIn(name, up)
        self.assertIn(f"{names['instance_project']}-internal-mtls", up)
        self.assertIn(names["gocache_volume"], up)
        self.assertIn(names["modules_volume"], up)

    def test_the_reference_servers_are_the_run_s_own_and_labelled(self):
        down = self.expansions["e2e:down"]
        for name in (self.plan["names"]["mail1_config"], self.plan["names"]["mail2_config"]):
            self.assertIn(f"down CONFIG={name} ", down)
        for task in ("e2e:mail1", "e2e:mail2", "e2e:down"):
            self.assertIn(f'PATH="{self.shim}:$PATH"', self.expansions[task])

    def test_the_dns_registration_restarts_the_runs_own_resolver(self):
        text = self.expansions["instance:dns:register"]
        self.assertIn(f"docker restart {self.plan['names']['dnsmasq']}", text)
        self.assertIn(f"{self.plan['names']['fragments_volume']}:/dst", text)

    def test_the_suite_container_is_on_the_run_s_network_and_resolver(self):
        text = self.expansions["test:e2e"]
        self.assertIn(f"--network {self.plan['names']['network']}", text)
        self.assertIn(f"\"{self.plan['dns_ip']}\" > \"$resolv\"", text)
        # The container reaches the API by its own address; the host-side CONTRACT_ENDPOINT is a loopback port.
        self.assertIn("${RESTMAIL_E2E_API_URL:-", text)

    def test_the_test_task_passes_the_addresses_the_suite_dials(self):
        text = self.expansions["test:e2e"]
        for variable in ("RESTMAIL_API_IP", "MAIL1_DOVECOT_IP", "MAIL2_DOVECOT_IP", "MAIL1_POSTFIX_IP", "MAIL2_POSTFIX_IP", "RESTMAIL_SMTP_IP"):
            self.assertRegex(text, rf"-e {variable}\b")

    def test_a_topology_with_no_run_variables_is_refused_rather_than_shared(self):
        result = subprocess.run(
            ["chore", "e2e:up", "TOPOLOGY=t2062"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
            env={key: value for key, value in os.environ.items() if key not in self.plan["environment"]},
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("chore topology:up TOPOLOGY=t2062", result.stdout + result.stderr)


class PrivateConfigTests(unittest.TestCase):
    """The files a run is made from, with no Docker and no chore."""

    def test_the_manifest_publishes_nothing(self):
        text = (ROOT / "internal/instance/testdata/manifest.yml").read_text()
        manifest = plan_module.render_manifest(text, plan_module.plan("p2062", ["10.99.0.0/16"]))
        self.assertRegex(manifest, r"(?m)^mailnet_only:\s+true")
        self.assertNotIn("10.99.", manifest)

    def test_a_reference_config_is_moved_onto_the_subnet(self):
        plan = plan_module.plan("p2062", ["10.99.0.0/16"])
        env = "POSTFIX_MYNETWORKS=127.0.0.0/8 10.99.0.0/16\nPOSTFIX_SASL_PATH=inet:10.99.0.111:12345\n"
        text = plan_module.render_reference_config(env, plan)
        prefix = plan["subnet"].rsplit(".", 2)[0]
        self.assertIn(f"{prefix}.0.0/16", text)
        self.assertIn(f"inet:{prefix}.0.111:12345", text)
        self.assertNotIn("10.99.", text)

    def test_a_pointer_record_name_is_moved_with_its_address(self):
        plan = plan_module.plan("p2062", ["10.99.0.0/16"])
        second = plan["subnet"].split(".")[1]
        self.assertEqual(
            plan_module.render_reference_config("MAIL_PTR=11.0.99.10.in-addr.arpa\n", plan),
            f"MAIL_PTR=11.0.{second}.10.in-addr.arpa\n",
        )

    def test_a_reference_config_directory_is_copied_without_another_run_s_keys(self):
        import tempfile

        plan = plan_module.plan("p2062", ["10.99.0.0/16"])
        with tempfile.TemporaryDirectory() as scratch:
            source, target = Path(scratch, "mail1"), Path(scratch, "out")
            (source / "rspamd-overlay/dkim").mkdir(parents=True)
            (source / "rspamd-overlay/dkim/mail1.test.mail.key").write_text("PRIVATE KEY")
            (source / "rspamd-overlay/local.conf").write_text("milter 10.99.0.17\n")
            (source / "resolv.conf").write_text("nameserver 10.99.0.10\n")
            (source / "blob.bin").write_bytes(b"\xff\xfe10.99.0.1")
            plan_module.copy_reference_config(str(source), str(target), plan)
            self.assertFalse((target / "rspamd-overlay/dkim").exists())
            self.assertNotIn("10.99.", (target / "resolv.conf").read_text())
            self.assertIn(plan["dns_ip"], (target / "resolv.conf").read_text())
            self.assertNotIn("10.99.", (target / "rspamd-overlay/local.conf").read_text())
            self.assertEqual((target / "blob.bin").read_bytes(), b"\xff\xfe10.99.0.1")

    def test_the_run_has_a_config_directory_and_caches_of_its_own(self):
        names = plan_module.plan("p2062", ["10.99.0.0/16"])["names"]
        self.assertEqual(names["config"], "restmail-p2062")
        self.assertEqual(names["config_dir"], ".cache/topology/p2062/config")
        self.assertEqual(names["gocache_volume"], "restmail-p2062-gocache")
        self.assertEqual(names["modules_volume"], "restmail-p2062-gomod")
        for name in ("gocache_volume", "modules_volume", "apiport", "config"):
            self.assertTrue(names[name].startswith("restmail-p2062"))

    def test_the_run_s_variables_name_its_config_and_caches(self):
        environment = plan_module.plan("p2062", ["10.99.0.0/16"])["environment"]
        self.assertEqual(environment["CONFIG"], "restmail-p2062")
        self.assertEqual(environment["TOPOLOGY_CONFIG"], "restmail-p2062")
        self.assertEqual(environment["CONFIG_DIR"], ".cache/topology/p2062/config")
        self.assertEqual(environment["RESTMAIL_GOCACHE_VOLUME"], "restmail-p2062-gocache")
        self.assertEqual(environment["RESTMAIL_MODULES_VOLUME"], "restmail-p2062-gomod")
        self.assertEqual(environment["RESTMAIL_DNSMASQ_FRAGMENTS_VOLUME"], environment["DNSMASQ_FRAGMENTS_VOLUME"])
        self.assertEqual(environment["RESTMAIL_DNSMASQ_CONTAINER"], environment["DNSMASQ_CONTAINER"])
        self.assertNotIn("NAME", environment)

    def test_the_forwarder_has_an_address_in_the_subnet_that_no_role_shares(self):
        plan = plan_module.plan("p2062", ["10.99.0.0/16"])
        self.assertIn(ipaddress.ip_address(plan["addresses"]["apiport"]), ipaddress.ip_network(plan["subnet"]))
        self.assertEqual(len(set(plan["addresses"].values())), len(plan["addresses"]))

    def test_the_run_s_own_network_does_not_move_the_plan(self):
        # Once the run's network exists, planning again must give the same subnet.
        listing = "{name} 10.99.0.0/16 \nother 10.100.0.0/16 \n{own} {subnet} \n"
        name = "p2062"
        own = plan_module.names_for(name)["network"]
        first = plan_module.plan(name, ["10.99.0.0/16", "10.100.0.0/16"])

        class Done:
            def __init__(self, out):
                self.returncode, self.stdout, self.stderr = 0, out, ""

        calls = []

        def fake(arguments, **_):
            calls.append(arguments)
            if arguments[:3] == ["docker", "network", "ls"]:
                return Done("a b c\n")
            return Done(listing.format(name="testbed_mailnet", own=own, subnet=first["subnet"]))

        original = plan_module.subprocess.run
        plan_module.subprocess.run = fake
        try:
            taken = plan_module.docker_subnets(exclude={own})
        finally:
            plan_module.subprocess.run = original
        self.assertNotIn(first["subnet"], taken)
        self.assertIn("10.99.0.0/16", taken)
        self.assertEqual(plan_module.plan(name, taken)["subnet"], first["subnet"])


def load_docker_shim():
    spec = importlib.util.spec_from_file_location("topology_docker", ROOT / "scripts/topology_docker.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


shim = load_docker_shim()


class RunNamingTests(unittest.TestCase):
    """Everything a run creates is named restmail-<run>-<purpose> and labelled (#2062)."""

    LABEL, RUN = "rest-mail-loop=2062", "2062"

    def rewrite(self, *arguments):
        return shim.rewrite(list(arguments), self.LABEL, self.RUN, 4242)

    def test_the_names_the_plan_makes_all_start_with_restmail_run(self):
        plan = plan_module.plan("2062", ["10.99.0.0/16"])
        for key in ("testbed_project", "instance_project", "network", "certs_volume", "fragments_volume", "dnsmasq",
                    "mail1_config", "mail2_config", "config", "gocache_volume", "modules_volume", "apiport"):
            self.assertTrue(plan["names"][key].startswith("restmail-2062-") or plan["names"][key] == "restmail-2062", (key, plan["names"][key]))
        for _, name in plan["resources"]:
            self.assertTrue(name.startswith("restmail-2062-"), name)

    def test_it_labels_and_names_a_container_that_has_no_name(self):
        self.assertEqual(self.rewrite("run", "--rm", "alpine:3.20", "true"),
                         ["run", "--label", self.LABEL, "--rm", "--name", "restmail-2062-helper-4242", "alpine:3.20", "true"])

    def test_a_name_after_the_image_is_not_the_container_s(self):
        out = self.rewrite("run", "--rm", "ghcr.io/rest-mail/reference-dnsmasq:0.1.0", "render-fragment", "--name", "x")
        self.assertIn("restmail-2062-helper-4242", out)

    def test_it_keeps_a_name_that_is_the_run_s_and_refuses_another(self):
        out = self.rewrite("run", "-d", "--name", "restmail-2062-api", "img")
        self.assertEqual(out.count("--name"), 1)
        for bad in ("testbed-dnsmasq", "rest-mail-api", "mailref-mail1-postfix"):
            with self.assertRaises(shim.Refused):
                self.rewrite("run", "-d", "--name", bad, "img")

    def test_it_reads_a_reference_server_name_as_the_run_s(self):
        out = self.rewrite("run", "-d", "--name", "mailref-restmail-2062-mail1-postfix", "-v", "mailref-restmail-2062-mail1-postgres-data:/d", "img")
        self.assertIn("restmail-2062-mail1-postfix", out)
        self.assertIn("restmail-2062-mail1-postgres-data:/d", out)
        self.assertNotIn("mailref-", " ".join(out))
        self.assertEqual(self.rewrite("restart", "mailref-restmail-2062-mail1-rspamd"), ["restart", "restmail-2062-mail1-rspamd"])

    def test_the_shared_reference_servers_are_not_read_as_the_run_s(self):
        self.assertEqual(self.rewrite("restart", "mailref-mail1-rspamd"), ["restart", "mailref-mail1-rspamd"])

    def test_a_named_volume_must_be_the_run_s_but_a_path_may_be_mounted(self):
        self.rewrite("run", "--rm", "-v", "restmail-2062-tb_certs:/certs", "-v", "/tmp/x:/x:ro", "-v", "./y:/y", "img")
        for bad in ("testbed_certs", "rest-mail-go-modules", "rest-mail-src-abc"):
            with self.assertRaises(shim.Refused):
                self.rewrite("run", "--rm", "-v", f"{bad}:/d", "img")

    def test_a_network_must_be_the_run_s_unless_it_is_a_built_in(self):
        self.rewrite("run", "--rm", "--network", "restmail-2062-tb_mailnet", "img")
        self.rewrite("run", "--rm", "--network", "none", "img")
        with self.assertRaises(shim.Refused):
            self.rewrite("run", "--rm", "--network", "testbed_mailnet", "img")

    def test_a_volume_or_network_is_labelled_and_must_be_the_run_s(self):
        self.assertEqual(self.rewrite("volume", "create", "restmail-2062-gomod"), ["volume", "create", "--label", self.LABEL, "restmail-2062-gomod"])
        self.assertEqual(self.rewrite("network", "create", "--internal", "--label", "a=b", "restmail-2062-js-filter-net"),
                         ["network", "create", "--label", self.LABEL, "--internal", "--label", "a=b", "restmail-2062-js-filter-net"])
        with self.assertRaises(shim.Refused):
            self.rewrite("volume", "create", "--label", "x=y", "rest-mail-e2e-gocache")

    def test_other_commands_pass_through(self):
        self.assertEqual(self.rewrite("ps", "-aq"), ["ps", "-aq"])
        self.assertEqual(self.rewrite("rm", "-fv", "restmail-2062-api"), ["rm", "-fv", "restmail-2062-api"])

    def test_the_shim_script_runs_the_helper_with_the_real_docker(self):
        text = plan_module.docker_shim("/repo", "/usr/local/bin/docker")
        self.assertIn('TOPOLOGY_REAL_DOCKER="/usr/local/bin/docker"', text)
        self.assertIn('/repo/scripts/topology_docker.py', text)

    def test_the_shim_refuses_to_run_without_its_environment(self):
        result = subprocess.run(["python3", "-B", str(ROOT / "scripts/topology_docker.py"), "ps"], capture_output=True, text=True, env={"PATH": "/usr/bin:/bin"}, check=False)
        self.assertEqual(result.returncode, 2)


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
