"""The scripts that let CI skip work that cannot change its result (#1896)."""

import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
PATHS_CHANGED = ROOT / "scripts/ci/paths-changed"
DEV_GOCACHE = ROOT / "tasks/dev-gocache.sh"


def git(directory, *args):
    subprocess.run(
        ["git", "-c", "user.name=t", "-c", "user.email=t@example.test", *args],
        cwd=directory, check=True, capture_output=True, text=True, timeout=30,
    )


class PathsChanged(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.origin = self.root / "origin"
        self.origin.mkdir()
        git(self.origin, "init", "--quiet", "--initial-branch=main")
        (self.origin / "module").mkdir()
        (self.origin / "module/go.mod").write_text("module a\n")
        (self.origin / "other").write_text("one\n")
        git(self.origin, "add", ".")
        git(self.origin, "commit", "--quiet", "-m", "base")
        self.base = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=self.origin, check=True, capture_output=True, text=True, timeout=30,
        ).stdout.strip()
        git(self.origin, "branch", "base-tip")

    def checkout(self):
        # As the workflow's checkout leaves it: one commit, without the base.
        checkout = self.root / "checkout"
        git(self.root, "clone", "--quiet", "--depth=1", self.origin.as_uri(), str(checkout))
        return checkout

    def commit(self, path, text):
        target = self.origin / path
        target.write_text(text)
        git(self.origin, "add", ".")
        git(self.origin, "commit", "--quiet", "-m", "change")

    def answer(self, base, *paths):
        output = self.root / "output"
        output.unlink(missing_ok=True)
        result = subprocess.run(
            [str(PATHS_CHANGED), base, *(paths or ("module",))], cwd=self.checkout_dir,
            env=dict(os.environ, GITHUB_OUTPUT=str(output)), capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return output.read_text().strip()

    def test_another_path_changing_skips(self):
        self.commit("other", "two\n")
        self.checkout_dir = self.checkout()
        self.assertEqual(self.answer(self.base), "changed=false")

    def test_the_path_changing_runs(self):
        self.commit("module/go.mod", "module b\n")
        self.checkout_dir = self.checkout()
        self.assertEqual(self.answer(self.base), "changed=true")

    def test_any_named_path_changing_runs(self):
        self.commit("other", "two\n")
        self.checkout_dir = self.checkout()
        self.assertEqual(self.answer(self.base, "module", "other"), "changed=true")

    def test_no_base_runs(self):
        self.commit("other", "two\n")
        self.checkout_dir = self.checkout()
        self.assertEqual(self.answer(""), "changed=true")

    def test_a_base_that_cannot_be_fetched_runs(self):
        self.commit("other", "two\n")
        self.checkout_dir = self.checkout()
        self.assertEqual(self.answer("0" * 40), "changed=true")


class DevGoCache(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.calls = self.directory / "calls"

    def run_script(self, *args, volume_exists=False, volume="fixture"):
        docker = self.directory / "docker"
        docker.write_text(textwrap.dedent(f"""\
            #!/bin/sh
            echo "$*" >> "{self.calls}"
            case "$1 $2" in
              "volume inspect") exit {0 if volume_exists else 1} ;;
            esac
            echo noise
        """))
        docker.chmod(0o755)
        environment = dict(os.environ, PATH=f"{self.directory}{os.pathsep}{os.environ['PATH']}")
        if volume:
            environment["RESTMAIL_DEV_GOCACHE_VOLUME"] = volume
        return subprocess.run(
            [str(DEV_GOCACHE), *args], env=environment, capture_output=True, text=True, timeout=10,
        )

    def test_a_new_volume_is_handed_to_its_user_and_only_the_flag_is_printed(self):
        result = self.run_script("10001")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "-v fixture-10001:/tmp/go-build-cache\n")
        calls = self.calls.read_text().splitlines()
        self.assertIn("volume create --label rest-mail.run=local fixture-10001", calls)
        self.assertIn("run --rm --label rest-mail.run=local -v fixture-10001:/cache alpine:3.20 chown 10001:10001 /cache", calls)

    def test_a_volume_in_use_is_left_alone(self):
        result = self.run_script("0", volume_exists=True)
        self.assertEqual(result.stdout, "-v fixture-0:/tmp/go-build-cache\n")
        self.assertEqual(self.calls.read_text().splitlines(), ["volume inspect fixture-0"])

    def test_it_needs_a_user_and_a_volume(self):
        self.assertEqual(self.run_script().returncode, 2)
        self.assertNotEqual(self.run_script("0", volume="").returncode, 0)


class Workflow(unittest.TestCase):
    def test_the_imap_step_runs_unless_told_it_did_not_change(self):
        # A missing output must run the tests, not skip them.
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        step = workflow.split("- name: Test the local IMAP compatibility module\n", 1)[1]
        self.assertIn("if: steps.imap-module.outputs.changed != 'false'\n", step.split("run:", 1)[0])


if __name__ == "__main__":
    unittest.main()
