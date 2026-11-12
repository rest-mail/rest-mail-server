"""The cluster-e2e failure issue (#1750): which run opens, comments, closes or
does nothing, what a failure report says, and the gh calls each one makes,
with gh stood in for. The workflow wiring is checked from cluster-e2e.yml.
"""

import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/cluster_e2e_issue.py"
WORKFLOW = ROOT / ".github/workflows/cluster-e2e.yml"

spec = importlib.util.spec_from_file_location("cluster_e2e_issue", SCRIPT)
issue = importlib.util.module_from_spec(spec)
spec.loader.exec_module(issue)

PASS = "cluster-e2e receipt commit=abc images=api:ci,smtp-gateway:ci step=health result=pass"
FAIL = "cluster-e2e receipt commit=abc images=api:ci,smtp-gateway:ci step=smtp-rcpt result=fail detail=RCPT_550"
SKIP = "cluster-e2e receipt commit=abc images=api:ci,smtp-gateway:ci step=imap-sign-in result=skipped"


class DecideTests(unittest.TestCase):
    TRACKED = [
        ("schedule", "refs/heads/main"),
        ("push", "refs/tags/v1.4.12"),
        ("workflow_dispatch", "refs/heads/main"),
    ]
    UNTRACKED = [
        ("workflow_dispatch", "refs/heads/test/1750-cluster-stage2"),
        ("workflow_dispatch", "refs/tags/v1.4.12"),
        ("push", "refs/heads/main"),
        ("push", "refs/tags/nightly"),
        ("pull_request", "refs/pull/1/merge"),
    ]

    def test_failure_opens_without_an_open_issue(self):
        for event, ref in self.TRACKED:
            self.assertEqual(issue.decide(event, ref, "failure", None), "open", (event, ref))

    def test_failure_comments_on_the_open_issue(self):
        for event, ref in self.TRACKED:
            self.assertEqual(issue.decide(event, ref, "failure", 42), "comment", (event, ref))

    def test_success_closes_the_open_issue(self):
        for event, ref in self.TRACKED:
            self.assertEqual(issue.decide(event, ref, "success", 42), "close", (event, ref))

    def test_success_without_an_issue_does_nothing(self):
        for event, ref in self.TRACKED:
            self.assertEqual(issue.decide(event, ref, "success", None), "none", (event, ref))

    def test_cancelled_or_skipped_says_nothing(self):
        for result in ("cancelled", "skipped", ""):
            for open_issue in (None, 42):
                self.assertEqual(issue.decide("schedule", "refs/heads/main", result, open_issue), "none")

    def test_dispatch_off_main_and_other_events_never_touch_it(self):
        for event, ref in self.UNTRACKED:
            for result in ("failure", "success"):
                for open_issue in (None, 42):
                    self.assertEqual(issue.decide(event, ref, result, open_issue), "none", (event, ref, result))


