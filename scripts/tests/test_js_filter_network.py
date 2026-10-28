"""Run the real launch shells against a Docker stub, without a daemon."""

import json
import os
import re
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class JSFilterNetworkTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.state = self.directory / "state.json"
        self.capture = self.directory / "docker.jsonl"
        self.state.write_text(json.dumps({"networks": {}, "containers": {}}))
        docker = self.directory / "docker"
        docker.write_text("""#!/usr/bin/env python3
import json, os, re, stat, sys
from pathlib import Path
args = sys.argv[1:]
if "--env-file" in args:
    secrets = Path(args[args.index("--env-file") + 1])
    assert stat.S_IMODE(secrets.stat().st_mode) == 0o600
    assert "JS_FILTER_TOKEN=fixture-token" in secrets.read_text().splitlines()
    assert "fixture-token" not in args
path = Path(os.environ["STATE"])
state = json.loads(path.read_text())
with open(os.environ["CAPTURE"], "a") as output:
    output.write(json.dumps(args) + "\\n")
networks, containers = state["networks"], state["containers"]
if args[:2] == ["network", "inspect"]:
    network = networks.get(args[-1])
    if network is None:
        sys.exit(1)
    if "range .Containers" in " ".join(args):
        print("\\n".join(name for name, c in containers.items()
                          if args[-1] in c["networks"]))
    else:
        print(network)
elif args[:2] == ["network", "create"]:
    assert "--internal" in args and "--subnet" not in args
    project = args[args.index("--label") + 1].split("=", 1)[1]
    networks[args[-1]] = "true bridge " + project
elif args[:2] == ["network", "rm"]:
    assert not any(args[-1] in c["networks"] for c in containers.values())
    networks.pop(args[-1])
elif args[:2] == ["network", "connect"]:
    containers[args[-1]]["networks"].append(args[-2])
elif args[0] in ("run", "create") and "--rm" not in args:
    name = args[args.index("--name") + 1]
    network = args[args.index("--network") + 1]
    containers[name] = {"networks": [network]}
elif args[0] == "inspect":
    container = containers.get(args[-1])
    if container is None:
        sys.exit(1)
    network = re.search(r'Networks "([^"]+)"', args[2])[1]
    address = "172.30.0.2" if network in container["networks"] else ""
    print(str(len(container["networks"])) + " " + address)
elif args[0] == "ps":
    name = args[-1].removeprefix("name=^/").removesuffix("$")
    if name in containers:
        print(name)
elif args[0] == "rm":
    containers.pop(args[-1], None)
elif args[0] not in ("start", "run", "volume"):
    sys.exit("unexpected Docker call: " + repr(args))
path.write_text(json.dumps(state))
""")
        docker.chmod(0o755)
        self.env = dict(
            os.environ,
            PATH=self.temp.name + os.pathsep + os.environ["PATH"],
            STATE=str(self.state),
            CAPTURE=str(self.capture),
            OUTPUT="/dev/null",
            VERBOSE="",
        )

    def helper(self, action, project="one", success=True):
        result = subprocess.run(
            ["sh", str(ROOT / "tasks/js-filter-network.sh"), action, project],
            env=self.env,
            text=True,
            capture_output=True,
            timeout=10,
            check=False,
        )
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
        return result

    def launch(self, service, project, task="up"):
        source = (ROOT / "tasks" / (service + ".yml")).read_text()
        up = source.split("\n  " + task + ":\n", 1)[1]
        up = re.split(r"\n  [a-z][\w:-]*:\n", up, maxsplit=1)[0]
        script = "\n".join(
            textwrap.dedent(block) for block in up.split("      - |\n")[1:]
        )
        prefix = service.upper().replace("-", "_")
        values = {
            "ROOT_DIR": self.temp.name,
            "TASKFILE_DIR": str(ROOT / "tasks"),
            prefix + "_PROJECT": project,
            prefix + "_CONTAINER_NAME": project + "-" + service,
            "JS_FILTER_TOKEN": "fixture-token",
            "API_JS_FILTER_TOKEN": "fixture-token",
            "API_MODE": "dev",
            "API_INTERNAL_MTLS": "false",
        }
        script = re.sub(
            r"{{\.(\w+)}}",
            lambda m: values.get(m[1], "fixture-" + m[1].lower()),
            script,
        )
        self.assertNotIn("{{", script)
        result = subprocess.run(
            ["sh", "-eu"],
            input=script,
            cwd=self.temp.name,
            env=self.env,
            text=True,
            capture_output=True,
            timeout=10,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def calls(self):
        return [json.loads(line) for line in self.capture.read_text().splitlines()]

    def test_launch_isolates_each_instance_and_preserves_dns_and_hardening(self):
        for project in ("one", "two"):
            self.launch("js-filter", project)
            self.launch("api", project)
        state = json.loads(self.state.read_text())
        self.assertEqual(
            set(state["networks"]), {"one-js-filter-net", "two-js-filter-net"}
        )
        for project in ("one", "two"):
            network = project + "-js-filter-net"
            self.assertEqual(
                state["containers"][project + "-js-filter"]["networks"], [network]
            )
            self.assertEqual(
                state["containers"][project + "-api"]["networks"],
                ["fixture-api_network", network],
            )
        calls = self.calls()
        for service in ("js-filter", "api"):
            launches = [
                c
                for c in calls
                if "--name" in c and c[c.index("--name") + 1] == "one-" + service
            ]
            self.assertEqual(len(launches), 1)
            args = launches[0]
            self.assertIn("no-new-privileges:true", args)
            self.assertEqual(args[args.index("--cap-drop") + 1], "ALL")
            self.assertIn("--env-file", args)
            if service == "js-filter":
                self.assertNotIn("--ip", args)
                self.assertNotIn("-p", args)
                self.assertIn("--read-only", args)
            else:
                self.assertIn("JS_FILTER_URL=http://172.30.0.2:3100", args)
                self.assertIn(
                    str(self.directory / ".cache/testbed-resolv.conf")
                    + ":/etc/resolv.conf:ro",
                    args,
                )
                connect = ["network", "connect", "one-js-filter-net", "one-api"]
                self.assertLess(calls.index(args), calls.index(connect))
                self.assertLess(calls.index(connect), calls.index(["start", "one-api"]))
        self.assertFalse(list((self.directory / ".cache").glob("*.secrets.env")))
        self.helper("down", "one", success=False)
        self.launch("api", "one", "down")
        self.launch("js-filter", "one", "down")
        self.helper("down", "one")
        state = json.loads(self.state.read_text())
        self.assertEqual(set(state["networks"]), {"two-js-filter-net"})
        self.assertEqual(set(state["containers"]), {"two-api", "two-js-filter"})

    def test_lifecycle_is_idempotent_and_rejects_unsafe_network_reuse(self):
        self.helper("up")
        self.helper("up")
        creates = [c for c in self.calls() if c[:2] == ["network", "create"]]
        self.assertEqual(len(creates), 1)
        self.helper("down")
        self.helper("down")
        self.assertFalse(json.loads(self.state.read_text())["networks"])
        for unsafe in ("false bridge one", "true bridge other", "true overlay one"):
            with self.subTest(network=unsafe):
                self.state.write_text(
                    json.dumps(
                        {"networks": {"one-js-filter-net": unsafe}, "containers": {}}
                    )
                )
                self.helper("up", success=False)
                self.helper("down", success=False)

    def test_rejects_unexpected_peers_and_filter_on_multiple_networks(self):
        self.helper("up")
        state = json.loads(self.state.read_text())
        state["containers"]["other-api"] = {"networks": ["one-js-filter-net"]}
        self.state.write_text(json.dumps(state))
        self.helper("up", success=False)
        state["containers"] = {
            "one-js-filter": {"networks": ["one-js-filter-net", "mailnet"]}
        }
        self.state.write_text(json.dumps(state))
        self.helper("address", success=False)

    def test_instance_lifecycle_and_include_wiring(self):
        source = (ROOT / "chores.yml").read_text()
        includes = source.split("\nincludes:\n", 1)[1].split("\ntasks:\n", 1)[0]
        self.assertNotIn("JS_FILTER_NETWORK: '{{.RESTMAIL_NETWORK}}'", includes)
        self.assertNotIn("RESTMAIL_JS_FILTER_IP", includes)
        up = source.split("\n  instance:up:rest-mail:\n", 1)[1].split(
            "\n  instance:up:reference:\n", 1
        )[0]
        down = source.split("\n  instance:down:rest-mail:\n", 1)[1].split(
            "\n  instance:restart:\n", 1
        )[0]
        self.assertLess(
            up.index("task: js-filter:network:up"), up.index("task: js-filter:up")
        )
        self.assertLess(
            down.index("task: api:down"), down.index("task: js-filter:network:down")
        )
        self.assertLess(
            down.index("task: js-filter:down"),
            down.index("task: js-filter:network:down"),
        )


if __name__ == "__main__":
    unittest.main()
