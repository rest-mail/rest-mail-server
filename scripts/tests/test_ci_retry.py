"""Pin every rule of the CI retry bot's decision (scripts/ci_retry.py, #2113)."""

import contextlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import ci_retry  # noqa: E402
import yaml  # noqa: E402

POLICY_FILE = ROOT / ".github" / "ci-retry.yml"
WORKFLOW_FILE = ROOT / ".github" / "workflows" / "ci-retry.yml"
POLICY = ci_retry.load_policy(POLICY_FILE)

ARC = "Error: the job was cancelled after its runner reservation ran out"
EGRESS = "Failed to upload artifact: 503 Egress is over the account limit"
DNS = "dial tcp: lookup proxy.golang.org on 127.0.0.53:53: server misbehaving"
RESET = "read tcp 10.0.0.4:5432: connection reset by peer"
SELF_HOSTED_JOB = {"id": 501, "name": "Test", "runner_name": "antimatter-vps-linux-x64-runner-4zjsp",
                   "labels": ["antimatter-vps-linux-x64"]}
HOSTED_JOB = {"id": 501, "name": "Test", "runner_name": "GitHub Actions 1000000123",
              "labels": ["ubuntu-24.04"]}
UNREPORTED_JOB = {"id": 501, "name": "Test", "runner_name": None, "labels": []}


def decide(failure_text=ARC, mode="infra", attempt=1, max_attempts=5, ready_runners=2,
           already_retried_head=False, failed_on_self_hosted=True):
    return ci_retry.decide(POLICY, mode, attempt, max_attempts, failure_text, ready_runners,
                           already_retried_head=already_retried_head,
                           failed_on_self_hosted=failed_on_self_hosted)


class PolicyFileTests(unittest.TestCase):
    def test_the_repository_copy_parses(self):
        self.assertEqual(POLICY.retryable, (
            "after its runner reservation ran out",
            "Egress is over the account limit",
            "dial tcp: lookup",
            "connection reset by peer",
        ))
        self.assertEqual(POLICY.never, ("--- FAIL:", "FAIL:"))

    def test_a_policy_without_never_is_refused(self):
        with self.assertRaisesRegex(ValueError, "never"):
            ci_retry.parse_policy({"retryable": ["x"]})

    def test_an_empty_pattern_is_refused(self):
        with self.assertRaisesRegex(ValueError, "retryable"):
            ci_retry.parse_policy({"retryable": [""], "never": []})

    def test_an_unknown_key_is_refused(self):
        with self.assertRaisesRegex(ValueError, "retry"):
            ci_retry.parse_policy({"retryable": [], "never": [], "retry": []})

    def test_the_workflow_parses_and_reacts_to_ci_and_e2e(self):
        workflow = yaml.safe_load(WORKFLOW_FILE.read_text())
        # PyYAML reads the bare key `on` as True.
        trigger = workflow[True]["workflow_run"]
        self.assertEqual(trigger["workflows"], ["CI", "E2E"])
        self.assertEqual(trigger["types"], ["completed"])
        self.assertEqual(workflow["permissions"], {
            "actions": "write", "pull-requests": "write", "contents": "read"})
        job = workflow["jobs"]["retry"]
        self.assertIn("github.event.workflow_run.conclusion == 'failure'", job["if"])
        text = WORKFLOW_FILE.read_text()
        self.assertIn("vars.CI_RETRY || 'infra'", text)
        self.assertIn("vars.CI_RETRY_MAX || '5'", text)
        self.assertIn("vars.RUNNER_GATE_URL", text)
        self.assertIn("rerun-failed-jobs", text)
        self.assertIn("gh label create ci:needs-human --force", text)
        # The bot never cancels anything.
        self.assertNotIn("/cancel", text)
        self.assertNotIn("gh run cancel", text)


