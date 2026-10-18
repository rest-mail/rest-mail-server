"""Exercise the optional credential guard without calling GitHub or merging."""

import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]


class AutoMergeWorkflowTests(unittest.TestCase):
    def setUp(self):
        self.source = (ROOT / ".github/workflows/auto-merge.yml").read_text()
        self.jobs = {
            match[1]: match[2]
            for match in re.finditer(
                r"^  ([a-z-]+):\n(.*?)(?=^  [a-z-]+:|\Z)",
                self.source, re.MULTILINE | re.DOTALL)
        }

    def check_configuration(self, token):
        script = textwrap.dedent(self.jobs["configuration"].split("        run: |\n", 1)[1])
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            env = dict(os.environ, GH_TOKEN=token, GITHUB_OUTPUT=str(output))
            result = subprocess.run(["bash", "-eu", "-o", "pipefail"], input=script,
                                    text=True, capture_output=True, env=env, timeout=5)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            return output.read_text(), result.stdout + result.stderr

    def test_missing_token_disables_helpers_with_an_explicit_notice(self):
        output, log = self.check_configuration("")
        self.assertEqual(output, "available=false\n")
        self.assertIn("::notice::PIPELINE_RESTMAIL_TOKEN is not configured", log)

    def test_configured_token_enables_helpers_without_logging_it(self):
        token = "synthetic-token-do-not-print"
        output, log = self.check_configuration(token)
        self.assertEqual(output, "available=true\n")
        self.assertNotIn(token, output + log)

    def test_both_helpers_require_the_guard_and_keep_existing_trust_rules(self):
        for name in ("arm", "catch-up"):
            job = self.jobs[name]
            self.assertIn("    needs: configuration\n", job)
            self.assertIn("needs.configuration.outputs.available == 'true'", job)
            self.assertIn("GH_TOKEN: ${{ secrets.PIPELINE_RESTMAIL_TOKEN }}", job)
            self.assertNotIn("continue-on-error", job)
        arm = self.jobs["arm"]
        self.assertIn("github.event.pull_request.head.repo.full_name == github.repository", arm)
        self.assertIn("github.event.pull_request.base.ref == github.event.repository.default_branch", arm)
        self.assertIn("!github.event.pull_request.draft", arm)
        self.assertIn('gh pr merge --auto --squash --delete-branch "$PR"', arm)
        self.assertIn("permissions: {}", self.source)
        self.assertNotIn("${{ github.token }}", self.source)


if __name__ == "__main__":
    unittest.main()
