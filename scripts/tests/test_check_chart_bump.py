"""A chart change without a new version fails here, not on main after it merges."""

import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("check_chart_bump", ROOT / "scripts/check_chart_bump.py")
bump = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bump)
CHART = bump.CHART
WORKFLOW = bump.WORKFLOW


class ChartBumpInARepository(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        patcher = patch.object(bump, "ROOT", self.root)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "test")
        self.write(WORKFLOW, (ROOT / WORKFLOW).read_text())
        self.write(CHART, "name: restmail\nversion: 0.42.1\nappVersion: 0.42.1\n")
        self.write("helm/restmail/values.yaml", "a: 1\n")
        self.write("helm/restmail/tests/render-test.sh", "#!/bin/sh\n")
        self.write("internal/a.go", "package a\n")
        self.commit()
        self.base = self.git("rev-parse", "HEAD").strip()

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.root, check=True, capture_output=True,
                              text=True, timeout=600).stdout

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def commit(self):
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "change")

    def run_check(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            code = bump.main(["--base", self.base])
        return code, output.getvalue()

    def test_a_packaged_chart_file_without_a_new_version_is_refused(self):
        self.write("helm/restmail/values.yaml", "a: 2\n")
        self.commit()
        code, output = self.run_check()
        self.assertEqual(code, 1, output)
        self.assertIn("helm/restmail/values.yaml starts chart publishing", output)
        self.assertIn("bump version in helm/restmail/Chart.yaml", output)

    def test_the_publishing_workflow_without_a_new_version_is_refused(self):
        self.write(WORKFLOW, (ROOT / WORKFLOW).read_text() + "# a comment\n")
        self.commit()
        code, output = self.run_check()
        self.assertEqual(code, 1, output)
        self.assertIn(WORKFLOW, output)

    def test_a_new_version_passes(self):
        self.write("helm/restmail/values.yaml", "a: 2\n")
        self.write(CHART, "name: restmail\nversion: 0.42.2\nappVersion: 0.42.1\n")
        self.commit()
        code, output = self.run_check()
        self.assertEqual(code, 0, output)
        self.assertIn("as 0.42.2 (from 0.42.1)", output)

    def test_a_new_app_version_alone_does_not_count(self):
        self.write(CHART, "name: restmail\nversion: 0.42.1\nappVersion: 0.43.0\n")
        self.commit()
        code, output = self.run_check()
        self.assertEqual(code, 1, output)

    def test_chart_tests_are_not_packaged_so_they_start_nothing(self):
        self.write("helm/restmail/tests/render-test.sh", "#!/bin/sh\nexit 0\n")
        self.commit()
        code, output = self.run_check()
        self.assertEqual(code, 0, output)
        self.assertIn("no change here starts chart publishing", output)

    def test_a_change_elsewhere_starts_nothing(self):
        self.write("internal/a.go", "package a\n\nvar B = 1\n")
        self.commit()
        code, output = self.run_check()
        self.assertEqual(code, 0, output)

    def test_main_moving_on_does_not_count_against_a_branch(self):
        self.git("checkout", "-q", "-b", "work")
        self.write("internal/a.go", "package a\n\nvar B = 1\n")
        self.commit()
        self.git("checkout", "-q", "main")
        self.write("helm/restmail/values.yaml", "a: 3\n")
        self.write(CHART, "name: restmail\nversion: 0.42.2\nappVersion: 0.42.1\n")
        self.commit()
        self.base = self.git("rev-parse", "main").strip()
        self.git("checkout", "-q", "work")
        code, output = self.run_check()
        self.assertEqual(code, 0, output)

    def test_a_base_that_does_not_exist_fails_closed(self):
        self.base = "no-such-ref"
        code, output = self.run_check()
        self.assertEqual(code, 1, output)


class ThisTree(unittest.TestCase):
    def test_the_workflow_ignores_what_the_package_ignores(self):
        patterns = bump.trigger_paths((ROOT / WORKFLOW).read_text())
        self.assertIn("!helm/restmail/tests/**", patterns)
        self.assertIn("tests/", (ROOT / "helm/restmail/.helmignore").read_text().splitlines())

    def test_this_tree_bumps_the_chart_if_it_changes_it(self):
        # So the local gate fails where CI's Helm Chart job does. CI's own checkout
        # has no origin/main, and its job runs the script with the commit before.
        try:
            bump.git("rev-parse", "--verify", "-q", "origin/main")
        except subprocess.CalledProcessError:
            self.skipTest("no origin/main here; the Helm Chart job makes this check in CI")
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            code = bump.main([])
        self.assertEqual(code, 0, output.getvalue())


class CiRunsIt(unittest.TestCase):
    def test_the_helm_chart_job_compares_with_the_commit_before_this_one(self):
        ci = (ROOT / ".github/workflows/ci.yml").read_text()
        job = ci[ci.index("\n  helm:\n"):ci.index("\n  webmail:\n")]
        self.assertIn("fetch-depth: 2", job)
        self.assertIn("python3 scripts/check_chart_bump.py --base HEAD^1", job)


if __name__ == "__main__":
    unittest.main()