class RuleTests(unittest.TestCase):
    def test_off_refuses_even_a_retryable_failure(self):
        decision = decide(mode="off")
        self.assertFalse(decision.retry)
        self.assertEqual(decision.reason, "retries are off")
        self.assertFalse(decision.needs_human)

    def test_an_unknown_mode_refuses(self):
        decision = decide(mode="sometimes")
        self.assertFalse(decision.retry)
        self.assertEqual(decision.rule, "mode")

    def test_an_already_retried_head_is_not_retried_again(self):
        decision = decide(already_retried_head=True)
        self.assertFalse(decision.retry)
        self.assertEqual(decision.reason, "this head was already retried")
        self.assertFalse(decision.needs_human)

    def test_no_ready_runner_refuses(self):
        decision = decide(ready_runners=0)
        self.assertFalse(decision.retry)
        self.assertEqual(decision.reason, "no runner is running; retrying cannot help")
        self.assertFalse(decision.needs_human)

    def test_zero_ready_runners_comes_before_an_exhausted_budget(self):
        # The pool check runs before the attempt budget: a dead pool stops at once and
        # labels nothing, instead of reporting a budget it never got to use.
        decision = decide(ready_runners=0, attempt=5, max_attempts=5)
        self.assertEqual(decision.rule, "no-runners")
        self.assertEqual(decision.reason, "no runner is running; retrying cannot help")
        self.assertFalse(decision.needs_human)
        # The same failure with a runner ready is the budget's refusal.
        self.assertEqual(decide(ready_runners=1, attempt=5, max_attempts=5).rule, "budget")

    def test_the_rules_apply_in_order(self):
        # Every condition true at once; switch them off one by one from the top.
        failing = "\n".join([ARC, "--- FAIL: TestDeliver (0.10s)"])
        conditions = dict(mode="off", already_retried_head=True, ready_runners=0,
                          failure_text=failing, attempt=5, max_attempts=5)
        expected = ["off", "already-retried", "no-runners", "never", "budget"]
        relax = [("mode", "infra"), ("already_retried_head", False), ("ready_runners", 1),
                 ("failure_text", ARC)]
        for step, rule in enumerate(expected):
            with self.subTest(rule=rule):
                self.assertEqual(decide(**conditions).rule, rule)
            if step < len(relax):
                key, value = relax[step]
                conditions[key] = value

    def test_each_retryable_signature_is_retried(self):
        for line, signature in [(ARC, "after its runner reservation ran out"),
                                (EGRESS, "Egress is over the account limit"),
                                (DNS, "dial tcp: lookup"),
                                (RESET, "connection reset by peer")]:
            with self.subTest(signature=signature):
                text = "2026-11-19T10:00:00.0Z ##[group]Run make test\n" + line + "\n"
                decision = decide(failure_text=text)
                self.assertTrue(decision.retry)
                self.assertEqual(decision.retryable_signature, signature)
                self.assertEqual(decision.rule, "retry")
                self.assertIn(signature, decision.reason)

    def test_a_never_line_wins_over_a_retryable_one(self):
        text = "\n".join([DNS, "--- FAIL: TestSMTPRelay (2.31s)", RESET])
        for mode in ("infra", "all"):
            with self.subTest(mode=mode):
                decision = decide(failure_text=text, mode=mode)
                self.assertFalse(decision.retry)
                self.assertEqual(decision.rule, "never")
                self.assertTrue(decision.needs_human)
                self.assertIn('"--- FAIL:"', decision.reason)

    def test_a_plain_fail_line_names_its_pattern(self):
        decision = decide(failure_text="FAIL: test_ready (test_ci_retry.RuleTests)")
        self.assertEqual(decision.rule, "never")
        self.assertIn('"FAIL:"', decision.reason)

    def test_infra_refuses_an_unmatched_failure_and_lists_the_patterns(self):
        decision = decide(failure_text="Error: Process completed with exit code 2.")
        self.assertFalse(decision.retry)
        self.assertEqual(decision.rule, "unmatched")
        self.assertFalse(decision.needs_human)
        self.assertIsNone(decision.retryable_signature)
        for pattern in POLICY.retryable:
            self.assertIn(f'"{pattern}"', decision.reason)

    def test_infra_refuses_a_failure_with_no_logs(self):
        self.assertEqual(decide(failure_text="").rule, "unmatched")

    def test_all_accepts_an_unmatched_failure(self):
        decision = decide(failure_text="Error: Process completed with exit code 2.", mode="all")
        self.assertTrue(decision.retry)
        self.assertIsNone(decision.retryable_signature)
        self.assertEqual(decision.reason, "CI_RETRY=all retries any failure")

    def test_the_cap(self):
        for attempt, retry in [(1, True), (4, True), (5, False), (6, False)]:
            with self.subTest(attempt=attempt):
                decision = decide(attempt=attempt, max_attempts=5)
                self.assertEqual(decision.retry, retry)
                if not retry:
                    self.assertEqual(decision.reason, f"attempt {attempt} of 5 exhausted")
                    self.assertEqual(decision.rule, "budget")
                    self.assertTrue(decision.needs_human)

    def test_the_cap_applies_in_all_mode(self):
        decision = decide(failure_text="exit code 2", mode="all", attempt=3, max_attempts=3)
        self.assertEqual(decision.reason, "attempt 3 of 3 exhausted")


