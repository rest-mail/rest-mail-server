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
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SERVICES = ("api", "smtp-gateway", "imap-gateway", "pop3-gateway")


class DevReadonlyTests(unittest.TestCase):
    def launch(self, service, mode, directory, **extra_env):
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
            prefix + "_API_BASE_URL": "http://192.0.2.20:8080/restmail",
            # Only the contract fault run builds with tags.
            "API_BUILD_TAGS": "",
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
            RESTMAIL_LABEL="rest-mail.run=fixture",
            PATH=str(directory / "bin") + os.pathsep + os.environ["PATH"],
            CAPTURE=str(capture),
            OUTPUT="/dev/null",
            VERBOSE="",
            **extra_env,
        )
        if "RESTMAIL_DEV_GOCACHE_VOLUME" not in extra_env:
            env.pop("RESTMAIL_DEV_GOCACHE_VOLUME", None)
        subprocess.run(["sh", "-n"], input=script, text=True, check=True)
        result = subprocess.run(
            ["sh", "-eu"],
            input=script,
            cwd=directory,
            env=env,
            text=True,
            capture_output=True,
            timeout=600,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = [json.loads(line) for line in capture.read_text().splitlines()]
        command = ["create", "--name"] if service == "api" else ["run", "-d"]
        front = [call["args"] for call in calls if call["args"][:2] == command and any(arg.endswith("-https") for arg in call["args"])]
        if service == "smtp-gateway":
            self.assertEqual(len(front), 1)
            self.assertIn("container:fixture-smtp_gateway_container_name", front[0])
            self.assertIn("fixture-smtp_gateway_own_certs_volume:/certs:ro", front[0])
            for flag in ("--read-only", "--cap-drop", "ALL", "no-new-privileges:true", "10001:10001"):
                self.assertIn(flag, front[0])
            watcher = front[0].index("tls-front")
            self.assertEqual(front[0][watcher - 1], (ROOT / "tasks/tls-front-reload.sh").read_text().rstrip("\n"))
            self.assertEqual(front[0][watcher + 2:], ["fixture-smtp_gateway_tls_cert_path", "fixture-smtp_gateway_tls_key_path"])
            for setting in ("listen 443 ssl", "ssl_certificate fixture-smtp_gateway_tls_cert_path", "ssl_certificate_key fixture-smtp_gateway_tls_key_path", "proxy_pass 192.0.2.20:8080;"):
                self.assertIn(setting, front[0][watcher + 1])
        else:
            self.assertEqual(front, [])
        launch = [call for call in calls if call["args"][:2] == command and not any(arg.endswith("-https") for arg in call["args"])]
        self.assertEqual(len(launch), 1)
        stage = [call["args"] for call in calls if call["args"][:2] == ["run", "--rm"]]
        self.assertEqual(len(stage), 1)
        self.assertFalse(list((directory / ".cache").glob("*.secrets.env")))
        return launch[0], stage[0]

    def test_smtp_https_front_stops_before_gateway(self):
        source = (ROOT / "tasks/smtp-gateway.yml").read_text()
        down = source.split("\n  down:\n", 1)[1].split("\n  restart:\n", 1)[0]
        script = textwrap.dedent(down.split("      - |\n", 1)[1])
        script = script.replace("{{.SMTP_GATEWAY_CONTAINER_NAME}}", "fixture-smtp-gateway")
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            capture = directory / "docker.jsonl"
            docker = directory / "docker"
            docker.write_text('''#!/usr/bin/env python3
import json, os, sys
with open(os.environ["CAPTURE"], "a") as output:
    output.write(json.dumps(sys.argv[1:]) + "\\n")
if sys.argv[1] == "ps":
    print("fixture-container-id")
''')
            docker.chmod(0o755)
            subprocess.run(
                ["sh", "-eu"], input=script, text=True, check=True,
                capture_output=True,
                env={**os.environ, "PATH": str(directory) + os.pathsep + os.environ["PATH"],
                     "CAPTURE": str(capture), "OUTPUT": "/dev/null"},
            )
            calls = [json.loads(line) for line in capture.read_text().splitlines()]
            removals = [args for args in calls if args[:2] == ["rm", "-f"]]
            self.assertEqual(removals, [
                ["rm", "-f", "fixture-smtp-gateway-https"],
                ["rm", "-f", "fixture-smtp-gateway"],
            ])

    def test_dev_mounts_and_production_boundary(self):
        for service in SERVICES:
            with self.subTest(service=service), tempfile.TemporaryDirectory() as temp:
                # The scripts report canonical paths; macOS aliases /var to
                # /private/var, so compare against the resolved directory.
                directory = Path(temp).resolve()
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
                with patch.dict(os.environ, {"RESTMAIL_DEV_GOCACHE_VOLUME": "inherited-cache"}):
                    dev, dev_stage = self.launch(service, "dev", directory)
                    prod, prod_stage = self.launch(service, "prod", directory)
                uid = 0 if service == "api" else 10001
                prefix = service.replace("-", "_")
                self.assertIn(f"fixture-{prefix}_project-devcache-{uid}:/cache", dev["args"])
                self.assertNotIn("--tmpfs", dev["args"])
                cached, _ = self.launch(service, "dev", directory, RESTMAIL_DEV_GOCACHE_VOLUME="fixture-cache")
                self.assertIn(f"fixture-cache-{uid}:/cache", cached["args"])
                prod_cached, _ = self.launch(service, "prod", directory, RESTMAIL_DEV_GOCACHE_VOLUME="fixture-cache")
                self.assertEqual(prod_cached["args"], prod["args"])
                flags = shlex.split(f"""--read-only -v {directory}:/app
                    -v fixture-{prefix}_container_name-tmp:/tmp
                    -v fixture-{prefix}_container_name-air:/app/tmp
                    -v fixture-{prefix}_project-devcache-{uid}:/cache
                    -e GOPATH=/cache/go -e GOMODCACHE=/cache/go/pkg/mod
                    -e GOCACHE=/cache/build
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
                topology, _ = self.launch(service, "prod", directory, RESTMAIL_TOPOLOGY_STORAGE="true")
                self.assertIn("--read-only", topology["args"])
                self.assertNotIn("--tmpfs", topology["args"])
                self.assertIn(f"fixture-{prefix}_container_name-tmp:/tmp", topology["args"])
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