class ReportTests(unittest.TestCase):
    def report(self, receipts, steps=("Stages 1 and 2 against the install",)):
        return issue.failure_report("https://x/run/7", "schedule", "refs/heads/main", "abc123",
                                    "ghcr.io/rest-mail/*:ci", receipts, list(steps))

    def test_names_run_commit_images_steps_and_only_the_failing_receipt(self):
        text = self.report([PASS, FAIL, SKIP])
        self.assertIn("[This run](https://x/run/7) failed (schedule, `refs/heads/main`)", text)
        self.assertIn("- commit: `abc123`", text)
        self.assertIn("- images: `api:ci,smtp-gateway:ci`", text)
        self.assertIn("`Stages 1 and 2 against the install`", text)
        self.assertIn("```\n" + FAIL + "\n```", text)
        self.assertNotIn("step=health", text)
        self.assertNotIn("step=imap-sign-in", text)

    def test_without_receipts_says_the_stages_never_started(self):
        text = self.report([], steps=["Wait for every pod to be ready"])
        self.assertIn("built as `ghcr.io/rest-mail/*:ci`", text)
        self.assertIn("failed before the stages started", text)
        self.assertIn("`Wait for every pod to be ready`", text)

    def test_all_passed_receipts_say_the_failure_came_after(self):
        self.assertIn("came after the stages", self.report([PASS], steps=[]))
        self.assertIn("failed job steps: not read", self.report([PASS], steps=[]))

    def test_unknown_images_fall_back_to_the_build_tag(self):
        line = FAIL.replace("images=api:ci,smtp-gateway:ci", "images=unknown")
        self.assertIn("built as `ghcr.io/rest-mail/*:ci`", self.report([line]))

    def test_reads_only_receipt_lines(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "receipts.txt"
            path.write_text(f"noise\n{PASS}\n{FAIL}\n")
            self.assertEqual(issue.read_receipts(path), [PASS, FAIL])
            self.assertEqual(issue.read_receipts(Path(tmp) / "missing.txt"), [])


class FakeGh:
    """Answers gh by its first words and records every call."""

    def __init__(self, issues=(), labels=(), failed_steps=""):
        self.issues = list(issues)
        self.labels = list(labels)
        self.failed_steps = failed_steps
        self.calls = []

    def __call__(self, *args, input_text=None):
        self.calls.append((args, input_text))
        if args[:2] == ("issue", "list"):
            return json.dumps(self.issues)
        if args[:2] == ("label", "list"):
            return json.dumps([{"name": name} for name in self.labels])
        if args[0] == "api":
            return self.failed_steps
        return ""

    def writes(self):
        return [(args[:2], args, body) for args, body in self.calls
                if args[:2] in {("issue", "create"), ("issue", "comment"), ("issue", "close"), ("label", "create")}]


class MainTests(unittest.TestCase):
    def run_main(self, fake, event, ref, result, receipts=(FAIL,)):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "receipts.txt"
            path.write_text("\n".join(receipts) + "\n")
            argv = ["--result", result, "--receipts", str(path), "--event", event, "--ref", ref,
                    "--repo", "rest-mail/rest-mail-server", "--commit", "abc123", "--run-id", "7",
                    "--server-url", "https://github.com"]
            with patch.object(issue, "gh", fake), redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
                self.assertEqual(issue.main(argv), 0)

    def test_first_failure_makes_the_label_and_opens_the_issue(self):
        fake = FakeGh(failed_steps="Stages 1 and 2 against the install\n")
        self.run_main(fake, "schedule", "refs/heads/main", "failure")
        writes = fake.writes()
        self.assertEqual([w[0] for w in writes], [("label", "create"), ("issue", "create")])
        args, body = writes[1][1], writes[1][2]
        self.assertIn("cluster-e2e is failing", args)
        self.assertEqual(args[args.index("--label") + 1], "cluster-e2e")
        self.assertIn("https://github.com/rest-mail/rest-mail-server/actions/runs/7", body)
        self.assertIn(FAIL, body)

    def test_existing_label_is_not_made_again(self):
        fake = FakeGh(labels=["cluster-e2e"])
        self.run_main(fake, "push", "refs/tags/v1.4.12", "failure")
        self.assertEqual([w[0] for w in fake.writes()], [("issue", "create")])

    def test_later_failure_comments_on_the_oldest_matching_open_issue(self):
        fake = FakeGh(issues=[{"number": 90, "title": "cluster-e2e is failing"},
                              {"number": 80, "title": "cluster-e2e is failing"},
                              {"number": 70, "title": "something else"}])
        self.run_main(fake, "schedule", "refs/heads/main", "failure")
        writes = fake.writes()
        self.assertEqual([w[0] for w in writes], [("issue", "comment")])
        self.assertEqual(writes[0][1][2], "80")
        self.assertIn(FAIL, writes[0][2])

    def test_pass_closes_it_with_the_run_link(self):
        fake = FakeGh(issues=[{"number": 80, "title": "cluster-e2e is failing"}])
        self.run_main(fake, "push", "refs/tags/v1.4.12", "success", receipts=(PASS,))
        writes = fake.writes()
        self.assertEqual([w[0] for w in writes], [("issue", "close")])
        args = writes[0][1]
        self.assertEqual(args[2], "80")
        self.assertIn("https://github.com/rest-mail/rest-mail-server/actions/runs/7", args[args.index("--comment") + 1])

    def test_branch_dispatch_calls_no_gh_at_all(self):
        fake = FakeGh(issues=[{"number": 80, "title": "cluster-e2e is failing"}])
        for result in ("failure", "success"):
            self.run_main(fake, "workflow_dispatch", "refs/heads/test/1750-cluster-stage2", result)
        self.assertEqual(fake.calls, [])

    def test_cancelled_install_writes_nothing(self):
        fake = FakeGh(issues=[{"number": 80, "title": "cluster-e2e is failing"}])
        self.run_main(fake, "schedule", "refs/heads/main", "cancelled")
        self.assertEqual(fake.writes(), [])


class WorkflowTests(unittest.TestCase):
    def setUp(self):
        self.workflow = yaml.safe_load(WORKFLOW.read_text())

    def test_repository_token_stays_read_only(self):
        self.assertEqual(self.workflow["permissions"], {"contents": "read"})
        self.assertNotIn("permissions", self.workflow["jobs"]["install"])

    def test_only_the_issue_job_writes_issues_and_it_runs_after_any_install_result(self):
        job = self.workflow["jobs"]["failure-issue"]
        self.assertEqual(job["permissions"], {"contents": "read", "issues": "write", "actions": "read"})
        self.assertEqual(job["needs"], "install")
        self.assertEqual(job["if"], "always()")
        run = job["steps"][-1]["run"]
        self.assertIn("scripts/cluster_e2e_issue.py", run)
        self.assertIn("needs.install.result", run)

    def test_failed_steps_are_read_from_the_install_job_by_its_name(self):
        self.assertEqual(self.workflow["jobs"]["install"]["name"], issue.INSTALL_JOB)


if __name__ == "__main__":
    unittest.main()