class WhereTheJobRanTests(unittest.TestCase):
    """Rule 3 applies only when a failed job ran on the self-hosted pool."""

    def test_self_hosted_with_no_ready_runner_is_refused(self):
        decision = decide(failed_on_self_hosted=True, ready_runners=0)
        self.assertFalse(decision.retry)
        self.assertEqual(decision.rule, "no-runners")
        self.assertEqual(decision.reason, "no runner is running; retrying cannot help")
        self.assertEqual(decision.runner, "self-hosted")

    def test_self_hosted_with_a_ready_runner_goes_on_to_the_signatures(self):
        decision = decide(failed_on_self_hosted=True, ready_runners=1)
        self.assertTrue(decision.retry)
        self.assertEqual(decision.retryable_signature, "after its runner reservation ran out")
        unmatched = decide(failure_text="exit code 2", failed_on_self_hosted=True, ready_runners=1)
        self.assertEqual(unmatched.rule, "unmatched")

    def test_hosted_with_no_ready_runner_goes_on_and_a_retryable_signature_is_retried(self):
        for line, signature in [(ARC, "after its runner reservation ran out"),
                                (EGRESS, "Egress is over the account limit"),
                                (DNS, "dial tcp: lookup"),
                                (RESET, "connection reset by peer")]:
            with self.subTest(signature=signature):
                decision = decide(failure_text=line, failed_on_self_hosted=False, ready_runners=0)
                self.assertTrue(decision.retry)
                self.assertEqual(decision.retryable_signature, signature)
                self.assertEqual(decision.runner, "hosted")

    def test_hosted_with_a_never_line_is_refused_by_the_pattern_not_the_pool(self):
        text = "--- FAIL: TestRelay (1.2s)\n" + DNS
        decision = decide(failure_text=text, failed_on_self_hosted=False, ready_runners=0)
        self.assertFalse(decision.retry)
        self.assertEqual(decision.rule, "never")
        self.assertTrue(decision.needs_human)
        self.assertIn('"--- FAIL:"', decision.reason)

    def test_self_hosted_still_checks_the_pool_before_the_budget(self):
        decision = decide(failed_on_self_hosted=True, ready_runners=0, attempt=5, max_attempts=5)
        self.assertEqual(decision.rule, "no-runners")
        self.assertFalse(decision.needs_human)

    def test_hosted_skips_the_pool_and_reaches_the_budget(self):
        decision = decide(failed_on_self_hosted=False, ready_runners=0, attempt=5, max_attempts=5)
        self.assertEqual(decision.rule, "budget")
        self.assertTrue(decision.needs_human)

    def test_hosted_keeps_the_earlier_rules(self):
        self.assertEqual(decide(mode="off", failed_on_self_hosted=False).rule, "off")
        self.assertEqual(decide(already_retried_head=True, failed_on_self_hosted=False).rule,
                         "already-retried")
        self.assertEqual(decide(failure_text="exit code 2", failed_on_self_hosted=False,
                                ready_runners=0).rule, "unmatched")


