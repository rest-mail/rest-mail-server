"""Exercise the local runner's startup and killed-run recovery (#2113)."""

import importlib.util
import json
import os
import signal
import subprocess
import sys
import tempfile
import time
import unittest
import uuid
from pathlib import Path
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


def load_script(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


gate = load_script("ci_local")
scratch = load_script("scratch_run")
LIVE = os.environ.get("RESTMAIL_SUBSTRATE_LIVE") == "1"

DOCKER_FIXTURE = r"""
import json, os, re, sys, uuid
from pathlib import Path

path = Path(os.environ["DOCKER_FIXTURE_STATE"])
state = json.loads(path.read_text()) if path.exists() else {}
args = sys.argv[1:]

if args[0] == "run":
    name = args[args.index("--name") + 1]
    label = args[args.index("--label") + 1] if "--label" in args else ""
    identity = uuid.uuid4().hex * 2
    state[identity] = {"Id": identity, "Name": "/" + name, "State": {"Running": True},
                       "Config": {"Labels": dict([label.split("=", 1)]) if label else {}}}
    print(identity)
elif args[0] == "ps":
    matches = list(state.values())
    for index, argument in enumerate(args):
        if argument != "--filter":
            continue
        kind, value = args[index + 1].split("=", 1)
        if kind == "name":
            matches = [item for item in matches if re.search(value, item["Name"])]
        elif kind == "label":
            key, expected = value.split("=", 1)
            matches = [item for item in matches if item["Config"]["Labels"].get(key) == expected]
        else:
            raise AssertionError(args)
    print("\n".join(item["Id"] for item in matches))
elif args[:2] == ["container", "inspect"]:
    if args[-1] not in state:
        sys.exit(1)
    print(json.dumps([state[args[-1]]]))
elif args[0] == "rm":
    for identity in args[2:]:
        del state[identity]
elif args[:2] not in (["network", "ls"], ["volume", "ls"]):
    raise AssertionError(args)
path.write_text(json.dumps(state))
"""


class Substrate(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="ac2113-")
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)

    def local_gate(self):
        local = gate.Gate(ROOT, self.directory, self.directory, False, "ac2113")
        local.packages = ["mod/db"]
        local.workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        return local

    def test_ac02_startup_waits_for_docker_and_postgres_and_bounds_failures(self):
        for scenario in (
            "delayed",
            "daemon absent",
            "daemon hung",
            "postgres hung",
            "postgres crashed",
            "create failed",
        ):
            with self.subTest(scenario=scenario):
                self.assert_startup_scenario(scenario)

        if LIVE:
            local = self.local_gate()
            self.addCleanup(local.cleanup)
            local.postgres()
            self.assertNotEqual(local.env["DB_PORT"], "5432")
            locale = self.docker(
                "exec",
                local.container_id,
                "psql",
                "-U",
                "restmail",
                "-d",
                "restmail_test",
                "-Atc",
                "SELECT datlocprovider FROM pg_database WHERE datname = current_database()",
            ).stdout.strip()
            self.assertEqual(locale, "i")
            self.assertTrue((self.directory / "testdb-exec").is_file())
            print(
                f"live startup: PostgreSQL accepted connections on {local.env['DB_PORT']}; locale provider={locale}"
            )

    def assert_startup_scenario(self, scenario):
        local = self.local_gate()
        initial_port = local.env.get("DB_PORT")
        clock = 0
        events = []

        def advance(seconds):
            nonlocal clock
            clock += seconds

        def run(args, **kwargs):
            if args[0] == "go":
                self.assertIn(
                    "postgres ready",
                    events,
                    "initialized the dependent before PostgreSQL readiness",
                )
                self.assertEqual(local.env["DB_PORT"], "49152")
                events.append("dependent initialized")
                return subprocess.CompletedProcess(args, 0)
            timeout = kwargs["timeout"]
            self.assertGreater(timeout, 0)
            action = args[1]
            if action == "info":
                events.append("docker probe")
                self.assertLessEqual(timeout, 2)
                if scenario == "daemon hung":
                    advance(timeout)
                    raise subprocess.TimeoutExpired(args, timeout)
                if (
                    scenario == "daemon absent"
                    or events.count("docker probe") < 3
                ):
                    raise subprocess.CalledProcessError(
                        1, args, stderr="no such Docker socket"
                    )
                events.append("docker ready")
            elif action == "run":
                self.assertEqual(timeout, 300)
                self.assertIn(
                    "docker ready",
                    events,
                    "started PostgreSQL before Docker readiness",
                )
                events.append("postgres created")
                if scenario == "create failed":
                    raise subprocess.CalledProcessError(
                        125, args, stderr="no space left on device"
                    )
                return subprocess.CompletedProcess(args, 0, "f" * 64)
            elif action == "port":
                self.assertEqual(timeout, 300)
                return subprocess.CompletedProcess(args, 0, "127.0.0.1:49152\n")
            elif action == "exec":
                self.assertEqual(args[3:6], ["pg_isready", "-h", "127.0.0.1"])
                events.append("postgres probe")
                self.assertLessEqual(timeout, 2)
                if scenario == "postgres hung":
                    advance(timeout)
                    raise subprocess.TimeoutExpired(args, timeout)
                if (
                    scenario == "postgres crashed"
                    or events.count("postgres probe") < 3
                ):
                    return subprocess.CompletedProcess(
                        args, 2, "no response", ""
                    )
                events.append("postgres ready")
            elif action == "inspect":
                self.assertLessEqual(timeout, 2)
                state = (
                    "exited 1"
                    if scenario == "postgres crashed"
                    else "running 0"
                )
                return subprocess.CompletedProcess(args, 0, state)
            elif action == "logs":
                self.assertEqual(args[2:4], ["--tail", "20"])
                self.assertLessEqual(timeout, 2)
                return subprocess.CompletedProcess(
                    args, 0, "FATAL: incompatible data directory"
                )
            elif args[1:3] != ["image", "inspect"]:
                self.fail(f"unexpected command: {args}")
            return subprocess.CompletedProcess(args, 0, "")

        with (
            patch.object(gate.subprocess, "run", side_effect=run),
            patch.object(gate.time, "monotonic", side_effect=lambda: clock),
            patch.object(gate.time, "sleep", side_effect=advance),
        ):
            if scenario == "delayed":
                local.postgres()
                self.assertEqual(
                    events[-2:], ["postgres ready", "dependent initialized"]
                )
                self.assertEqual(events.count("docker probe"), 3)
                self.assertEqual(events.count("postgres probe"), 3)
                self.assertEqual(clock, 4)
            elif scenario.startswith("daemon"):
                reason = (
                    "no such Docker socket"
                    if scenario == "daemon absent"
                    else "timed out"
                )
                with self.assertRaisesRegex(
                    gate.CouldNotRun,
                    f"check its context/socket.*not ready within 10s.*{reason}",
                ):
                    local.postgres()
                self.assertEqual(clock, 10)
                self.assertEqual(set(events), {"docker probe"})
            else:
                reason = {
                    "postgres hung": "not ready within 60s",
                    "postgres crashed": "container exited 1",
                    "create failed": "could not start container: no space left on device",
                }[scenario]
                with self.assertRaisesRegex(
                    RuntimeError, f"PostgreSQL .*{reason}"
                ) as failure:
                    local.postgres()
                self.assertEqual(events.count("postgres created"), 1)
                self.assertNotIn("dependent initialized", events)
                self.assertEqual(local.env.get("DB_PORT"), initial_port)
                if scenario == "postgres hung":
                    self.assertEqual(clock, 62)
                elif scenario == "postgres crashed":
                    self.assertEqual(clock, 2)
                    self.assertIn(
                        "FATAL: incompatible data directory",
                        str(failure.exception),
                    )

    def docker(self, *args, env=None, check=True):
        return subprocess.run(
            ["docker", *args],
            env=env,
            check=check,
            capture_output=True,
            text=True,
            timeout=600,
        )

    def test_ac03_killed_run_cleanup_spares_unlabelled_reused_pid_and_unrelated_orphan(
        self,
    ):
        environment = dict(os.environ)
        if not LIVE:
            docker = self.directory / "docker"
            docker.write_text(f"#!{sys.executable}\n" + DOCKER_FIXTURE)
            docker.chmod(0o700)
            environment.update(
                PATH=str(self.directory) + os.pathsep + environment["PATH"],
                DOCKER_FIXTURE_STATE=str(self.directory / "docker.json"),
            )
        topology = "ac2113-" + uuid.uuid4().hex[:10]
        label = "rest-mail-loop=" + topology
        prefix = "restmail-" + topology
        receipt = self.directory / "receipt.json"
        ready = self.directory / "owned.json"
        scratch_dir = self.directory / "scratch"

        def remove_container(identity):
            self.docker("rm", "-fv", identity, env=environment, check=False)

        unlabelled = self.docker(
            "run",
            "-d",
            "--name",
            prefix + "-unlabelled",
            "alpine:3.20",
            "sleep",
            "900",
            env=environment,
        ).stdout.strip()
        self.addCleanup(remove_container, unlabelled)

        def start(*args, **extra):
            process = subprocess.Popen(
                args, env=environment | extra, start_new_session=True
            )
            self.addCleanup(process.wait, timeout=600)
            self.addCleanup(lambda: process.poll() is None and process.kill())
            return process

        reused = start(sys.executable, "-c", "import time; time.sleep(900)")
        orphan_leader = start(
            sys.executable,
            "-c",
            """
import subprocess, sys
from pathlib import Path
orphan = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(900)'])
Path(sys.argv[1]).write_text(str(orphan.pid))
""",
            str(self.directory / "orphan"),
        )
        self.assertEqual(orphan_leader.wait(timeout=600), 0)
        orphan = int((self.directory / "orphan").read_text())
        self.assertEqual(os.getpgid(orphan), orphan_leader.pid)
        orphan_start = scratch.process_start(orphan)

        def kill_if_same(pid, birth):
            if scratch.process_start(pid) == birth:
                os.kill(pid, signal.SIGKILL)

        self.addCleanup(kill_if_same, orphan, orphan_start)
        parent = start(
            sys.executable,
            str(ROOT / "scripts/scratch_run.py"),
            sys.executable,
            "-c",
            """
import json, os, subprocess, sys, time
from pathlib import Path
container = subprocess.check_output(['docker', 'run', '-d', '--label', sys.argv[1],
                                     '--name', sys.argv[2], 'alpine:3.20', 'sleep', '900'],
                                    text=True, timeout=600).strip()
destination = Path(sys.argv[3])
temporary = destination.with_suffix('.tmp')
temporary.write_text(json.dumps({'pid': os.getpid(), 'container': container}))
temporary.replace(destination)
time.sleep(900)
""",
            label,
            prefix + "-owned",
            str(ready),
            RESTMAIL_SCRATCH=str(scratch_dir),
            RESTMAIL_PROCESS_CLEANUP_RECEIPT=str(receipt),
        )

        def wait_for(predicate):
            deadline = time.monotonic() + 600
            while time.monotonic() < deadline:
                if predicate():
                    return
                time.sleep(0.05)
            self.fail("timed out waiting for " + predicate.__name__)

        wait_for(ready.is_file)
        owned = json.loads(ready.read_text())
        self.addCleanup(remove_container, owned["container"])
        birth = scratch.process_start(owned["pid"])
        self.assertTrue(birth)
        self.assertTrue(orphan_start)
        before = json.loads(
            self.docker(
                "container", "inspect", owned["container"], env=environment
            ).stdout
        )[0]
        self.assertEqual(before["Config"]["Labels"]["rest-mail-loop"], topology)
        self.assertTrue(before["State"]["Running"])

        parent.kill()
        self.assertEqual(parent.wait(timeout=600), -signal.SIGKILL)
        wait_for(lambda: scratch.process_start(owned["pid"]) is None)
        wait_for(lambda: receipt.is_file() and receipt.stat().st_size > 0)
        self.assertEqual(
            json.loads(receipt.read_text()),
            {"pid": owned["pid"], "start": birth, "outcome": "signalled"},
        )
        self.assertEqual(list(scratch_dir.iterdir()), [])

        wrong_birth = scratch.process_start(reused.pid) + "-stale"
        mismatch = scratch.remove_owned_group({"pid": reused.pid, "start": wrong_birth})
        self.assertEqual(
            mismatch["outcome"], "identity-mismatch", "cleanup signalled a reused PID"
        )
        self.assertIsNone(reused.poll())
        self.assertEqual(scratch.process_start(orphan), orphan_start)

        # Run the actual task recipe with disposable names; skip the unrelated VM cache drop.
        task = yaml.safe_load((ROOT / "tasks/topology.yml").read_text())["tasks"][
            "substrate:down"
        ]["cmds"][0]
        script = task.replace("{{.ROOT_DIR}}", str(ROOT)).replace(
            "{{.TOPOLOGY}}", topology
        )
        result = subprocess.run(
            ["sh", "-c", script],
            cwd=ROOT,
            env=environment | {"CONTAINERS_ONLY": "true"},
            capture_output=True,
            text=True,
            timeout=600,
            check=True,
        )
        self.assertIn(
            f"cleanup: removed container {owned['container']} ({label})", result.stdout
        )
        self.assertNotIn(unlabelled, result.stdout)
        self.assertNotEqual(
            self.docker(
                "container", "inspect", owned["container"], env=environment, check=False
            ).returncode,
            0,
        )
        survivor = json.loads(
            self.docker("container", "inspect", unlabelled, env=environment).stdout
        )[0]
        self.assertTrue(
            survivor["State"]["Running"], "unlabelled container did not survive"
        )
        self.assertNotIn("rest-mail-loop", survivor["Config"]["Labels"] or {})
        self.assertIsNone(reused.poll())
        self.assertEqual(scratch.process_start(orphan), orphan_start)
        if LIVE:
            print(f"live process receipt: {receipt.read_text()}")
            print(result.stdout.strip())
            print(
                f"live negative controls survived: container {unlabelled}, reused PID {reused.pid}, orphan {orphan}"
            )
