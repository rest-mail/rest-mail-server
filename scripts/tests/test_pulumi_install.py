"""Exercise the local component installer with a fake stack and no deployment."""

import hashlib
import json
import os
import pty
import select
import signal
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SIGNATURE = "4dabf18193072939515e22adb298388d"
SECRET = "1b47061264138c4ac30d75fd1eb44270"
KEY = "synthetic APNs test credential\nsecond fixture line\n"
COMPONENT = "urn:pulumi:prod::infra::restmail:deployment:RestMail::restmail"
RELEASE = "urn:pulumi:prod::infra::restmail:deployment:RestMail$kubernetes:helm.sh/v3:Release::restmail-release"
NAMESPACE = "urn:pulumi:prod::infra::restmail:deployment:RestMail$kubernetes:core/v1:Namespace::restmail-namespace"
CREDENTIAL = "urn:pulumi:prod::infra::restmail:deployment:RestMail$kubernetes:core/v1:Secret::restmail-apns"


def secret(value):
    return {SIGNATURE: SECRET, "value": value}


class PulumiInstall(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)
        self.project = self.path / "project with spaces"
        self.project.mkdir()
        (self.project / "Pulumi.yaml").write_text("name: infra\nruntime: nodejs\n")
        self.key = self.path / "APNs key.p8"
        self.key.write_text(KEY)
        self.config = self.path / "config.json"
        self.config.write_text(
            json.dumps(
                {
                    "restmail:ownerKeys": {"value": "fixture-public-key"},
                    "restmail:adminPasswordHash": {"value": "fixture-hash"},
                }
            )
        )
        self.calls = self.path / "calls.jsonl"
        self.preview = self.path / "preview.json"
        self.preview.write_text(json.dumps(self.component_preview()))
        binary = self.path / "bin"
        binary.mkdir()
        self.env = dict(
            os.environ,
            PATH=str(binary) + os.pathsep + os.environ["PATH"],
            PROJECT=str(self.project),
            USER_WORKING_DIR=str(self.path),
            STACK="prod",
            HOST="mail.example.test",
            DOMAIN="example.test",
            NAME="restmail",
            NAMESPACE="rest-mail",
            VERSION="0.40.0",
            PASSWORD="",
            OWNER_KEY="",
            APNS_KEY_FILE="",
            APNS_KEY_ID="",
            APNS_TEAM_ID="",
            APNS_TOPIC="",
            FIXTURE_CONFIG=str(self.config),
            FIXTURE_CALLS=str(self.calls),
            FIXTURE_PREVIEW=str(self.preview),
        )
        self.stub("helm", "#!/bin/sh\nexit 0\n")
        self.stub("kubectl", "#!/bin/sh\necho '{\"items\":[]}'\n")
        self.stub("restmail", "#!/bin/sh\ncat >/dev/null\necho fixture-hash\n")
        self.stub(
            "pulumi",
            """#!/usr/bin/env python3
import hashlib,json,os,sys
from pathlib import Path
a=sys.argv[1:]
entry={"args":a}
cfg=Path(os.environ["FIXTURE_CONFIG"])
values=json.loads(cfg.read_text())
if "set" in a and "--secret" in a:
    body=sys.stdin.read()
    entry["stdin_sha256"]=hashlib.sha256(body.encode()).hexdigest()
with open(os.environ["FIXTURE_CALLS"],"a") as f: f.write(json.dumps(entry)+"\\n")
if "--show-name" in a:
    if os.environ.get("OFFLINE"): sys.exit(1)
    print("prod")
elif "export" in a: print('{"deployment":{"resources":[]}}')
elif "config" in a and "--json" in a: print(json.dumps(values))
elif "get" in a:
    item=values.get(a[a.index("get")+1])
    if item is None: sys.exit(1)
    if item.get("secret"): raise RuntimeError("installer must never read a private key")
    print(item.get("value",""))
elif "set" in a:
    key=a[a.index("set")+1]
    if "--secret" in a:
        if os.environ.get("ENCRYPT_FAIL"):
            sys.stderr.write(body) # prove even a misbehaving CLI cannot echo key data
            sys.exit(19)
        values[key]={"secret":True} # model encryption; never retain fixture key text
    else: values[key]={"value":a[a.index("set")+2]}
    cfg.write_text(json.dumps(values))
elif "preview" in a: print(Path(os.environ["FIXTURE_PREVIEW"]).read_text())
elif "up" in a: sys.exit(17) # stop before any rollout/readiness operation
else: sys.exit(1)
""",
        )

    def stub(self, name, source):
        p = self.path / "bin" / name
        p.write_text(source)
        p.chmod(0o755)

    def component_preview(self):
        def step(urn, kind, inputs=None, parent=None):
            return {
                "op": "create",
                "urn": urn,
                "newState": {"type": kind, "inputs": inputs or {}, "parent": parent},
            }

        return {
            "steps": [
                step(COMPONENT, "restmail:deployment:RestMail"),
                step(NAMESPACE, "kubernetes:core/v1:Namespace", parent=COMPONENT),
                step(
                    CREDENTIAL,
                    "kubernetes:core/v1:Secret",
                    {"stringData": secret({"AuthKey.p8": KEY})},
                    COMPONENT,
                ),
                step(
                    RELEASE,
                    "kubernetes:helm.sh/v3:Release",
                    {
                        "name": "restmail",
                        "namespace": "rest-mail",
                        "version": "0.40.0",
                        "values": secret(
                            {"mailserver": {"hostname": secret("mail.example.test")}}
                        ),
                    },
                    COMPONENT,
                ),
                step("urn:unrelated", "kubernetes:core/v1:Namespace"),
            ]
        }

    def call(self, terminal=False):
        command = ["bash", str(ROOT / "tasks/lifecycle/pulumi.sh"), "install"]
        if not terminal:
            p = subprocess.run(
                command,
                check=False,
                cwd=self.path,
                env=self.env,
                text=True,
                capture_output=True,
                timeout=15,
            )
            return p.returncode, p.stdout + p.stderr
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(self.path)
            os.execvpe(command[0], command, self.env)
        output = b""
        answered = False
        deadline = time.monotonic() + 15
        try:
            while time.monotonic() < deadline:
                if not answered and b"component resources only?" in output:
                    os.write(fd, b"y\n")
                    answered = True
                if not select.select([fd], [], [], 0.05)[0]:
                    continue
                try:
                    chunk = os.read(fd, 65536)
                except OSError:
                    break
                if not chunk:
                    break
                output += chunk
            else:
                self.fail("Installer terminal timed out")
            _, status = os.waitpid(pid, 0)
            pid = None
            return os.waitstatus_to_exitcode(status), output.decode(errors="replace")
        finally:
            os.close(fd)
            if pid is not None:
                os.killpg(pid, signal.SIGKILL)
                os.waitpid(pid, 0)

    def records(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()]

    def configure_key(self):
        self.env.update(
            APNS_KEY_FILE=self.key.name,
            APNS_KEY_ID="KEY1234567",
            APNS_TEAM_ID="TEAM123456",
        )

    def test_encrypts_exact_file_on_stdin_and_targets_only_component_resources(self):
        self.configure_key()
        code, output = self.call(terminal=True)
        self.assertEqual(
            code, 1, output
        )  # fake up returns 17, installer reports failure
        records = self.records()
        self.assertTrue(
            all("--show-sames" in r["args"] for r in records if "preview" in r["args"])
        )
        encrypted = [r for r in records if "--secret" in r["args"]]
        self.assertEqual(len(encrypted), 1)
        self.assertIn("--raw", encrypted[0]["args"])
        self.assertIn("--non-interactive", encrypted[0]["args"])
        self.assertEqual(
            encrypted[0]["stdin_sha256"], hashlib.sha256(KEY.encode()).hexdigest()
        )
        self.assertNotIn(KEY.strip(), output)
        self.assertNotIn(KEY.strip(), json.dumps(records))
        targets = next(r["args"] for r in records if "up" in r["args"])
        selected = [
            targets[i + 1] for i, arg in enumerate(targets) if arg == "--target"
        ]
        self.assertEqual(set(selected), {COMPONENT, NAMESPACE, CREDENTIAL, RELEASE})
        self.assertNotIn("--target-dependents", targets)
        self.assertNotIn("urn:unrelated", targets)

    def test_keeps_existing_encrypted_key_and_metadata_without_reading_private_value(
        self,
    ):
        config = json.loads(self.config.read_text())
        config.update(
            {
                "restmail:apnsPrivateKey": {"secret": True},
                "restmail:apnsKeyId": {"value": "KEPTKEY"},
                "restmail:apnsTeamId": {"value": "KEPTTEAM"},
                "restmail:apnsTopic": {"value": "kept.topic"},
            }
        )
        self.config.write_text(json.dumps(config))
        code, output = self.call()
        self.assertEqual(code, 0, output)
        self.assertFalse(any("--secret" in r["args"] for r in self.records()))
        self.assertEqual(
            json.loads(self.config.read_text())["restmail:apnsTopic"]["value"],
            "kept.topic",
        )

    def test_refuses_plaintext_private_config_without_echoing_it(self):
        config = json.loads(self.config.read_text())
        config["restmail:apnsPrivateKey"] = {"value": KEY, "secret": False}
        self.config.write_text(json.dumps(config))
        code, output = self.call()
        self.assertEqual(code, 2, output)
        self.assertIn("is not encrypted", output)
        self.assertNotIn(KEY.strip(), output)
        self.assertFalse(
            any("set" in r["args"] or "up" in r["args"] for r in self.records())
        )

    def test_refuses_missing_file_identifiers_and_inaccessible_stack_before_mutation(
        self,
    ):
        for change in [
            {"APNS_KEY_FILE": "missing.p8"},
            {"APNS_KEY_ID": ""},
            {"APNS_KEY_FILE": ""},
            {"OFFLINE": "yes"},
        ]:
            with self.subTest(change=change):
                self.configure_key()
                self.env.pop("OFFLINE", None)
                self.env.update(change)
                before = len(self.records()) if self.calls.exists() else 0
                code, output = self.call()
                self.assertEqual(code, 2, output)
                self.assertNotIn(KEY.strip(), output)
                self.assertFalse(
                    any(
                        "set" in r["args"] or "up" in r["args"]
                        for r in self.records()[before:]
                    )
                )

    def test_encryption_failure_stops_without_printing_key(self):
        self.configure_key()
        self.env["ENCRYPT_FAIL"] = "yes"
        code, output = self.call()
        self.assertEqual(code, 1, output)
        self.assertIn("Could not encrypt", output)
        self.assertNotIn(KEY.strip(), output)
        self.assertFalse(any("up" in r["args"] for r in self.records()))

    def test_redacted_helm_values_use_the_component_public_url(self):
        preview = self.component_preview()
        preview["steps"][0]["newState"]["outputs"] = {
            "url": "https://mail.example.test"
        }
        preview["steps"][3]["newState"]["inputs"]["values"] = secret("[secret]")
        self.preview.write_text(json.dumps(preview))
        code, output = self.call()
        self.assertEqual(code, 0, output)
        self.assertIn("as mail.example.test", output)
        self.assertNotIn(KEY.strip(), output)

    def test_known_release_hostname_is_checked_even_with_a_component_url(self):
        preview = self.component_preview()
        preview["steps"][0]["newState"]["outputs"] = {
            "url": "https://mail.example.test"
        }
        preview["steps"][3]["newState"]["inputs"]["values"] = secret(
            {"mailserver": {"hostname": "wrong.test"}}
        )
        self.preview.write_text(json.dumps(preview))
        code, output = self.call()
        self.assertEqual(code, 2, output)
        self.assertIn("wrong.test", output)
        self.assertFalse(any("up" in r["args"] for r in self.records()))

    def test_wrong_hostname_is_refused_after_secret_envelope_unwrap(self):
        preview = self.component_preview()
        preview["steps"][3]["newState"]["inputs"]["values"] = secret(
            {"mailserver": {"hostname": "wrong.test"}}
        )
        self.preview.write_text(json.dumps(preview))
        code, output = self.call()
        self.assertEqual(code, 2, output)
        self.assertIn("wrong.test", output)
        self.assertNotIn(KEY.strip(), output)
        self.assertFalse(any("up" in r["args"] for r in self.records()))

    def test_legacy_release_still_targets_only_itself(self):
        preview = self.component_preview()
        release = preview["steps"][3]
        release["newState"]["parent"] = "urn:stack"
        self.preview.write_text(json.dumps({"steps": [release]}))
        code, output = self.call(terminal=True)
        self.assertEqual(code, 1, output)
        args = next(r["args"] for r in self.records() if "up" in r["args"])
        self.assertEqual(
            args[args.index("--target") + 1 :], [RELEASE, "--stack", "prod"]
        )

    def test_chore_binds_apns_flags_to_the_installer(self):
        chore = os.environ.get("CHORE_BIN")
        if not chore:
            self.skipTest(
                "CHORE_BIN is not set; script behavior is tested independently"
            )
        env = dict(self.env)
        for name in ["APNS_KEY_FILE", "APNS_KEY_ID", "APNS_TEAM_ID", "APNS_TOPIC"]:
            env.pop(name)
        result = subprocess.run(
            [
                chore,
                "-y",
                "-f",
                str(ROOT / "tasks/install.yml"),
                "pulumi",
                str(self.project),
                "mail.example.test",
                "--version",
                "0.40.0",
                "--stack",
                "prod",
                "--apns-key-file",
                self.key.name,
                "--apns-key-id",
                "CLIFLAGKEY",
                "--apns-team-id",
                "CLIFLAGTEAM",
                "--apns-topic",
                "custom.cli.bundle",
            ],
            check=False,
            cwd=self.path,
            env=env,
            input="",
            text=True,
            capture_output=True,
            timeout=20,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn(KEY.strip(), result.stdout + result.stderr)
        config = json.loads(self.config.read_text())
        self.assertTrue(config["restmail:apnsPrivateKey"]["secret"])
        self.assertEqual(config["restmail:apnsKeyId"]["value"], "CLIFLAGKEY")
        self.assertEqual(config["restmail:apnsTopic"]["value"], "custom.cli.bundle")

    def test_other_runtimes_refuse_component_apns_before_configuration(self):
        (self.project / "Pulumi.yaml").write_text("name: infra\nruntime: python\n")
        self.configure_key()
        code, output = self.call()
        self.assertEqual(code, 2, output)
        self.assertIn("runtime: nodejs", output)
        self.assertFalse(any("set" in r["args"] for r in self.records()))


if __name__ == "__main__":
    unittest.main()