class RunnerOfTests(unittest.TestCase):
    def test_the_pool_is_self_hosted_by_runner_name_or_label(self):
        self.assertEqual(ci_retry.runner_of(SELF_HOSTED_JOB), "self-hosted")
        self.assertEqual(ci_retry.runner_of({"runner_name": "antimatter-vps-linux-x64-runner-x"}),
                         "self-hosted")
        # A job that never got its runner still carries the label it asked for.
        self.assertEqual(ci_retry.runner_of({"runner_name": "", "labels": ["antimatter-vps-linux-x64"]}),
                         "self-hosted")

    def test_github_runners_are_hosted(self):
        self.assertEqual(ci_retry.runner_of(HOSTED_JOB), "hosted")
        self.assertEqual(ci_retry.runner_of({"runner_name": None, "labels": ["ubuntu-latest"]}), "hosted")

    def test_a_job_with_no_runner_reported_is_unknown(self):
        self.assertIsNone(ci_retry.runner_of(UNREPORTED_JOB))
        self.assertIsNone(ci_retry.runner_of({"id": 1}))

    def test_one_self_hosted_job_makes_the_run_self_hosted(self):
        self.assertEqual(ci_retry.failed_runners([HOSTED_JOB, dict(SELF_HOSTED_JOB, id=502)]), (True, []))

    def test_unreported_jobs_count_as_hosted_and_are_named(self):
        self.assertEqual(ci_retry.failed_runners([UNREPORTED_JOB, HOSTED_JOB]), (False, ["Test"]))
        self.assertEqual(ci_retry.failed_runners([]), (False, []))


