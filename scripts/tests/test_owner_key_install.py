"""Owner selection reaches deployments, without accepting or retaining private keys."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPptUlcK4cqOpW64ToKQ/KgNAlKNOj+qvlsCcW8B1K3H owner's laptop"


class OwnerKeyInstall(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)
        self.key = self.path / "owner key.pub"
        self.key.write_text(KEY + "\n")
        self.env = dict(os.environ, OWNER_KEY=str(self.key), USER_WORKING_DIR=str(self.path),
                        HOST="mail.example.test", DOMAIN="example.test", VERSION="0.20.0",
                        NAME="restmail", NAMESPACE="rest-mail", PROJECT=str(self.path), STACK="prod",
                        PASSWORD="fixture installation password", CAPTURE=str(self.path / "capture.json"))

    def call(self, script, *args, env=None):
        return subprocess.run(["bash", str(ROOT / script), *args], cwd=self.path,
                              env=env or self.env, text=True, capture_output=True, timeout=15)

    def read(self):
        return subprocess.run(["bash", "-c", 'source "$1"; source "$2"; read_owner_key', "test",
                               str(ROOT / "tasks/lifecycle/lib.sh"), str(ROOT / "tasks/lifecycle/owner-key.sh")],
                              cwd=self.path, env=self.env, text=True, capture_output=True, timeout=15)

    def stub(self, name, source):
        p = self.path / "bin" / name
        p.parent.mkdir(exist_ok=True)
        p.write_text(source)
        p.chmod(0o755)
        self.env["PATH"] = str(p.parent) + os.pathsep + os.environ["PATH"]

    def tools(self):
        self.stub("kubectl", "#!/bin/sh\nprintf '%s\\n' '{\"items\":[]}'\n")
        self.stub("helm", '''#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
args=sys.argv[1:]
if "status" in args: sys.exit(1)
if "install" in args:
 i=max(i for i,x in enumerate(args) if x=="-f")
 Path(os.environ["CAPTURE"]).write_text(Path(args[i+1]).read_text())
 sys.exit(17) # Stop immediately after capturing install values; no deployment.
''')
        self.stub("pulumi", "#!/bin/sh\nexit 1\n")
        self.stub("restmail", "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{BLF-CRYPT}fixture-hash'\n")

    def test_selects_only_public_key_and_prints_fingerprint(self):
        self.env["OWNER_KEY"] = self.key.name
        r = self.read()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout, KEY)
        self.assertIn("SHA256:", r.stderr)
        self.assertEqual(self.key.read_text(), KEY + "\n")

    def test_private_key_is_refused_before_any_registration(self):
        self.key.write_text("-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-fixture\n-----END OPENSSH PRIVATE KEY-----\n")
        r = self.read()
        self.assertEqual(r.returncode, 2)
        self.assertEqual(r.stdout, "")
        self.assertNotIn("private-fixture", r.stderr)

    def test_bad_base64_multiple_keys_and_options_are_refused(self):
        for contents in ["ssh-ed25519 not-base64!", KEY + "\n" + KEY,
                         'command="echo restricted" ' + KEY, "ssh-dss AAAA"]:
            with self.subTest(contents=contents[:20]):
                self.key.write_text(contents)
                r = self.read()
                self.assertEqual(r.returncode, 2, r.stderr)
                self.assertEqual(r.stdout, "")

    def test_no_selection_does_not_invent_or_discover_a_key(self):
        self.env["OWNER_KEY"] = ""
        r = self.read()
        self.assertEqual((r.returncode, r.stdout, r.stderr), (0, "", ""))

    def test_helm_receives_exact_public_key_in_values(self):
        self.tools()
        r = self.call("tasks/lifecycle/install-helm.sh")
        self.assertEqual(r.returncode, 1, r.stderr)  # fixture helm stopped the install
        values = json.loads(Path(self.env["CAPTURE"]).read_text())
        self.assertEqual(values["api"]["ownerKeys"], KEY)
        self.assertEqual(values["api"]["bootstrapAdminPassword"], self.env["PASSWORD"])
        self.assertNotIn("statusKeys", values["api"])

    def test_helm_without_selection_does_not_clear_owner_values_file(self):
        self.tools()
        self.env["OWNER_KEY"] = ""
        r = self.call("tasks/lifecycle/install-helm.sh")
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertNotIn("ownerKeys", json.loads(Path(self.env["CAPTURE"]).read_text())["api"])

    def test_offline_pulumi_prints_quoted_public_key_and_matching_program(self):
        self.tools()
        (self.path / "Pulumi.yaml").write_text("name: infra\nruntime: nodejs\n")
        r = self.call("tasks/lifecycle/pulumi.sh", "install")
        self.assertEqual(r.returncode, 0, r.stderr)
        line = next(x for x in r.stdout.splitlines() if x.startswith("  restmail:ownerKeys:"))
        self.assertEqual(json.loads(line.split(": ", 1)[1]), KEY)
        self.assertIn('ownerKeys: mail.get("ownerKeys") || ""', r.stdout)
        self.assertIn("restmail reset-admin", r.stdout)

    def test_programs_use_optional_owner_key_config(self):
        for runtime, expected in [("nodejs", 'mail.get("ownerKeys")'), ("python", 'mail.get("ownerKeys")'),
                                  ("go", 'mail.Get("ownerKeys")')]:
            env = dict(self.env, RUNTIME=runtime)
            r = self.call("tasks/lifecycle/pulumi-code.sh", env=env)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertIn(expected, r.stdout)
        for selected in ["", "1"]:
            r = self.call("tasks/lifecycle/pulumi-code.sh", env=dict(self.env, RUNTIME="yaml", OWNER_KEYS_CONFIGURED=selected))
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual("ownerKeys: ${restmail:ownerKeys}" in r.stdout, bool(selected))

    @unittest.skipUnless(shutil.which("chore"), "chore is not installed")
    def test_chore_binds_owner_key_flag(self):
        task = self.path / "chores.yml"
        task.write_text('version: "3"\ntasks:\n  key:\n    args: [owner_key]\n    cmds:\n      - \'printf "%s" "$OWNER_KEY"\'\n')
        env = dict(self.env)
        env.pop("OWNER_KEY")
        r = subprocess.run(["chore", "-f", str(task), "key", "--owner-key", self.key.name],
                           env=env, capture_output=True, text=True, timeout=15)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn(self.key.name, r.stdout)


if __name__ == "__main__":
    unittest.main()
