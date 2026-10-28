"""Exercise service launch shells without building images or contacting Docker."""

import json
import os
import re
import shlex
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVICES = ("api", "smtp-gateway", "imap-gateway", "pop3-gateway")


class DevReadonlyTests(unittest.TestCase):
    def launch(self, service, mode, directory):
        prefix = service.upper().replace("-", "_")
        source = (ROOT / "tasks" / (service + ".yml")).read_text()
        # Execute the actual up command blocks. Only scalar Task templates in
        # these shell blocks are substituted; fail if a new expression appears.
        up = source.split("\n  up:\n", 1)[1]
        up = re.split(r"\n  [a-z][\w-]*:\n", up, maxsplit=1)[0]
        blocks = up.split("      - |\n")[1:]
        script = "\n".join(textwrap.dedent(block) for block in blocks)
        values = {
            "ROOT_DIR": str(directory),
            "TASKFILE_DIR": str(ROOT / "tasks"),
            prefix + "_MODE": mode,
            prefix + "_INTERNAL_MTLS": "true",
            prefix + "_DB_PASS": "fixture-password",
            prefix + "_MASTER_KEY": "fixture-master-key",
            prefix + "_JWT_SECRET": "fixture-jwt-secret",
            prefix + "_JS_FILTER_TOKEN": "fixture-filter-token",
        }
        script = re.sub(
            r"{{\.(\w+)}}",
            lambda match: values.get(match[1], "fixture-" + match[1].lower()),
            script,
        )
        self.assertNotIn("{{", script)
        capture = directory / "docker.jsonl"
        capture.unlink(missing_ok=True)
        env = dict(
            os.environ,
            PATH=str(directory / "bin") + os.pathsep + os.environ["PATH"],
            CAPTURE=str(capture),
            OUTPUT="/dev/null",
            VERBOSE="",
        )
        subprocess.run(["sh", "-n"], input=script, text=True, check=True)
        result = subprocess.run(
            ["sh", "-eu"],
            input=script,
            cwd=directory,
            env=env,
            text=True,
            capture_output=True,
            timeout=10,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = [json.loads(line) for line in capture.read_text().splitlines()]
        command = ["create", "--name"] if service == "api" else ["run", "-d"]
        launch = [call for call in calls if call["args"][:2] == command]
        self.assertEqual(len(launch), 1)
        stage = [call["args"] for call in calls if call["args"][:2] == ["run", "--rm"]]
        self.assertEqual(len(stage), 1)
        self.assertFalse(list((directory / ".cache").glob("*.secrets.env")))
        return launch[0], stage[0]

    def test_dev_mounts_and_production_boundary(self):
        for service in SERVICES:
            with self.subTest(service=service), tempfile.TemporaryDirectory() as temp:
                directory = Path(temp)
                (directory / "bin").mkdir()
                docker = directory / "bin/docker"
                docker.write_text("""#!/usr/bin/env python3
import json, os, stat, sys
from pathlib import Path
args = sys.argv[1:]
call = {"args": args}
if "--env-file" in args:
    path = Path(args[args.index("--env-file") + 1])
    call["secrets_mode"] = stat.S_IMODE(path.stat().st_mode)
    call["secrets"] = path.read_text().splitlines()
with open(os.environ["CAPTURE"], "a") as output:
    output.write(json.dumps(call) + "\\n")
if args[:2] == ["network", "inspect"] and "range .Containers" not in " ".join(args):
    print("true bridge fixture-api_project")
elif args[0] == "inspect":
    print("1 172.30.0.2")
""")
                docker.chmod(0o755)
                dev, dev_stage = self.launch(service, "dev", directory)
                prod, prod_stage = self.launch(service, "prod", directory)
                uid = 0 if service == "api" else 10001
                flags = shlex.split(f"""--read-only -v {directory}:/app
                    --tmpfs /app/tmp:rw,exec,nosuid,nodev,size=128m,mode=0700,uid={uid},gid={uid}
                    --tmpfs /tmp:rw,exec,nosuid,nodev,size=1g,mode=1777
                    -e GOPATH=/tmp/go -e GOMODCACHE=/tmp/go/pkg/mod
                    -e GOCACHE=/tmp/go-build-cache
                    -e GOPROXY=file:///go/pkg/mod/cache/download,https://proxy.golang.org,direct""")
                start = dev["args"].index("--read-only")
                self.assertEqual(dev["args"][start : start + len(flags)], flags)
                # Removing the complete dev-only fragment must leave exactly
                # the production invocation, including data mounts and flags.
                self.assertEqual(
                    dev["args"][:start] + dev["args"][start + len(flags) :],
                    prod["args"],
                )
                self.assertNotIn("--read-only", prod["args"])
                self.assertNotIn("--tmpfs", prod["args"])
                for call in (dev, prod):
                    args = call["args"]
                    self.assertIn(
                        "fixture-"
                        + service.replace("-", "_")
                        + "_own_certs_volume:/certs:ro",
                        args,
                    )
                    self.assertIn("no-new-privileges:true", args)
                    self.assertEqual(args[args.index("--cap-drop") + 1], "ALL")
                    self.assertEqual(call["secrets_mode"], 0o600)
                    self.assertIn("DB_PASS=fixture-password", call["secrets"])
                    self.assertNotIn("fixture-password", " ".join(args))
                    if service != "api":
                        self.assertEqual(args[args.index("--user") + 1], "10001:10001")
                        self.assertIn(
                            "fixture-"
                            + service.replace("-", "_")
                            + "_logs_volume:/var/log/restmail",
                            args,
                        )
                    else:
                        self.assertIn(
                            "fixture-api_attachments_volume:/attachments", args
                        )
                # The actual staging helper still receives the same certificate
                # owner and enforces private-key modes before the service runs.
                for stage, owner in (
                    (dev_stage, f"{uid}:{uid}"),
                    (prod_stage, "10001:10001"),
                ):
                    self.assertEqual(stage[stage.index("_") + 1], owner)
                    self.assertIn('chmod 0400 "/to/$f"', stage[stage.index("-euc") + 1])
                    self.assertTrue(any("internal-ca.crt" in arg for arg in stage))


if __name__ == "__main__":
    unittest.main()
