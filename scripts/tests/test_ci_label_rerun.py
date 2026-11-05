"""Exercise label-gate recovery without spending a GitHub runner (#1942)."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from test_stacked_pr_workflows import WORKFLOWS, blocks, shell_of


class ContractLabelRerun(unittest.TestCase):
    def run_helper(self, conclusion, job=""):
        workflow = (WORKFLOWS / "ci-labels.yml").read_text()
        script = shell_of(
            blocks(workflow.split("jobs:\n", 1)[1], 2)["rerun-contract-gate"],
            "Re-run the contract gate once the newest CI run is done",
        )
        with tempfile.TemporaryDirectory() as directory:
            executable = Path(directory) / "gh"
            executable.write_text(
                "#!/bin/bash\n"
                'case "$*" in\n'
                '  "pr view "*) echo head ;;\n'
                '  "run list "*) echo "123 completed" ;;\n'
                '  "run view "*"--json jobs "*) printf "%s\\n" "$TEST_JOB" ;;\n'
                '  "run view "*"--json conclusion "*) echo "$TEST_CONCLUSION" ;;\n'
                '  "run rerun "*) echo "$*" > "$TEST_RERUN" ;;\n'
                '  *) echo "unexpected gh command: $*" >&2; exit 90 ;;\n'
                "esac\n"
            )
            executable.chmod(0o755)
            rerun = Path(directory) / "rerun"
            result = subprocess.run(
                ["bash", "-c", script], capture_output=True, text=True, timeout=600,
                env=dict(
                    os.environ, PATH=directory + os.pathsep + os.environ["PATH"],
                    PR="1907", GITHUB_REPOSITORY="rest-mail/rest-mail-server",
                    TEST_CONCLUSION=conclusion, TEST_JOB=job, TEST_RERUN=str(rerun),
                ),
            )
            return result, rerun.read_text() if rerun.exists() else ""

    def test_cancelled_run_without_jobs_needs_a_new_ci_run_not_a_job_rerun(self):
        result, rerun = self.run_helper("cancelled")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("cancelled before", result.stdout)
        self.assertEqual(rerun, "")

    def test_missing_gate_in_a_finished_run_remains_an_error(self):
        for conclusion in ("success", "failure", "timed_out", ""):
            with self.subTest(conclusion=conclusion):
                result, rerun = self.run_helper(conclusion)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("has no 'API contract changes' job", result.stdout)
                self.assertEqual(rerun, "")

    def test_existing_gate_is_rerun_even_if_the_run_was_cancelled(self):
        for conclusion in ("success", "failure", "cancelled"):
            with self.subTest(conclusion=conclusion):
                result, rerun = self.run_helper(conclusion, job="456")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("--job 456", rerun)


if __name__ == "__main__":
    unittest.main()
