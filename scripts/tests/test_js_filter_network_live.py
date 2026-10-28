"""Live Docker check of the private js-filter network (#122).

Opt-in: set RESTMAIL_JS_FILTER_LIVE=1 and JS_FILTER_LIVE_IMAGE to a built
js-filter image. Without the opt-in the test skips, so the plain
`unittest discover` in Lint stays daemon-free. With it, a missing Docker or a
failed positive control is a failure, never a skip.

The filter is started by the real launch shell in tasks/js-filter.yml, which
provisions the network with tasks/js-filter-network.sh. Two instances are
started. A dual-homed peer named <project>-api stands where the API sits (on a
mailnet-like bridge and the private network); the Go API itself is not started.
Every denied probe has a positive control showing the same probe reaches the
same listener from a network that is allowed to.

The lifecycle test stops, restarts and recreates instance A with the real
down/up/restart shells from the Taskfiles, in chores.yml's order, and checks
instance B is untouched throughout.
"""

import json
import os
import re
import shutil
import subprocess
import tempfile
import textwrap
import time
import unittest
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LIVE = os.environ.get("RESTMAIL_JS_FILTER_LIVE") == "1"
TOKEN = "live-js-filter-token-0123456789"
# A public address reached by IP, so a refusal is about routing, not DNS.
EXTERNAL = os.environ.get("JS_FILTER_LIVE_EXTERNAL", "1.1.1.1:443")

# Prints exactly one line: connected, timeout or error:<code>. It always exits
# 0, so a non-zero exit means the probe itself could not run.
PROBE = textwrap.dedent("""
    const [host, port] = process.argv[1].split(':');
    const s = require('net').connect({ host, port: Number(port) });
    const done = (r) => { console.log(r); s.destroy(); process.exit(0); };
    s.setTimeout(4000, () => done('timeout'));
    s.on('connect', () => done('connected'));
    s.on('error', (e) => done('error:' + (e.code || e.message)));
""")

# Calls /execute and /validate with and without the bearer token.
CALLS = textwrap.dedent("""
    const [base, token] = process.argv.slice(1);
    const script = 'function filter(email) { return { type: "action", action: "continue" }; }';
    const call = async (path, auth) => {
      const headers = { 'Content-Type': 'application/json' };
      if (auth) headers.Authorization = 'Bearer ' + token;
      const r = await fetch(base + path, { method: 'POST', headers,
        body: JSON.stringify({ script, email: { from: 'a@example.test' } }) });
      return { status: r.status, body: await r.json().catch(() => null) };
    };
    (async () => {
      const out = {};
      for (const p of ['/execute', '/validate']) {
        out[p] = { authed: await call(p, true), anonymous: await call(p, false) };
      }
      console.log(JSON.stringify(out));
    })().catch((e) => { console.log(JSON.stringify({ error: String(e) })); });
""")

LISTENER = "require('net').createServer((s) => s.end()).listen(2525, '0.0.0.0')"


