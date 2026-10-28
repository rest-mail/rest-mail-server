"""Check the js-filter private network against two real, running instances.

Opt-in: it starts and stops real containers, so it runs only when
RESTMAIL_LIVE_JS_FILTER_CONFIGS names two scaffolded configs, for example

    RESTMAIL_LIVE_JS_FILTER_CONFIGS="isolation-a.test isolation-b.test" \\
      python3 -m unittest scripts/tests/test_js_filter_isolation_live.py -v

Both configs need their own project name and mailnet addresses
(`chore config:scaffold <domain>`), and the testbed must be up. The test owns
the two instances: it refuses to run if any of their containers already exist,
starts them with `chore instance:up`, and stops them with `chore instance:down`
when it finishes, pass or fail. Data volumes are kept, as instance:down keeps
them. Each check prints one line, so the output can be attached as a record.
"""

import json
import os
import shlex
import shutil
import subprocess
import sys
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


def log(line):
    print("  " + line, file=sys.stderr, flush=True)


def run(*args, timeout=60, check=True):
    result = subprocess.run(
        args, cwd=ROOT, text=True, capture_output=True, timeout=timeout, check=False
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


@unittest.skipUnless(
    len(CONFIGS) == 2, "set RESTMAIL_LIVE_JS_FILTER_CONFIGS to two configs"
)
@unittest.skipUnless(
    shutil.which("docker") and shutil.which("chore"), "needs docker and chore"
)
class JSFilterIsolationLiveTest(unittest.TestCase):
    def setUp(self):
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
        for config in (self.a, self.b):
            # Registered before starting, so a half-started instance is stopped.
            self.addCleanup(self.chore, "instance:down", config, check=False)

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

    def check_api_to_filter(self, config):
        project = self.project(config)
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

    def probe(self, container, target):
        result = run(
            "docker", "exec", container, "node", "-e", PROBE, target, check=False
        )
        return result.returncode, (result.stdout + result.stderr).strip()

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
            code, outcome = self.probe(container, target)
            self.assertNotEqual(code, 0, f"{container} reached {target}")
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
        log(
            "{}: networks {}, resolver {}, {} resolves".format(
                api, " ".join(sorted(networks)), dns, name
            )
        )

    def check_instance(self, config, other):
        self.check_network(config)
        self.check_api_to_filter(config)
        self.check_egress(config, other)
        self.check_api_dns(config)

    def test_private_network_isolation_and_lifecycle(self):
        a, b = self.a, self.b
        self.chore("instance:up", a)
        self.chore("instance:up", b)
        self.check_instance(a, b)
        self.check_instance(b, a)

        # Recreating the filter alone keeps the network valid. Docker may give
        # it a new address, which the API reads only when it starts, so the API
        # is recreated after it.
        before = self.filter_address(a)
        self.chore("js-filter:restart", a)
        self.check_network(a)
        after = self.filter_address(a)
        log(f"filter address {before} -> {after} after recreating the filter")
        if before == after:
            self.check_api_to_filter(a)
        self.chore("api:up", a)
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
        self.chore("instance:up", a)
        self.check_instance(a, b)
        self.check_instance(b, a)


if __name__ == "__main__":
    unittest.main()
