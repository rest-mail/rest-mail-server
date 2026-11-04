"""Check the js-filter private network against two real, running instances.

Opt-in: it starts and stops real containers, so it runs only when
RESTMAIL_LIVE_JS_FILTER_CONFIGS names two scaffolded configs, for example

    RESTMAIL_LIVE_JS_FILTER_CONFIGS="isolation-a.test isolation-b.test" \\
      python3 -m unittest scripts/tests/test_js_filter_isolation_live.py -v

Both configs need their own project name and mailnet addresses
(`chore config:scaffold DOMAIN=<domain>`), registered testbed DNS and serving
and mTLS certificates, and the testbed must be up. The test owns
the two instances: it refuses to run if any of their containers already exist,
starts them with `chore instance:up`, and stops them with `chore instance:down`
when it finishes, pass or fail. Data volumes are kept, as instance:down keeps
them. Each check prints one line, so the output can be attached as a record.
The seeded admin signs in through the Go API, which must itself invoke both
sidecar endpoints. Direct token refusals and controlled TCP probes supplement
those API calls; a failed probe executable cannot count as denied egress.
"""

import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CONFIGS = os.environ.get("RESTMAIL_LIVE_JS_FILTER_CONFIGS", "").split()
# Public targets the filter must not reach: an address, and a name, which
# also exercises DNS from inside the private network.
PUBLIC_TARGETS = os.environ.get(
    "RESTMAIL_LIVE_PUBLIC_TARGETS", "1.1.1.1:443 example.com:443"
).split()
FILTER_PORT = 3100
# `chore js-filter:restart` returns once the container exists, not once the
# service inside it listens, so a check right after it must wait.
FILTER_READY_TIMEOUT = 60
FILTER_READY_INTERVAL = 1
SCRIPT = 'function filter(email) { return { type: "action", action: "continue" }; }'
EMAIL = {"from": "isolation@example.test"}

# Opens a TCP connection from inside a container and reports how it went.
PROBE = """
const net = require("net");
const target = process.argv[1], i = target.lastIndexOf(":");
const s = net.connect({ host: target.slice(0, i), port: +target.slice(i + 1) });
const done = (word, code) => { console.log(word); process.exit(code); };
s.setTimeout(3000, () => done("timeout", 1));
s.on("connect", () => done("connected", 0));
s.on("error", (e) => done(e.code || e.message, 1));
"""

# Calls the filter from inside the API container with the API's own URL and,
# unless NO_TOKEN is set, its own token. The token stays inside the container.
CALL = """
set -- --header 'Content-Type: application/json'
[ -n "${NO_TOKEN:-}" ] || set -- "$@" --header "Authorization: Bearer $JS_FILTER_TOKEN"
wget -S -O- -T 10 "$@" --post-data "$BODY" "$JS_FILTER_URL$ROUTE" 2>&1
echo "exit=$?"
"""

API_CALL = """
url="$1"
method="$2"
IFS= read -r token
set -- --header 'Content-Type: application/json'
[ -z "$token" ] || set -- "$@" --header "Authorization: Bearer $token"
[ "$method" = GET ] || set -- "$@" --post-file /dev/stdin
wget -S -O- -T 10 "$@" "$url"
"""


def log(line):
    print("  " + line, file=sys.stderr, flush=True)


def run(*args, timeout=60, check=True, input=None):
    result = subprocess.run(
        args,
        cwd=ROOT,
        text=True,
        input=input,
        capture_output=True,
        timeout=timeout,
        check=False,
    )
    if check and result.returncode != 0:
        raise AssertionError(
            f"{shlex.join(args)} failed ({result.returncode}):\n{result.stdout}{result.stderr}"
        )
    return result


def read_config(name):
    values = {}
    for line in (ROOT / "config" / name / "config.env").read_text().splitlines():
        key, sep, value = line.strip().partition("=")
        if sep and not key.startswith("#"):
            values[key.removeprefix("export ").strip()] = value.strip().strip("'\"")
    return values