@unittest.skipUnless(LIVE, "set RESTMAIL_JS_FILTER_LIVE=1 to run against Docker")
class JSFilterNetworkLiveTests(unittest.TestCase):
    def setUp(self):
        self.assertIsNotNone(shutil.which("docker"), "docker CLI is required")
        self.image = os.environ.get("JS_FILTER_LIVE_IMAGE", "")
        self.assertTrue(self.image, "JS_FILTER_LIVE_IMAGE must name a built image")
        self.docker("image", "inspect", self.image)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        tag = "jsfl" + uuid.uuid4().hex[:8]
        self.a, self.b = tag + "a", tag + "b"
        self.mailnet = tag + "-mailnet"
        self.peer = tag + "-peer"
        # Cleanup runs last-registered-first: containers, then networks.
        for name in (
            self.mailnet,
            self.a + "-js-filter-net",
            self.b + "-js-filter-net",
        ):
            self.addCleanup(self.docker, "network", "rm", name, check=False)
        for name in (self.peer,) + tuple(
            p + s for p in (self.a, self.b) for s in ("-api", "-js-filter")
        ):
            self.addCleanup(self.docker, "rm", "-f", name, check=False)

    def docker(self, *args, check=True, timeout=120):
        result = subprocess.run(
            ["docker", *args],
            text=True,
            capture_output=True,
            timeout=timeout,
            check=False,
        )
        if check:
            self.assertEqual(result.returncode, 0, f"docker {args}: {result.stderr}")
        return result.stdout.strip()

    def run_task(self, taskfile, task, project):
        """Run a real Taskfile task's commands for one instance, one shell each.

        `- task: x` entries run task x from the same file, as Task does.
        """
        source = (ROOT / taskfile).read_text()
        body = source.split(f"\n  {task}:\n", 1)[1]
        body = re.split(r"\n  [a-z][\w:-]*:\n", body, maxsplit=1)[0]
        commands = body.split("\n    cmds:\n", 1)[1]
        values = {
            "ROOT_DIR": self.temp.name,
            "TASKFILE_DIR": str(ROOT / "tasks"),
            "JS_FILTER_PROJECT": project,
            "JS_FILTER_CONTAINER_NAME": project + "-js-filter",
            "JS_FILTER_TOKEN": TOKEN,
            "JS_FILTER_IMAGE": self.image,
            "API_CONTAINER_NAME": project + "-api",
        }
        for command in re.split(r"^      - ", commands, flags=re.MULTILINE)[1:]:
            if command.startswith("task:"):
                self.run_task(taskfile, command.split(":", 1)[1].strip(), project)
                continue
            if command.startswith("|\n"):
                command = textwrap.dedent(command[2:])
            script = re.sub(r"{{\.(\w+)}}", lambda m: values[m[1]], command)
            result = subprocess.run(
                ["sh", "-eu"],
                input=script,
                cwd=self.temp.name,
                env=dict(os.environ, OUTPUT="/dev/null"),
                text=True,
                capture_output=True,
                timeout=120,
                check=False,
            )
            self.assertEqual(
                result.returncode,
                0,
                f"{taskfile} {task}: {result.stdout}{result.stderr}",
            )

    def launch_filter(self, project):
        """Run tasks/js-filter.yml's real `up` shell for one instance."""
        self.run_task("tasks/js-filter.yml", "up", project)

    def helper(self, action, project):
        result = subprocess.run(
            ["sh", str(ROOT / "tasks/js-filter-network.sh"), action, project],
            text=True,
            capture_output=True,
            timeout=60,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout.strip()

    def start_api_peer(self, project):
        """Join <project>-api as tasks/api.yml does: mailnet, then the private net."""
        name = project + "-api"
        self.docker(
            "create",
            "--name",
            name,
            "--network",
            self.mailnet,
            "--entrypoint",
            "sleep",
            self.image,
            "600",
        )
        self.docker("network", "connect", project + "-js-filter-net", name)
        self.docker("start", name)

    def start_api_standin(self, project):
        """Create <project>-api in tasks/api.yml's order.

        The filter's address comes from the helper's `address` and is baked
        into JS_FILTER_URL; the stand-in joins mailnet, then the private
        network, then starts.
        """
        address = self.helper("address", project)
        name = project + "-api"
        self.docker(
            "create",
            "--name",
            name,
            "--network",
            self.mailnet,
            "-e",
            f"JS_FILTER_URL=http://{address}:3100",
            "--entrypoint",
            "sleep",
            self.image,
            "600",
        )
        self.docker("network", "connect", project + "-js-filter-net", name)
        self.docker("start", name)

    def instance_up(self, project):
        """chores.yml's start order, less postgres: network, filter, API."""
        self.run_task("tasks/js-filter.yml", "network:up", project)
        self.run_task("tasks/js-filter.yml", "up", project)
        self.start_api_standin(project)
        self.wait_healthy(project + "-js-filter")

    def instance_down(self, project):
        """chores.yml's stop order: API, filter, then the private network."""
        self.run_task("tasks/api.yml", "down", project)
        self.run_task("tasks/js-filter.yml", "down", project)
        self.run_task("tasks/js-filter.yml", "network:down", project)

    def exists(self, kind, name):
        return (
            subprocess.run(
                ["docker", kind, "inspect", name],
                capture_output=True,
                timeout=60,
                check=False,
            ).returncode
            == 0
        )

    def private_address(self, project, container):
        template = (
            '{{(index .NetworkSettings.Networks "'
            + project
            + '-js-filter-net").IPAddress}}'
        )
        return self.docker("inspect", "--format", template, container)

    def assert_private_network(self, project):
        network = json.loads(
            self.docker("network", "inspect", project + "-js-filter-net")
        )[0]
        self.assertIs(network["Internal"], True)
        self.assertEqual(
            sorted(c["Name"] for c in network["Containers"].values()),
            sorted([project + "-api", project + "-js-filter"]),
        )

    def assert_api_calls(self, project):
        """Call the filter at the URL the API stand-in was created with."""
        base = self.docker("exec", project + "-api", "printenv", "JS_FILTER_URL")
        calls = json.loads(self.node(project + "-api", CALLS, base, TOKEN))
        self.assertNotIn("error", calls)
        self.assertEqual(calls["/execute"]["authed"]["status"], 200, calls)
        self.assertEqual(
            calls["/execute"]["authed"]["body"]["result"]["type"], "action"
        )
        self.assertEqual(calls["/validate"]["authed"]["status"], 200, calls)
        self.assertIs(calls["/validate"]["authed"]["body"]["valid"], True)
        for path in ("/execute", "/validate"):
            self.assertEqual(calls[path]["anonymous"]["status"], 401, calls)
        return base

    def assert_filter_isolated(self, project, other):
        template = (
            '{{(index .NetworkSettings.Networks "' + self.mailnet + '").IPAddress}}'
        )
        peer_ip = self.docker("inspect", "--format", template, self.peer)
        cases = [
            ("external", project + "-api", EXTERNAL),
            ("mailnet peer", project + "-api", f"{peer_ip}:2525"),
            (
                "other instance filter",
                other + "-api",
                f"{self.helper('address', other)}:3100",
            ),
        ]
        for label, control, target in cases:
            with self.subTest(project=project, case=label):
                self.assertEqual(
                    self.probe(control, target),
                    "connected",
                    f"positive control for {label} failed; "
                    "cannot prove the filter's denial",
                )
                self.assertNotEqual(
                    self.probe(project + "-js-filter", target),
                    "connected",
                    f"{project}-js-filter reached {label} {target}",
                )

    def node(self, container, code, *args):
        result = subprocess.run(
            ["docker", "exec", container, "node", "-e", code, *args],
            text=True,
            capture_output=True,
            timeout=60,
            check=False,
        )
        # A non-zero exit means the probe did not run, which proves nothing.
        self.assertEqual(
            result.returncode, 0, f"probe failed to run in {container}: {result.stderr}"
        )
        return result.stdout.strip()

    def probe(self, container, target):
        outcome = self.node(container, PROBE, target)
        self.assertRegex(outcome, r"^(connected|timeout|error:\S+)$")
        return outcome

    def wait_healthy(self, container):
        for _ in range(60):
            if self.probe(container, "127.0.0.1:3100") == "connected":
                return
            time.sleep(1)
        self.fail(container + " never listened on :3100")

    def test_private_network_serves_api_and_denies_filter_egress(self):
        self.docker("network", "create", "--driver", "bridge", self.mailnet)
        self.docker(
            "run",
            "-d",
            "--name",
            self.peer,
            "--network",
            self.mailnet,
            "--entrypoint",
            "node",
            self.image,
            "-e",
            LISTENER,
        )
        for project in (self.a, self.b):
            self.launch_filter(project)
            self.start_api_peer(project)
        ip = {p: self.helper("address", p) for p in (self.a, self.b)}
        template = (
            '{{(index .NetworkSettings.Networks "' + self.mailnet + '").IPAddress}}'
        )
        peer_ip = self.docker("inspect", "--format", template, self.peer)
        filter_a, api_a = self.a + "-js-filter", self.a + "-api"
        filter_b, api_b = self.b + "-js-filter", self.b + "-api"
        # Positive control for the probe inside the hardened filter itself.
        self.wait_healthy(filter_a)
        self.wait_healthy(filter_b)

        # The private network is internal and holds only this instance's pair.
        for project in (self.a, self.b):
            network = json.loads(
                self.docker("network", "inspect", project + "-js-filter-net")
            )[0]
            self.assertIs(network["Internal"], True)
            self.assertEqual(
                sorted(c["Name"] for c in network["Containers"].values()),
                sorted([project + "-api", project + "-js-filter"]),
            )

        # Authenticated /execute and /validate from the API position.
        calls = json.loads(self.node(api_a, CALLS, f"http://{ip[self.a]}:3100", TOKEN))
        self.assertNotIn("error", calls)
        self.assertEqual(calls["/execute"]["authed"]["status"], 200, calls)
        self.assertEqual(
            calls["/execute"]["authed"]["body"]["result"]["type"], "action"
        )
        self.assertEqual(calls["/validate"]["authed"]["status"], 200, calls)
        self.assertIs(calls["/validate"]["authed"]["body"]["valid"], True)
        for path in ("/execute", "/validate"):
            self.assertEqual(calls[path]["anonymous"]["status"], 401, calls)

        # Each denial is paired with the same listener reached from an allowed
        # network, so a refusal cannot be a missing listener.
        cases = [
            ("external", api_a, EXTERNAL),
            ("mailnet peer", api_a, f"{peer_ip}:2525"),
            ("other instance filter", api_b, f"{ip[self.b]}:3100"),
        ]
        for label, control, target in cases:
            with self.subTest(label):
                self.assertEqual(
                    self.probe(control, target),
                    "connected",
                    f"positive control for {label} failed; "
                    "cannot prove the filter's denial",
                )
                self.assertNotEqual(
                    self.probe(filter_a, target),
                    "connected",
                    f"{filter_a} reached {label} {target}",
                )

    def test_lifecycle_down_restart_and_filter_recreate(self):
        self.docker("network", "create", "--driver", "bridge", self.mailnet)
        self.docker(
            "run",
            "-d",
            "--name",
            self.peer,
            "--network",
            self.mailnet,
            "--entrypoint",
            "node",
            self.image,
            "-e",
            LISTENER,
        )
        for project in (self.a, self.b):
            self.instance_up(project)
            self.assert_private_network(project)
            self.assert_api_calls(project)
        b_filter = self.docker("inspect", "--format", "{{.Id}}", self.b + "-js-filter")
        b_api = self.docker("inspect", "--format", "{{.Id}}", self.b + "-api")

        def assert_b_untouched():
            for name, wanted in (
                (self.b + "-js-filter", b_filter),
                (self.b + "-api", b_api),
            ):
                self.assertEqual(
                    self.docker("inspect", "--format", "{{.Id}}", name),
                    wanted,
                    f"{name} was replaced by instance A's lifecycle",
                )
            self.assert_private_network(self.b)
            self.assert_api_calls(self.b)

        # 1. Stopping A removes A's private bridge and nothing else.
        self.instance_down(self.a)
        for name in (self.a + "-api", self.a + "-js-filter"):
            self.assertFalse(self.exists("container", name), f"{name} survived down")
        self.assertFalse(
            self.exists("network", self.a + "-js-filter-net"),
            "instance down left its private network",
        )
        self.assertTrue(self.exists("network", self.mailnet), "down removed mailnet")
        self.assertTrue(
            self.exists("container", self.peer), "down removed a mailnet peer"
        )
        assert_b_untouched()

        # 2. A full restart brings A's authenticated calls and isolation back.
        self.instance_up(self.a)
        self.assert_private_network(self.a)
        self.assert_api_calls(self.a)
        self.assert_filter_isolated(self.a, self.b)
        assert_b_untouched()

        # 3. Recreating only the filter, then the API as documented in
        # tasks/api.yml (it reads the address once, at creation).
        old_filter = self.docker(
            "inspect", "--format", "{{.Id}}", self.a + "-js-filter"
        )
        self.run_task("tasks/js-filter.yml", "restart", self.a)
        self.wait_healthy(self.a + "-js-filter")
        new_filter = self.docker(
            "inspect", "--format", "{{.Id}}", self.a + "-js-filter"
        )
        self.assertNotEqual(
            new_filter, old_filter, "restart did not recreate the filter"
        )
        # Docker may hand back the same IP, so only check the helper reports
        # the new container's actual address.
        address = self.helper("address", self.a)
        self.assertEqual(address, self.private_address(self.a, new_filter))
        self.run_task("tasks/api.yml", "down", self.a)
        self.start_api_standin(self.a)
        self.assert_private_network(self.a)
        self.assertEqual(self.assert_api_calls(self.a), f"http://{address}:3100")
        self.assert_filter_isolated(self.a, self.b)
        assert_b_untouched()
