"""Exercise PR ancestry preflight with real Git history and the workflow verdicts."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from test_stacked_pr_workflows import WORKFLOWS, blocks, context, evaluate, field, shell_of


STEP = "Require the current base tip in the pull request head"
HEAVY_WORKFLOWS = {
    "ci.yml", "e2e.yml", "email-explained.yml", "pulumi.yml", "release-cli.yml",
}


def run_shell(script, directory, **environment):
    return subprocess.run(
        ["bash", "-eu", "-o", "pipefail", "-c", script], cwd=directory,
        env=dict(os.environ, **environment), capture_output=True, text=True, timeout=600,
    )


class PreflightWorkflows(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.jobs = {
            name: blocks(blocks((WORKFLOWS / name).read_text(), 0)["jobs"], 2)
            for name in HEAVY_WORKFLOWS
        }
        cls.script = shell_of(cls.jobs["ci.yml"]["preflight"], STEP)

    def test_every_heavy_pr_workflow_starts_with_the_same_preflight(self):
        workflows = {
            path.name for path in WORKFLOWS.glob("*.yml")
            if "pull_request" in blocks(blocks(path.read_text(), 0).get("on", ""), 2)
        }
        self.assertEqual(workflows - {"ci-labels.yml"}, HEAVY_WORKFLOWS)
        for name, jobs in self.jobs.items():
            with self.subTest(workflow=name):
                self.assertEqual(next(iter(jobs)), "preflight")
                preflight = jobs["preflight"]
                self.assertEqual(field(preflight, "name", 4), "'Preflight: branch is up to date'")
                self.assertIn("ref: ${{ github.event.pull_request.head.sha }}", preflight)
                self.assertIn("fetch-depth: 0", preflight)
                self.assertIn("if: github.event_name == 'pull_request'", preflight)
                self.assertIn("BASE_REF: ${{ github.event.pull_request.base.ref }}", preflight)
                self.assertNotIn("pull_request.base.sha", preflight)
                self.assertEqual(shell_of(preflight, STEP), self.script)
                self.assertNotIn("needs:", preflight)

    def test_every_other_job_needs_preflight_and_heavy_jobs_cannot_run_after_failure(self):
        for name, jobs in self.jobs.items():
            for job_name, job in jobs.items():
                if job_name == "preflight":
                    continue
                with self.subTest(workflow=name, job=job_name):
                    needs = field(job, "needs", 4).strip("[]").split(", ")
                    self.assertIn("preflight", needs)
                    condition = field(job, "if", 4)
                    if "always()" in condition:
                        values = context()
                        values["needs"]["preflight"]["result"] = "failure"
                        self.assertEqual(bool(evaluate(condition, values)), job_name in {"ci", "e2e"})

    def test_failure_message_is_actionable_and_identifies_both_commits(self):
        for fragment in (
            "::error::$message", "head $HEAD_SHA", "$BASE_REF ($base_sha)",
            "You cannot push a branch that is not up to date with origin/$BASE_REF ($base_sha)",
            "git fetch origin $BASE_REF && git rebase origin/$BASE_REF",
            "Nothing else in this pipeline runs until you do.",
        ):
            with self.subTest(fragment=fragment):
                self.assertIn(fragment, self.script)

    def test_non_pr_events_pass_without_a_checkout_or_remote(self):
        with tempfile.TemporaryDirectory() as directory:
            for event in ("push", "merge_group", "workflow_dispatch"):
                with self.subTest(event=event):
                    result = run_shell(self.script, directory, EVENT_NAME=event)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("needs no pull request ancestry check", result.stdout)

    def test_stale_head_fails_then_rebased_detached_head_passes_against_live_base_tip(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            remote = root / "origin.git"
            checkout = root / "checkout"
            subprocess.run(["git", "init", "--bare", str(remote)], check=True, capture_output=True)
            subprocess.run(["git", "init", "-b", "main", str(checkout)], check=True, capture_output=True)

            def git(*arguments):
                return subprocess.run(
                    ["git", *arguments], cwd=checkout, capture_output=True,
                    text=True, check=True, timeout=600,
                ).stdout.strip()

            git("config", "user.name", "Preflight Test")
            git("config", "user.email", "preflight@example.invalid")
            git("config", "commit.gpgsign", "false")
            git("remote", "add", "origin", str(remote))
            (checkout / "initial").write_text("initial\n")
            git("add", ".")
            git("commit", "-m", "Initial base")
            git("push", "origin", "main")
            git("checkout", "-b", "topic")
            (checkout / "feature").write_text("feature\n")
            git("add", ".")
            git("commit", "-m", "Feature change")
            stale_head = git("rev-parse", "HEAD")
            for base in ("main", "feature/parent"):
                with self.subTest(base=base):
                    git("checkout", "main")
                    git("checkout", "-B", base)
                    (checkout / "base").write_text(base + "\n")
                    git("add", ".")
                    git("commit", "-m", "Advance base")
                    git("push", "origin", base)
                    base_sha = git("rev-parse", "HEAD")
                    git("checkout", "--detach", stale_head)
                    git("update-ref", "refs/remotes/origin/" + base, git("rev-parse", "HEAD^"))
                    result = run_shell(
                        self.script, checkout, EVENT_NAME="pull_request", BASE_REF=base, HEAD_SHA=stale_head,
                    )
                    self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                    self.assertIn("::error::Preflight:", result.stderr)
                    self.assertIn(stale_head, result.stderr)
                    self.assertIn(base_sha, result.stderr)
                    self.assertIn(f"git fetch origin {base} && git rebase origin/{base}", result.stderr)
                    self.assertEqual(git("rev-parse", "refs/remotes/origin/" + base), base_sha)
                    git("rebase", "origin/" + base)
                    rebased_head = git("rev-parse", "HEAD")
                    result = run_shell(
                        self.script, checkout, EVENT_NAME="pull_request", BASE_REF=base, HEAD_SHA=rebased_head,
                    )
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertIn(base_sha, result.stdout)

    def test_required_verdicts_fail_when_preflight_fails_even_if_everything_else_skips(self):
        with tempfile.TemporaryDirectory() as directory:
            ci_script = shell_of(self.jobs["ci.yml"]["ci"], "Verdict")
            ci_needs = field(self.jobs["ci.yml"]["ci"], "needs", 4).strip("[]").split(", ")
            for status in ("failure", "cancelled"):
                with self.subTest(gate="CI", status=status):
                    results = "\n".join(
                        f"{name}={status if name == 'preflight' else 'skipped'}" for name in ci_needs
                    )
                    result = run_shell(ci_script, directory, RESULTS=results)
                    self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                    self.assertIn("::error::", result.stderr)
            e2e_script = shell_of(self.jobs["e2e.yml"]["e2e"], "Verify e2e outcome")
            for status in ("failure", "cancelled", "skipped"):
                for changes in ("success", "skipped"):
                    with self.subTest(gate="E2E", status=status, changes=changes):
                        result = run_shell(
                            e2e_script, directory, PREFLIGHT_RESULT=status, CHANGES_RESULT=changes,
                            RUN_E2E="false", E2E_RESULT="skipped", PAIRS_RESULT="skipped",
                            PROOFS_RESULT="skipped", EVENT_NAME="pull_request",
                        )
                        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                        self.assertIn("::error::E2E gate failing", result.stdout)

    def test_local_guard_explicitly_states_the_default(self):
        result = subprocess.run(
            ["git", "config", "-f", str(WORKFLOWS.parents[1] / ".github-guard"),
             "--bool", "--get", "guards.push-up-to-date"],
            capture_output=True, text=True, check=True, timeout=600,
        )
        self.assertEqual(result.stdout.strip(), "true")


if __name__ == "__main__":
    unittest.main()
