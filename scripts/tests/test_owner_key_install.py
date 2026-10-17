"""Owner selection reaches deployments, without accepting or retaining private keys."""
import json
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import pty
import select
import signal
import time
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPptUlcK4cqOpW64ToKQ/KgNAlKNOj+qvlsCcW8B1K3H owner's laptop"
SPEC = importlib.util.spec_from_file_location("create_owner_key", ROOT / "tasks/lifecycle/create-owner-key.py")
CREATOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CREATOR)


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

    def terminal(self, answers, script=None, *args):
        env = dict(self.env, OWNER_KEY="")
        if script:
            command = ["bash", str(ROOT / script), *args]
        else:
            command = ["bash", "-c", 'source "$1"; source "$2"; key=$(choose_owner_key); status=$?; '
                       'printf "\\nSELECTED_PUBLIC:%s\\n" "$key"; exit "$status"', "test",
                       str(ROOT / "tasks/lifecycle/lib.sh"), str(ROOT / "tasks/lifecycle/owner-key.sh")]
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(self.path)
            os.execvpe(command[0], command, env)
        output = b""
        deadline = time.monotonic() + 15
        pending = list(answers)
        position = 0
        try:
            while time.monotonic() < deadline:
                if pending and pending[0][0].encode() in output[position:]:
                    _, answer = pending.pop(0)
                    position = len(output)
                    os.write(fd, (answer + "\n").encode())
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
                self.fail("Terminal did not finish: " + output.decode(errors="replace"))
            _, status = os.waitpid(pid, 0)
            pid = None
            self.assertFalse(pending, output.decode(errors="replace"))
            return os.waitstatus_to_exitcode(status), output.decode(errors="replace")
        finally:
            os.close(fd)
            if pid is not None:
                os.killpg(pid, signal.SIGKILL)
                os.waitpid(pid, 0)

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

    def test_public_filename_can_use_home_shorthand(self):
        self.env["OWNER_KEY"] = "~/" + os.path.relpath(self.key, Path.home())
        r = self.read()
        self.assertEqual((r.returncode, r.stdout), (0, KEY), r.stderr)

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

    def test_terminal_selects_existing_public_filename_with_spaces(self):
        code, output = self.terminal([("Choose [1]:", "2"), ("Existing public-key filename", self.key.name)])
        self.assertEqual(code, 0, output)
        self.assertIn("SELECTED_PUBLIC:" + KEY, output)
        self.assertFalse((self.path / "restmail_owner_ed25519").exists())

    def test_terminal_can_keep_managed_keys_without_creating_files(self):
        code, output = self.terminal([("Choose [1]:", "3")])
        self.assertEqual(code, 0, output)
        self.assertIn("SELECTED_PUBLIC:\r\n", output)
        self.assertEqual(list(self.path.iterdir()), [self.key])

    def test_terminal_creates_encrypted_local_pair_and_helm_gets_only_public_key(self):
        self.tools()
        code, output = self.terminal([("Choose [1]:", "1"), ("Private-key filename", ""),
                                      ("Enter passphrase", "fixture-passphrase"),
                                      ("Enter same passphrase", "fixture-passphrase")],
                                     "tasks/lifecycle/install-helm.sh")
        self.assertEqual(code, 1, output)  # fixture stops after capturing Helm values
        private = self.path / "restmail_owner_ed25519"
        public = Path(str(private) + ".pub")
        self.assertEqual(private.stat().st_mode & 0o777, 0o600)
        proof = subprocess.run(["ssh-keygen", "-y", "-P", "fixture-passphrase", "-f", str(private)],
                               text=True, capture_output=True, check=True)
        self.assertEqual(proof.stdout.split()[:2], public.read_text().split()[:2])
        values = json.loads(Path(self.env["CAPTURE"]).read_text())
        self.assertEqual(values["api"]["ownerKeys"], public.read_text().strip())
        self.assertNotIn(private.read_text(), output)
        self.assertNotIn("fixture-passphrase", output)
        self.assertNotIn("PRIVATE KEY", Path(self.env["CAPTURE"]).read_text())
        self.assertIn("Back it up securely", output)
        self.assertFalse(list(self.path.glob(".restmail-owner-*")))

    def test_creation_refuses_existing_private_public_and_dangling_links(self):
        target = self.path / "new-owner"
        for suffix, symlink in [("", False), (".pub", False), ("", True), (".pub", True)]:
            with self.subTest(suffix=suffix, symlink=symlink):
                occupied = Path(str(target) + suffix)
                if symlink:
                    occupied.symlink_to(self.path / "missing")
                else:
                    occupied.write_text("keep this file")
                with mock.patch.object(CREATOR.subprocess, "run") as keygen:
                    with self.assertRaises(FileExistsError):
                        CREATOR.create_key(target)
                    keygen.assert_not_called()
                if not symlink:
                    self.assertEqual(occupied.read_text(), "keep this file")
                occupied.unlink()

    def test_publication_race_preserves_public_file_and_removes_new_private_link(self):
        target = self.path / "new-owner"
        public = Path(str(target) + ".pub")

        def generation(command, **kwargs):
            temporary = Path(command[-1])
            temporary.write_text("temporary fixture private key")
            Path(str(temporary) + ".pub").write_text(KEY)
            public.write_text("a file that arrived while generating")

        with mock.patch.object(CREATOR.subprocess, "run", side_effect=generation):
            with self.assertRaises(FileExistsError):
                CREATOR.create_key(target)
        self.assertFalse(target.exists())
        self.assertEqual(public.read_text(), "a file that arrived while generating")
        self.assertFalse(list(self.path.glob(".restmail-owner-*")))

    def test_keygen_failure_leaves_no_destination_or_temporary_key(self):
        target = self.path / "new-owner"
        with mock.patch.object(CREATOR.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "ssh-keygen")):
            with self.assertRaises(subprocess.CalledProcessError):
                CREATOR.create_key(target)
        self.assertEqual(list(self.path.iterdir()), [self.key])

    def test_already_installed_helm_refuses_before_offering_key_creation(self):
        self.tools()
        self.stub("helm", "#!/bin/sh\nexit 0\n")
        code, output = self.terminal([], "tasks/lifecycle/install-helm.sh")
        self.assertEqual(code, 2, output)
        self.assertNotIn("Owner recovery key:", output)
        self.assertFalse((self.path / "restmail_owner_ed25519").exists())

    def test_pulumi_keeps_existing_managed_key_without_prompt_or_rewrite(self):
        self.tools()
        self.stub("pulumi", '''#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
a=sys.argv[1:]
if "--show-name" in a: print("prod")
elif "export" in a: print('{"deployment":{"resources":[]}}')
elif "config" in a and "--json" in a: print(json.dumps({"restmail:ownerKeys":{"value":os.environ["FIXTURE_OWNER_KEY"]}}))
elif "get" in a: print(os.environ["FIXTURE_OWNER_KEY"])
elif "set" in a:
 with open(os.environ["CAPTURE"],"a") as f: f.write(a[a.index("set")+1]+"\\n")
else: sys.exit(1) # Stop at preview; never deploy.
''')
        for key in (KEY, ""):
            with self.subTest(managed_key=bool(key)):
                self.env["FIXTURE_OWNER_KEY"] = key
                answers = [] if key else [("Choose [1]:", "3")]
                code, output = self.terminal(answers, "tasks/lifecycle/pulumi.sh", "install")
                self.assertEqual(code, 1, output)
                self.assertEqual("Owner recovery key:" in output, not bool(key))
                self.assertNotIn("restmail:ownerKeys", Path(self.env["CAPTURE"]).read_text().splitlines())
                self.assertFalse((self.path / "restmail_owner_ed25519").exists())

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