@unittest.skipUnless(CONFIGS, "set RESTMAIL_LIVE_JS_FILTER_CONFIGS to two configs")
class JSFilterIsolationLiveTest(unittest.TestCase):
    def setUp(self):
        self.assertEqual(len(CONFIGS), 2, "exactly two configs are required")
        self.assertTrue(
            shutil.which("docker") and shutil.which("chore"), "needs docker and chore"
        )
        self.a, self.b = (read_config(name) | {"CONFIG": name} for name in CONFIGS)
        projects = [c["RESTMAIL_PROJECT"] for c in (self.a, self.b)]
        self.assertNotEqual(projects[0], projects[1], "configs share a project")
        for project in projects:
            existing = run(
                "docker", "ps", "-aq", "--filter", "name=^/" + project + "-"
            ).stdout.strip()
            if existing:
                self.fail(
                    f"containers for {project} already exist; this check owns its "
                    "instances and will not touch a running stack"
                )
            network = run(
                "docker", "network", "inspect", project + "-js-filter-net", check=False
            )
            self.assertNotEqual(
                network.returncode,
                0,
                "private network already exists; refusing ownership",
            )
        for config in (self.a, self.b):
            # Registered before starting, so a half-started instance is stopped.
            self.addCleanup(self.chore, "instance:down", config)

    def chore(self, task, config, check=True):
        log("chore {} CONFIG={}".format(task, config["CONFIG"]))
        return run(
            "chore", task, "CONFIG=" + config["CONFIG"], timeout=1800, check=check
        )

    def project(self, config):
        return config["RESTMAIL_PROJECT"]

    def filter_address(self, config):
        return run(
            "sh", "tasks/js-filter-network.sh", "address", self.project(config)
        ).stdout.strip()

    def check_network(self, config):
        project = self.project(config)
        network = json.loads(
            run("docker", "network", "inspect", project + "-js-filter-net").stdout
        )[0]
        peers = sorted(c["Name"] for c in network["Containers"].values())
        self.assertTrue(network["Internal"], network["Name"] + " is not internal")
        self.assertEqual(peers, [project + "-api", project + "-js-filter"])
        log("{}: Internal=true, peers {}".format(network["Name"], " ".join(peers)))

    def call_filter(self, config, route, token):
        body = {"script": SCRIPT, "email": EMAIL}
        args = [
            "docker",
            "exec",
            "-e",
            "ROUTE=" + route,
            "-e",
            "BODY=" + json.dumps(body),
        ]
        if not token:
            args += ["-e", "NO_TOKEN=1"]
        return run(*args, self.project(config) + "-api", "sh", "-c", CALL).stdout

    def wait_for_filter(self, config):
        """Wait, within a bound, for a starting filter to accept connections.

        Only a filter still starting is waited for: a container that stopped or
        restarted has crashed, and any other probe result is a fault, so each
        fails at once. The probe runs inside the filter, so a broken network
        path between the API and the filter still fails the checks after this.
        """
        container = self.project(config) + "-js-filter"
        target = f"127.0.0.1:{FILTER_PORT}"

        def not_ready(reason):
            logs = run("docker", "logs", "--tail", "20", container, check=False)
            self.fail(
                f"js-filter service {container} is not ready: {reason}\n"
                f"last log lines:\n{logs.stdout}{logs.stderr}"
            )

        deadline = time.monotonic() + FILTER_READY_TIMEOUT
        while True:
            state = run(
                "docker",
                "inspect",
                "--format",
                "{{.State.Status}} restarts={{.RestartCount}}",
                container,
                check=False,
            )
            state = (state.stdout + state.stderr).strip()
            if state != "running restarts=0":
                not_ready(state)
            code, outcome = self.probe(container, target)
            if code == 0:
                return
            if outcome != "ECONNREFUSED":
                not_ready(f"probe of {target} exited {code}: {outcome}")
            if time.monotonic() >= deadline:
                not_ready(
                    f"{target} still refused connections after {FILTER_READY_TIMEOUT}s"
                )
            time.sleep(FILTER_READY_INTERVAL)

    def check_api_to_filter(self, config):
        project = self.project(config)
        self.wait_for_filter(config)
        url = run(
            "docker", "exec", project + "-api", "sh", "-c", 'echo "$JS_FILTER_URL"'
        ).stdout.strip()
        expected = f"http://{self.filter_address(config)}:{FILTER_PORT}"
        self.assertEqual(url, expected, "API is not using the filter's private address")
        for route, success in (
            ("/execute", '"continue"'),
            ("/validate", '"valid":true'),
        ):
            output = self.call_filter(config, route, token=True)
            self.assertIn("200 OK", output)
            self.assertIn(success, output)
            self.assertIn("exit=0", output)
            output = self.call_filter(config, route, token=False)
            self.assertIn("401 Unauthorized", output)
            self.assertNotIn("exit=0", output)
            log(f"{project}-api -> {url}{route}: 200 with token, 401 without")

    def api_request(self, config, route, *, body=None, token="", status=200):
        result = run(
            "docker",
            "exec",
            "-i",
            self.project(config) + "-api",
            "sh",
            "-c",
            API_CALL,
            "sh",
            "http://127.0.0.1:8080/api/v1" + route,
            "GET" if body is None else "POST",
            input=token + "\n" + (json.dumps(body) if body is not None else ""),
            check=False,
        )
        statuses = re.findall(r"HTTP/\S+ (\d{3})", result.stderr)
        self.assertTrue(statuses, "Go API returned no HTTP status for " + route)
        self.assertEqual(int(statuses[-1]), status, "Go API request failed: " + route)
        self.assertEqual(result.returncode, 0, "Go API request failed: " + route)
        try:
            return json.loads(result.stdout)
        except ValueError:
            self.fail("Go API returned invalid JSON for " + route)

    def check_go_api(self, config):
        login = self.api_request(
            config,
            "/auth/login",
            body={
                "email": "admin@" + config["RESTMAIL_HOSTNAME"],
                "password": "admin123!@",
                "client": "native",
            },
        )
        token = login["data"]["access_token"]
        self.assertTrue(token, "native login returned no access token")
        domains = self.api_request(config, "/admin/domains", token=token)["data"]
        domain = next(d for d in domains if d["name"] == config["RESTMAIL_HOSTNAME"])
        created = self.api_request(
            config,
            "/admin/custom-filters",
            token=token,
            status=201,
            body={
                "domain_id": domain["id"],
                "name": "private-network-live-check",
                "filter_type": "action",
                "direction": "both",
                "config": {"script": SCRIPT},
            },
        )
        validated = self.api_request(
            config,
            "/admin/custom-filters/validate",
            token=token,
            body={"script": SCRIPT},
        )
        self.assertIs(validated.get("valid"), True)
        executed = self.api_request(
            config,
            f"/admin/custom-filters/{created['data']['id']}/test",
            token=token,
            body={},
        )
        self.assertEqual(executed["result"], {"type": "action", "action": "continue"})
        log(
            self.project(config)
            + " Go API -> sidecar /validate: valid=true; /execute: action=continue"
        )

    def probe(self, container, target):
        result = run(
            "docker", "exec", container, "node", "-e", PROBE, target, check=False
        )
        return result.returncode, (result.stdout + result.stderr).strip()

    def positive_control(self, config, target):
        project = self.project(config)
        image = run(
            "docker", "inspect", "--format", "{{.Config.Image}}", project + "-js-filter"
        ).stdout.strip()
        result = run(
            "docker",
            "run",
            "--rm",
            "--name",
            project + "-network-probe",
            "--network",
            "container:" + project + "-api",
            "--entrypoint",
            "node",
            image,
            "-e",
            PROBE,
            target,
            check=False,
        )
        self.assertEqual(
            result.returncode,
            0,
            f"positive control {target}: {result.stdout}{result.stderr}",
        )
        self.assertEqual(result.stdout.strip(), "connected")
        log(f"{project}-api network -> {target}: connected (positive control)")

    def check_egress(self, config, other):
        container = self.project(config) + "-js-filter"
        # The probe must work, or every "unreachable" below would prove nothing.
        code, outcome = self.probe(container, f"127.0.0.1:{FILTER_PORT}")
        self.assertEqual(code, 0, "probe cannot reach the filter itself: " + outcome)
        targets = PUBLIC_TARGETS + [
            config["RESTMAIL_TESTBED_DNS_IP"] + ":53",
            config["RESTMAIL_POSTGRES_IP"] + ":5432",
            config["RESTMAIL_API_IP"] + ":8080",
            f"{self.filter_address(other)}:{FILTER_PORT}",
            f"{self.project(other)}-js-filter:{FILTER_PORT}",
        ]
        for target in targets:
            if target == f"{self.project(other)}-js-filter:{FILTER_PORT}":
                code, outcome = self.probe(self.project(other) + "-js-filter", target)
                self.assertEqual(
                    (code, outcome),
                    (0, "connected"),
                    "other filter name positive control failed",
                )
            else:
                control = (
                    other
                    if target == f"{self.filter_address(other)}:{FILTER_PORT}"
                    else config
                )
                self.positive_control(control, target)
            code, outcome = self.probe(container, target)
            self.assertEqual(code, 1, f"probe failed unexpectedly: {outcome}")
            self.assertIn(
                outcome,
                {
                    "timeout",
                    "EHOSTUNREACH",
                    "ENETUNREACH",
                    "ECONNREFUSED",
                    "ENOTFOUND",
                    "EAI_AGAIN",
                },
            )
            log(f"{container} -> {target}: unreachable ({outcome})")

    def check_api_dns(self, config):
        project, dns = self.project(config), config["RESTMAIL_TESTBED_DNS_IP"]
        api = project + "-api"
        networks = json.loads(
            run(
                "docker",
                "inspect",
                "--format",
                "{{json .NetworkSettings.Networks}}",
                api,
            ).stdout
        )
        self.assertEqual(
            sorted(networks),
            sorted([config["RESTMAIL_NETWORK"], project + "-js-filter-net"]),
        )
        resolv = run("docker", "exec", api, "cat", "/etc/resolv.conf").stdout
        self.assertEqual(
            [line for line in resolv.splitlines() if line.startswith("nameserver")],
            ["nameserver " + dns],
        )
        name = config["RESTMAIL_HOSTNAME"]
        result = run("docker", "exec", api, "nslookup", name, check=False)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(config["RESTMAIL_SMTP_IP"], result.stdout)
        log(
            "{}: networks {}, resolver {}, {} resolves".format(
                api, " ".join(sorted(networks)), dns, name
            )
        )

    def check_instance(self, config, other):
        for attempt in range(60):
            health = run(
                "docker",
                "inspect",
                "--format",
                "{{.State.Health.Status}}",
                self.project(config) + "-js-filter",
            ).stdout.strip()
            if health == "healthy":
                break
            time.sleep(0.5)
        self.assertEqual(health, "healthy", "filter did not become healthy")
        self.check_network(config)
        self.check_api_to_filter(config)
        self.check_go_api(config)
        self.check_egress(config, other)
        self.check_api_dns(config)

    def test_private_network_isolation_and_lifecycle(self):
        a, b = self.a, self.b
        self.chore("instance:up", a)
        self.chore("instance:up", b)
        self.check_instance(a, b)
        self.check_instance(b, a)

        def instance_ids(config):
            return run(
                "docker",
                "ps",
                "-aq",
                "--no-trunc",
                "--filter",
                "name=^/" + self.project(config) + "-",
            ).stdout.split()

        other_ids = instance_ids(b)
        api_id = run(
            "docker", "inspect", "--format", "{{.Id}}", self.project(a) + "-api"
        ).stdout.strip()
        filter_id = run(
            "docker", "inspect", "--format", "{{.Id}}", self.project(a) + "-js-filter"
        ).stdout.strip()

        # Recreating the filter alone keeps the network valid. Docker may give
        # it a new address, which the API reads only when it starts, so the API
        # is recreated after it.
        before = self.filter_address(a)
        self.chore("js-filter:restart", a)
        self.assertEqual(
            run(
                "docker", "inspect", "--format", "{{.Id}}", self.project(a) + "-api"
            ).stdout.strip(),
            api_id,
        )
        self.assertNotEqual(
            run(
                "docker",
                "inspect",
                "--format",
                "{{.Id}}",
                self.project(a) + "-js-filter",
            ).stdout.strip(),
            filter_id,
        )
        self.check_network(a)
        after = self.filter_address(a)
        log(f"filter address {before} -> {after} after recreating the filter")
        if before == after:
            self.check_api_to_filter(a)
            self.check_go_api(a)
        self.chore("api:up", a)
        self.chore("health:wait", a)
        self.check_instance(a, b)

        # Stopping removes the network; starting again recreates it.
        project = self.project(a)
        self.chore("instance:down", a)
        gone = run(
            "docker", "network", "inspect", project + "-js-filter-net", check=False
        )
        self.assertNotEqual(gone.returncode, 0, "network survived instance:down")
        left = run("docker", "ps", "-aq", "--filter", "name=^/" + project + "-").stdout
        self.assertEqual(left.strip(), "", "containers survived instance:down")
        log(f"{project}-js-filter-net removed by instance:down")
        self.assertEqual(instance_ids(b), other_ids, "stopping A changed instance B")
        self.check_network(b)
        self.check_api_to_filter(b)
        self.check_go_api(b)
        self.check_api_dns(b)
        self.chore("instance:up", a)
        self.check_instance(a, b)
        self.check_instance(b, a)
        self.assertEqual(instance_ids(b), other_ids, "restarting A changed instance B")
        log("instance B container IDs unchanged through A stop/start")


if __name__ == "__main__":
    unittest.main()