class RunnerGateTests(unittest.TestCase):
    STATUS = [
        {"org": "antimatter-studios", "workers": [
            {"phase": "Running", "busy": False},
            {"phase": "Running", "busy": False}]},
        {"org": "rest-mail", "workers": [
            {"phase": "Running", "busy": True},
            {"phase": "Running", "busy": False},
            {"phase": "Pending", "busy": False},
            {"phase": "Running"}]},
    ]

    def test_counts_running_idle_workers_of_the_org(self):
        self.assertEqual(ci_retry.count_ready_runners(self.STATUS, "rest-mail"), 1)

    def test_counts_every_pool_without_an_org(self):
        self.assertEqual(ci_retry.count_ready_runners(self.STATUS), 3)

    def test_an_org_with_no_pool_has_no_runner(self):
        self.assertEqual(ci_retry.count_ready_runners(self.STATUS, "elsewhere"), 0)

    def test_an_unreadable_status_counts_zero(self):
        with tempfile.TemporaryDirectory() as directory:
            for name, content in [("html", "<html>502 Bad Gateway</html>"), ("number", "42")]:
                path = Path(directory) / name
                path.write_text(content)
                out, err = io.StringIO(), io.StringIO()
                with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                    ci_retry.main(["ready-runners", "--status-file", str(path), "--org", "rest-mail"])
                self.assertEqual(out.getvalue().strip(), "0")
                self.assertIn("counting 0 ready runners", err.getvalue())

    def test_a_missing_status_file_counts_zero(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            ci_retry.main(["ready-runners", "--status-file", "/nonexistent/status.json"])
        self.assertEqual(out.getvalue().strip(), "0")


class AlreadyRetriedTests(unittest.TestCase):
    def test_a_fresh_failure_is_not_retried_yet(self):
        self.assertFalse(ci_retry.already_retried(7, 2, 2, "completed", ["looks flaky"]))

    def test_a_newer_attempt_means_retried(self):
        self.assertTrue(ci_retry.already_retried(7, 2, 3, "completed", []))

    def test_a_run_in_progress_again_means_retried(self):
        self.assertTrue(ci_retry.already_retried(7, 2, 2, "queued", []))

    def test_a_retry_comment_for_this_attempt_means_retried(self):
        comment = "<!-- ci-retry: retried run=7 attempt=2 -->\nCI retry bot re-ran ..."
        self.assertTrue(ci_retry.already_retried(7, 2, 2, "completed", [comment]))
        self.assertFalse(ci_retry.already_retried(7, 3, 3, "completed", [comment]))
        self.assertFalse(ci_retry.already_retried(8, 2, 2, "completed", [comment]))


class CommandTests(unittest.TestCase):
    def run_decide(self, log, *extra, jobs=(SELF_HOSTED_JOB,)):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            logs = work / "logs"
            logs.mkdir()
            (logs / "123.log").write_text(log)
            failed_jobs = work / "failed-jobs.json"
            failed_jobs.write_text(json.dumps(list(jobs)))
            extra = ("--failed-jobs", str(failed_jobs), *extra)
            output = work / "output"
            comment = work / "comment.md"
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                ci_retry.main(["decide", "--policy", str(POLICY_FILE), "--failure-text", str(logs),
                               "--workflow", "CI", "--run-id", "99",
                               "--run-url", "https://github.com/o/r/actions/runs/99",
                               "--head-sha", "a" * 40, "--comment-out", str(comment),
                               "--github-output", str(output), *extra])
            outputs = {}
            lines = output.read_text().splitlines()
            while lines:
                key = lines.pop(0).split("<<", 1)[0]
                value = []
                while (line := lines.pop(0)) != "CI_RETRY_EOF":
                    value.append(line)
                outputs[key] = "\n".join(value)
            return outputs, comment.read_text() if comment.exists() else None

    def test_a_retry_writes_outputs_and_a_marked_comment(self):
        outputs, comment = self.run_decide(EGRESS, "--attempt", "2", "--ready-runners", "1")
        self.assertEqual(outputs["retry"], "true")
        self.assertEqual(outputs["signature"], "Egress is over the account limit")
        self.assertEqual(outputs["comment"], "true")
        self.assertIn("<!-- ci-retry: retried run=99 attempt=2 -->", comment)
        self.assertIn("attempt 3 of 5", comment)
        self.assertIn("`Egress is over the account limit`", comment)
        self.assertTrue(ci_retry.already_retried(99, 2, 2, "completed", [comment]))

    def test_an_exhausted_budget_asks_for_a_human(self):
        outputs, comment = self.run_decide(ARC, "--attempt", "5", "--max-attempts", "5",
                                           "--ready-runners", "1")
        self.assertEqual(outputs["retry"], "false")
        self.assertEqual(outputs["needs-human"], "true")
        self.assertIn("attempt 5 of 5 exhausted", comment)
        self.assertIn("ci:needs-human", comment)

    def test_a_quiet_refusal_writes_no_comment(self):
        outputs, comment = self.run_decide(ARC, "--ready-runners", "0")
        self.assertEqual(outputs["rule"], "no-runners")
        self.assertEqual(outputs["needs-human"], "false")
        self.assertEqual(outputs["comment"], "false")
        self.assertIsNone(comment)

    def test_a_self_hosted_comment_names_the_pool(self):
        outputs, comment = self.run_decide(ARC, "--ready-runners", "1")
        self.assertEqual(outputs["runner"], "self-hosted")
        self.assertIn("Runner: the self-hosted pool.", comment)

    def test_a_hosted_job_is_retried_with_no_ready_runner(self):
        outputs, comment = self.run_decide(DNS, "--ready-runners", "0", jobs=[HOSTED_JOB])
        self.assertEqual(outputs["retry"], "true")
        self.assertEqual(outputs["runner"], "hosted")
        self.assertIn("Runner: GitHub-hosted, so the pool check was skipped.", comment)
        self.assertNotIn("not reported", comment)

    def test_an_unreported_runner_is_treated_as_hosted_and_said_so(self):
        for jobs, named in [([UNREPORTED_JOB], "Test"), ([], "the failed jobs")]:
            with self.subTest(jobs=jobs):
                outputs, comment = self.run_decide(DNS, "--ready-runners", "0", jobs=jobs)
                self.assertEqual(outputs["retry"], "true")
                self.assertIn(f"The runner of {named} was not reported; treated as GitHub-hosted.", comment)

    def test_an_unreported_runner_is_said_in_the_needs_human_comment_too(self):
        _, comment = self.run_decide("--- FAIL: TestX", "--ready-runners", "0", jobs=[UNREPORTED_JOB])
        self.assertIn("The runner of Test was not reported; treated as GitHub-hosted.", comment)
        self.assertIn("ci:needs-human", comment)

    def test_empty_vars_fall_back_to_the_defaults(self):
        outputs, _ = self.run_decide(DNS, "--mode", "", "--max-attempts", "", "--ready-runners", "1")
        self.assertEqual(outputs["retry"], "true")

    def test_the_decision_is_printed_as_json(self):
        out = io.StringIO()
        with tempfile.NamedTemporaryFile("w", suffix=".log") as log:
            log.write(RESET)
            log.flush()
            with contextlib.redirect_stdout(out):
                ci_retry.main(["decide", "--policy", str(POLICY_FILE), "--failure-text", log.name,
                               "--ready-runners", "3"])
        decision = json.loads(out.getvalue())
        self.assertEqual(decision["retry"], True)
        self.assertEqual(decision["retryable_signature"], "connection reset by peer")


FAKE_GH = r'''#!/usr/bin/env python3
"""A gh stand-in: answers from $STATE, applies --jq with jq, records every call."""
import json, os, re, subprocess, sys
args = sys.argv[1:]
state_path = os.environ["STATE"]
state = json.load(open(state_path))
with open(os.environ["CALLS"], "a") as out:
    out.write(json.dumps(args) + "\n")

def option(name):
    return args[args.index(name) + 1] if name in args else None

def answer(value):
    jq = option("--jq")
    text = json.dumps(value)
    if jq:
        text = subprocess.run(["jq", "-r", jq], input=text, capture_output=True, text=True,
                              check=True).stdout
    sys.stdout.write(text)

def fail(message):
    sys.stderr.write(message + "\n")
    sys.exit(1)

def save():
    json.dump(state, open(state_path, "w"))

if args[:2] == ["pr", "list"]:
    answer(state["prs"])
elif args[:2] == ["label", "create"]:
    if "--force" not in args and args[2] in state["labels"]:
        fail("label already exists")
    state["labels"].append(args[2])
    save()
elif args[0] == "api":
    path = next(a for a in args[1:] if a.startswith("repos/"))
    if re.search(r"/actions/runs/\d+$", path):
        answer(state["run"])
    elif re.search(r"/actions/runs/\d+/rerun-failed-jobs$", path):
        state["reruns"] += 1
        save()
    elif re.search(r"/attempts/\d+/jobs$", path):
        answer({"jobs": state["jobs"]})
    elif re.search(r"/actions/jobs/\d+/logs$", path):
        job = path.split("/")[-2]
        if job not in state["logs"]:
            fail("gh: Not Found (HTTP 404)")
        sys.stdout.write(state["logs"][job])
    elif re.search(r"/check-runs/\d+/annotations$", path):
        answer([{"message": m} for m in state["annotations"].get(path.split("/")[-2], [])])
    elif path.endswith("/labels"):
        if "ci:needs-human" not in state["labels"]:
            fail("gh: Validation Failed (HTTP 422): label does not exist")
        state["pr_labels"].append("ci:needs-human")
        save()
        answer([{"name": n} for n in state["pr_labels"]])
    elif path.endswith("/comments") and "-F" in args:
        state["posted"].append(open(option("-F").split("@", 1)[1]).read())
        save()
        answer({"html_url": "https://github.com/o/r/pull/1#issuecomment-1"})
    elif path.endswith("/comments"):
        answer([{"user": {"login": "github-actions[bot]"}, "body": b} for b in state["comments"]])
    else:
        fail("unexpected api call: " + path)
else:
    fail("unexpected gh call: " + " ".join(args))
'''

FAKE_CURL = r'''#!/bin/sh
# The runner gate: GATE=down fails like an unreachable host, else answers GATE_STATUS.
[ "$GATE" = down ] && { echo "curl: (6) Could not resolve host" >&2; exit 6; }
while [ $# -gt 0 ]; do [ "$1" = -o ] && { printf '%s' "$GATE_STATUS" > "$2"; }; shift; done
'''

HEAD = "c0ffee" + "0" * 34


class WorkflowStepTests(unittest.TestCase):
    """Run the workflow's own shell steps against a fake gh and curl.

    This proves the plumbing between the steps, not GitHub: the workflow_run trigger,
    the token's permissions and the real API answers are not exercised here.
    """

    def run_workflow(self, *, prs=None, gate="up", ready=1, logs=None, annotations=None,
                     attempt=1, run_attempt=None, comments=(), labels=(), mode="infra",
                     max_attempts="5", runner=SELF_HOSTED_JOB):
        workflow = yaml.safe_load(WORKFLOW_FILE.read_text())
        job = workflow["jobs"]["retry"]
        steps = {step["name"]: step for step in job["steps"]}
        state = {
            "prs": [{"number": 41, "headRefOid": HEAD}] if prs is None else prs,
            "run": {"run_attempt": run_attempt or attempt, "status": "completed"},
            "jobs": [dict(runner, conclusion="failure"),
                     {"id": 502, "name": "Lint", "conclusion": "success",
                      "runner_name": "antimatter-vps-linux-x64-runner-zz", "labels": []}],
            "logs": {} if logs is None else logs,
            "annotations": annotations or {},
            "comments": list(comments), "labels": list(labels), "pr_labels": [],
            "reruns": 0, "posted": [],
        }
        gate_status = [{"org": "rest-mail", "workers": [{"phase": "Running", "busy": False}] * ready}]
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            fake = work / "bin"
            fake.mkdir()
            (fake / "gh").write_text(FAKE_GH)
            (fake / "curl").write_text(FAKE_CURL)
            for tool in fake.iterdir():
                tool.chmod(0o755)
            (work / "state.json").write_text(json.dumps(state))
            (work / "tmp").mkdir()
            env = dict(os.environ)
            env.update({
                "PATH": os.pathsep.join([str(fake), str(Path(sys.executable).parent), env["PATH"]]),
                "STATE": str(work / "state.json"), "CALLS": str(work / "calls.jsonl"),
                "GATE": gate, "GATE_STATUS": json.dumps(gate_status),
                "RUNNER_TEMP": str(work / "tmp"),
                "GITHUB_STEP_SUMMARY": str(work / "summary.md"),
                "GH_TOKEN": "fake", "GH_REPO": "rest-mail/rest-mail-server", "ORG": "rest-mail",
                "CI_RETRY": mode, "CI_RETRY_MAX": max_attempts,
                "RUNNER_GATE_URL": "https://gate.invalid/status",
                "RUN_ID": "9001", "RUN_ATTEMPT": str(attempt),
                "RUN_URL": "https://github.com/rest-mail/rest-mail-server/actions/runs/9001",
                "HEAD_SHA": HEAD, "WORKFLOW": "CI",
            })
            for key in job["env"]:
                self.assertIn(key, env, "the harness must set every job env: " + key)
            outputs = {}

            def run(name):
                step = steps[name]
                output = work / ("output-" + str(len(outputs)))
                output.write_text("")
                step_env = dict(env, GITHUB_OUTPUT=str(output))
                for key, value in step.get("env", {}).items():
                    found = re.fullmatch(r"\$\{\{ steps\.(\w+)\.outputs\.([\w-]+) \}\}", value)
                    step_env[key] = outputs.get(found.group(1), {}).get(found.group(2), "")
                script = work / "step.sh"
                script.write_text(step["run"])
                result = subprocess.run(["bash", "--noprofile", "--norc", "-eo", "pipefail", str(script)],
                                        cwd=ROOT, env=step_env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, name + "\n" + result.stdout + result.stderr)
                outputs[step.get("id", name)] = parse_outputs(output.read_text())
                return result.stdout + result.stderr

            log = run("Find the open pull request whose head failed")
            if outputs["pr"]["number"]:
                log += run("Gather the facts")
                log += run("Decide")
                if outputs["decide"]["retry"] == "true":
                    log += run("Re-run the failed jobs and say so on the pull request")
                if outputs["decide"]["needs-human"] == "true":
                    log += run("Ask a person to look")
            return json.loads((work / "state.json").read_text()), outputs, log

    def test_an_annotation_is_enough_when_the_job_has_no_log(self):
        state, outputs, log = self.run_workflow(annotations={"501": [ARC]})
        self.assertIn("Job 501 (Test) has no log", log)
        self.assertEqual(outputs["decide"]["signature"], "after its runner reservation ran out")
        self.assertEqual(state["reruns"], 1)
        self.assertEqual(len(state["posted"]), 1)
        self.assertIn("<!-- ci-retry: retried run=9001 attempt=1 -->", state["posted"][0])

    def test_an_unreachable_gate_retries_nothing(self):
        state, outputs, log = self.run_workflow(gate="down", logs={"501": DNS})
        self.assertIn("did not answer; counting 0 ready runners", log)
        self.assertEqual(outputs["decide"]["rule"], "no-runners")
        self.assertEqual((state["reruns"], state["posted"], state["pr_labels"]), (0, [], []))

    def test_a_missing_label_is_created_before_it_is_added(self):
        state, outputs, _ = self.run_workflow(logs={"501": "--- FAIL: TestRelay (1.2s)\n" + DNS})
        self.assertEqual(outputs["decide"]["rule"], "never")
        self.assertEqual(state["reruns"], 0)
        self.assertEqual(state["labels"], ["ci:needs-human"])
        self.assertEqual(state["pr_labels"], ["ci:needs-human"])
        self.assertIn('never pattern "--- FAIL:"', state["posted"][0])

    def test_an_existing_label_does_not_fail_the_step(self):
        state, _, _ = self.run_workflow(logs={"501": RESET}, attempt=5, labels=["ci:needs-human"])
        self.assertEqual(state["pr_labels"], ["ci:needs-human"])
        self.assertIn("attempt 5 of 5 exhausted", state["posted"][0])

    def test_a_hosted_job_is_retried_while_the_gate_is_down(self):
        state, outputs, log = self.run_workflow(gate="down", logs={"501": DNS}, runner=HOSTED_JOB)
        self.assertIn("Job 501 (Test) ran on: GitHub Actions 1000000123.", log)
        self.assertEqual(outputs["decide"]["runner"], "hosted")
        self.assertEqual(state["reruns"], 1)
        self.assertIn("Runner: GitHub-hosted, so the pool check was skipped.", state["posted"][0])

    def test_a_job_with_no_runner_name_is_treated_as_hosted_and_said_so(self):
        state, outputs, log = self.run_workflow(gate="down", logs={"501": RESET}, runner=UNREPORTED_JOB)
        self.assertIn("Job 501 (Test) ran on: a runner the jobs API did not report.", log)
        self.assertEqual(outputs["decide"]["retry"], "true")
        self.assertIn("The runner of Test was not reported; treated as GitHub-hosted.", state["posted"][0])

    def test_no_pull_request_skips_everything(self):
        stacked = [{"number": 42, "headRefOid": "f" * 40}]
        state, outputs, log = self.run_workflow(prs=stacked, logs={"501": ARC})
        self.assertEqual(outputs["pr"]["number"], "")
        self.assertIn("nothing to retry", log)
        self.assertEqual(state["reruns"], 0)

    def test_a_run_already_on_a_newer_attempt_is_left_alone(self):
        state, outputs, _ = self.run_workflow(logs={"501": ARC}, attempt=2, run_attempt=3)
        self.assertEqual(outputs["decide"]["rule"], "already-retried")
        self.assertEqual(state["reruns"], 0)

    def test_a_retry_this_bot_already_commented_is_not_repeated(self):
        marker = "<!-- ci-retry: retried run=9001 attempt=2 -->\nCI retry bot re-ran ..."
        state, outputs, _ = self.run_workflow(logs={"501": ARC}, attempt=2, comments=[marker])
        self.assertEqual(outputs["decide"]["rule"], "already-retried")
        self.assertEqual(state["reruns"], 0)

    def test_attempt_four_of_five_starts_the_last_one(self):
        state, outputs, _ = self.run_workflow(logs={"501": EGRESS}, attempt=4)
        self.assertEqual(outputs["decide"]["retry"], "true")
        self.assertIn("attempt 5 of 5 starts now", state["posted"][0])


def parse_outputs(text):
    outputs, lines = {}, text.splitlines()
    while lines:
        line = lines.pop(0)
        if "<<" in line:
            key, delimiter = line.split("<<", 1)
            value = []
            while (line := lines.pop(0)) != delimiter:
                value.append(line)
            outputs[key] = "\n".join(value)
        elif "=" in line:
            key, value = line.split("=", 1)
            outputs[key] = value
    return outputs


if __name__ == "__main__":
    unittest.main()
