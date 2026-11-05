"""Exercise the workflow rules and real gate scripts without a GitHub runner (#1775)."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github/workflows"
DEFAULT_TYPES = {"opened", "synchronize", "reopened"}
REQUIRED = {"ci.yml": ("ci", "CI"), "e2e.yml": ("e2e", "E2E (full topology)")}


def blocks(source, indent):
    matches = list(re.finditer(rf"^{' ' * indent}([\w-]+):.*$", source, re.MULTILINE))
    return {
        match[1]: source[match.end():matches[index + 1].start() if index + 1 < len(matches) else len(source)]
        for index, match in enumerate(matches)
    }


def field(source, name, indent):
    match = re.search(rf"^{' ' * indent}{re.escape(name)}: (.+)$", source, re.MULTILINE)
    return match[1] if match else ""


def context(event="pull_request", action="opened", base="main", changes=None, label="run-e2e", run_id=100, actor="someone"):
    return {
        "github": {
            "event_name": event,
            "actor": actor,
            "base_ref": base if event == "pull_request" else "",
            "ref": "refs/pull/42/merge" if event == "pull_request" else "refs/heads/main",
            "run_id": run_id,
            "event": {"action": action, "changes": changes or {}, "label": {"name": label},
                      "pull_request": {"number": 42} if event == "pull_request" else {}},
        },
        "needs": {"changes": {"outputs": {"run_e2e": "true"}}},
    }


def evaluate(expression, values):
    """Evaluate the workflows' boolean/string subset, including absent webhook fields."""
    expression = expression.removeprefix("${{").removesuffix("}}").strip()

    def lookup(match):
        value = values
        for part in match[0].split("."):
            value = value.get(part, "") if isinstance(value, dict) else ""
        return repr(value)

    expression = re.sub(r"\b(?:github|needs|inputs)(?:\.[\w-]+)+", lookup, expression)
    expression = expression.replace("&&", " and ").replace("||", " or ")
    return eval(expression, {"__builtins__": {}}, {
        "always": lambda: True,
        "contains": lambda items, item: item in items,
        "fromJSON": json.loads,
        "format": lambda template, *args: template.format(*args),
    })


def shell_of(job, step):
    source = job.split(f"      - name: {step}\n", 1)[1]
    source = source.split("      - name:", 1)[0]
    return textwrap.dedent(source.split("        run: |\n", 1)[1])


class StackedPRWorkflows(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflows = {}
        cls.jobs = {}
        for path in WORKFLOWS.glob("*.yml"):
            source = path.read_text()
            if "pull_request" not in blocks(blocks(source, 0).get("on", ""), 2):
                continue
            cls.workflows[path.name] = source
            cls.jobs[path.name] = blocks(blocks(source, 0)["jobs"], 2)
        cls.full = {name: source for name, source in cls.workflows.items() if name not in {"ci-labels.yml", "ci-retarget.yml"}}

    def test_every_pr_workflow_is_covered_and_keeps_its_existing_events(self):
        self.assertEqual(set(self.full), {
            "ci.yml", "e2e.yml", "api-gaps.yml", "email-explained.yml", "pulumi.yml", "release-cli.yml",
        })
        for name, source in self.full.items():
            with self.subTest(workflow=name):
                trigger = blocks(blocks(source, 0)["on"], 2)["pull_request"]
                types = set(field(trigger, "types", 4).strip("[]").split(", "))
                expected = set(DEFAULT_TYPES)
                if name not in REQUIRED:
                    expected.add("edited")
                if name == "ci.yml":
                    expected.add("ready_for_review")
                self.assertEqual(types, expected)

    def assert_jobs(self, values, enabled):
        for name in self.full:
            for job_name, job in self.jobs[name].items():
                with self.subTest(workflow=name, job=job_name, values=values):
                    self.assertTrue(field(job, "if", 4), "Every PR job needs a rule before runner allocation")
                    # A dispatched run has no CI run for the proof union to join.
                    expected = enabled and not (job_name == "e2e-proofs" and values["github"]["event_name"] == "workflow_dispatch") and not (job_name == "contract-diff" and values["github"]["event_name"] != "pull_request") and not (job_name == "seed-caches" and values["github"]["event_name"] != "push")
                    self.assertEqual(bool(evaluate(field(job, "if", 4), values)), expected)

    def test_stacked_prs_allocate_no_runners_even_with_run_e2e(self):
        for action in (*DEFAULT_TYPES, "ready_for_review", "edited"):
            self.assert_jobs(context(action=action, base="feature/parent"), False)
        self.assert_jobs(context(action="edited", base="feature/parent", changes={"base": {"ref": {"from": "main"}}}), False)

    def test_main_prs_and_retargeting_start_the_usual_jobs(self):
        for action in (*DEFAULT_TYPES, "ready_for_review"):
            self.assert_jobs(context(action=action), True)
        self.assert_jobs(context(action="edited", changes={"base": {"ref": {"from": "feature/parent"}}}), True)
        self.assert_jobs(context(action="edited", changes={"base": {"ref": {"from": "feature/parent"}}, "title": {"from": "old"}}), True)

    def test_title_body_and_base_sha_edits_allocate_no_runners(self):
        for changes in ({}, {"title": {"from": "old"}}, {"body": {"from": "old"}}, {"base": {"sha": {"from": "old"}}}):
            self.assert_jobs(context(action="edited", changes=changes), False)

    def test_push_merge_group_and_dispatch_keep_their_job_decisions(self):
        for event in ("push", "merge_group", "workflow_dispatch"):
            self.assert_jobs(context(event=event), True)
        for event in ("pull_request", "push", "merge_group", "workflow_dispatch"):
            values = context(event=event)
            values["needs"]["changes"]["outputs"]["run_e2e"] = "false"
            self.assertFalse(evaluate(field(self.jobs["e2e.yml"]["e2e-run"], "if", 4), values))

    def test_a_merge_queue_push_does_not_run_ci_again_on_the_commit_it_tested(self):
        queue = context(event="push", actor="github-merge-queue[bot]")
        for job_name, job in self.jobs["ci.yml"].items():
            with self.subTest(job=job_name):
                self.assertEqual(bool(evaluate(field(job, "if", 4), queue)), job_name in {"seed-caches", "ci"})
        # Only the queue's own push: anyone else's has not been through the queue.
        for job_name, job in self.jobs["ci.yml"].items():
            with self.subTest(job=job_name, actor="a person"):
                self.assertEqual(bool(evaluate(field(job, "if", 4), context(event="push"))), job_name != "contract-diff")

    def test_only_the_queues_own_push_skips_and_every_gate_still_reports(self):
        for values in (context(event="merge_group", actor="github-merge-queue[bot]"), context(actor="github-merge-queue[bot]")):
            with self.subTest(event=values["github"]["event_name"]):
                self.assertTrue(evaluate(field(self.jobs["ci.yml"]["test"], "if", 4), values))
                self.assertTrue(evaluate(field(self.jobs["ci.yml"]["ci"], "if", 4), values))

    def test_required_contexts_report_skipped_on_stacks_but_edits_cannot_replace_them(self):
        for workflow, (job_name, required_name) in REQUIRED.items():
            job = self.jobs[workflow][job_name]
            for values in (context(), context(base="feature/parent"), context(event="merge_group"),
                           context(action="edited", changes={"base": {"ref": {"from": "feature/parent"}}})):
                with self.subTest(workflow=workflow, values=values):
                    self.assertEqual(evaluate(field(job, "name", 4), values), required_name)
            self.assertNotEqual(evaluate(field(job, "name", 4), context(action="edited")), required_name)
            self.assertIn("always()", field(job, "if", 4))

    def test_ignored_edits_cannot_cancel_running_or_pending_work(self):
        expected = {"api-gaps.yml": "api-gap-issues-42", "email-explained.yml": "email-explained-publish"}
        for name, active in expected.items():
            expression = field(blocks(self.workflows[name], 0)["concurrency"], "group", 2)
            with self.subTest(workflow=name):
                self.assertEqual(evaluate(expression, context()), active)
                self.assertEqual(evaluate(expression, context(action="edited", changes={"base": {"ref": {"from": "parent"}}})), active)
                ignored = evaluate(expression, context(action="edited"))
                self.assertNotEqual(ignored, active)
                self.assertNotEqual(ignored, evaluate(expression, context(action="edited", run_id=101)))
        expression = field(blocks(self.workflows["email-explained.yml"], 0)["concurrency"], "group", 2)
        self.assertNotEqual(evaluate(expression, context(base="parent")), "email-explained-publish")

    def test_ci_verdict_still_rejects_every_failure_and_cancellation(self):
        gate = self.jobs["ci.yml"]["ci"]
        needs = field(gate, "needs", 4).strip("[]").split(", ")
        self.assertEqual(set(needs), set(self.jobs["ci.yml"]) - {"ci"})
        self.assertEqual(set(re.findall(r"needs\.([\w-]+)\.result", gate)), set(needs))
        script = shell_of(gate, "Verdict")
        for status in ("success", "skipped", "failure", "cancelled"):
            for failed in needs:
                results = "\n".join(f"{name}={status if name == failed else 'success'}" for name in needs)
                with self.subTest(job=failed, status=status):
                    result = subprocess.run(["bash", "-c", script], env=dict(os.environ, RESULTS=results), capture_output=True, text=True, timeout=5)
                    self.assertEqual(result.returncode, int(status not in {"success", "skipped"}), result.stdout + result.stderr)

    def test_e2e_verdict_preserves_intentional_skips_and_rejects_failed_work(self):
        script = shell_of(self.jobs["e2e.yml"]["e2e"], "Verify e2e outcome")
        cases = [("success", "false", "skipped", 0), ("success", "true", "success", 0),
                 ("success", "true", "failure", 1), ("success", "true", "cancelled", 1),
                 ("success", "true", "skipped", 1), ("failure", "", "skipped", 1),
                 ("cancelled", "", "skipped", 1), ("skipped", "", "skipped", 1)]
        for changes, decision, heavy, expected in cases:
            with self.subTest(changes=changes, decision=decision, heavy=heavy):
                result = subprocess.run(["bash", "-c", script], env=dict(os.environ, CHANGES_RESULT=changes, RUN_E2E=decision, E2E_RESULT=heavy, PROOFS_RESULT="success", EVENT_NAME="pull_request"), capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)

    def test_e2e_verdict_requires_the_proof_union_when_the_heavy_suite_ran(self):
        script = shell_of(self.jobs["e2e.yml"]["e2e"], "Verify e2e outcome")
        for proofs, event, expected in (("failure", "merge_group", 1), ("skipped", "merge_group", 1),
                                        ("success", "merge_group", 0), ("skipped", "workflow_dispatch", 0)):
            with self.subTest(proofs=proofs, event=event):
                result = subprocess.run(["bash", "-c", script], env=dict(os.environ, CHANGES_RESULT="success", RUN_E2E="true", E2E_RESULT="success", PROOFS_RESULT=proofs, EVENT_NAME=event), capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)

    def test_e2e_main_pr_opt_in_and_merge_queue_policy_are_unchanged(self):
        script = shell_of(self.jobs["e2e.yml"]["changes"], "Classify diff (docs-only -> skip heavy e2e)")
        for event, label, expected in (("pull_request", "true", "true"),
                                       ("pull_request", "false", "false"),
                                       ("push", "true", "false"),
                                       ("workflow_dispatch", "false", "true")):
            with self.subTest(event=event, label=label), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / "output"
                result = subprocess.run(
                    ["bash", "-c", script], capture_output=True, text=True, timeout=5,
                    env=dict(os.environ, EVENT_NAME=event, RUN_E2E_LABEL=label, GITHUB_OUTPUT=str(output)),
                )
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(output.read_text().strip(), f"run_e2e={expected}")

    def test_label_helper_does_no_work_for_stacks_or_claim_labels(self):
        expression = field(self.jobs["ci-labels.yml"]["rerun-contract-gate"], "if", 4)
        for label in ("contract-change", "permission-change", "claimed:macbook", "run-e2e"):
            self.assertFalse(evaluate(expression, context(base="parent", label=label)))
            self.assertEqual(bool(evaluate(expression, context(label=label))), label in {"contract-change", "permission-change"})

    def test_an_edit_starts_no_run_of_a_workflow_that_owns_a_required_check(self):
        # A run on the head, even a skipped one with no required name in it, leaves GitHub
        # saying the required check is expected: enqueuePullRequest refused #1692 and #1900
        # with both checks green in the older runs (#1876). So only a new event may start one.
        for workflow in REQUIRED:
            with self.subTest(workflow=workflow):
                source = self.workflows[workflow]
                trigger = blocks(blocks(source, 0)["on"], 2)["pull_request"]
                self.assertNotIn("edited", field(trigger, "types", 4))
                self.assertNotIn("run-name", blocks(source, 0))
                self.assertNotIn("github.event.action", field(blocks(source, 0)["concurrency"], "group", 2))

    def test_no_other_workflow_can_report_a_required_check(self):
        required = {name for _, name in REQUIRED.values()}
        for workflow, jobs in self.jobs.items():
            if workflow in REQUIRED:
                continue
            for job_name, job in jobs.items():
                with self.subTest(workflow=workflow, job=job_name):
                    self.assertNotIn(field(job, "name", 4), required)

    def test_a_skipped_gate_cannot_read_as_the_required_check(self):
        # GitHub reports a skipped job's name unevaluated (#1895's stack gates, #1900's edit
        # gates). A plain name would report as skipped, which a required check accepts.
        for workflow, (job_name, required_name) in REQUIRED.items():
            with self.subTest(workflow=workflow):
                name = field(self.jobs[workflow][job_name], "name", 4)
                self.assertNotEqual(name, required_name)
                self.assertRegex(name, r"^\$\{\{ .*github\..* \}\}$")

    def test_retarget_relay_runs_only_for_a_base_change_to_main(self):
        expression = field(self.jobs["ci-retarget.yml"]["dispatch-required-checks"], "if", 4)
        self.assertTrue(evaluate(expression, context(action="edited", changes={"base": {"ref": {"from": "feature/parent"}}})))
        self.assertTrue(evaluate(expression, context(action="edited", changes={"base": {"ref": {"from": "feature/parent"}}, "title": {"from": "old"}})))
        self.assertFalse(evaluate(expression, context(action="edited", base="feature/other", changes={"base": {"ref": {"from": "feature/parent"}}})))
        for changes in ({}, {"title": {"from": "old"}}, {"body": {"from": "old"}}, {"base": {"sha": {"from": "old"}}}):
            with self.subTest(changes=changes):
                self.assertFalse(evaluate(expression, context(action="edited", changes=changes)))
        trigger = blocks(blocks(self.workflows["ci-retarget.yml"], 0)["on"], 2)["pull_request"]
        self.assertEqual(field(trigger, "types", 4), "[edited]")

    def run_relay(self, gh_exit):
        script = shell_of(self.jobs["ci-retarget.yml"]["dispatch-required-checks"], "Dispatch CI and E2E for the pull request's head")
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "calls"
            gh = Path(directory) / "gh"
            gh.write_text(f'#!/bin/sh\necho "$*" >> "{log}"\nexit {gh_exit}\n')
            gh.chmod(0o755)
            env = dict(os.environ, PATH=f"{directory}:{os.environ['PATH']}", GITHUB_REPOSITORY="rest-mail/rest-mail-server",
                       HEAD_REF="feature/layer", PR="42", BASE_SHA="abc123")
            result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, timeout=5)
            return result.returncode, log.read_text().splitlines()

    def test_retarget_relay_dispatches_both_required_workflows_for_the_head(self):
        code, calls = self.run_relay(0)
        self.assertEqual(code, 0)
        self.assertEqual(calls, [
            "workflow run ci.yml --repo rest-mail/rest-mail-server --ref feature/layer -f pull-request=42 -f base-sha=abc123",
            "workflow run e2e.yml --repo rest-mail/rest-mail-server --ref feature/layer",
        ])
        dispatch = blocks(blocks(self.workflows["ci.yml"], 0)["on"], 2)["workflow_dispatch"]
        for name in ("pull-request", "base-sha"):
            self.assertIn(f"      {name}:", dispatch)

    def test_retarget_relay_fails_when_github_refuses_the_dispatch(self):
        code, calls = self.run_relay(1)
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 1)

    def test_a_retarget_dispatch_still_runs_the_contract_gate(self):
        job = self.jobs["ci.yml"]["contract-diff"]
        expression = field(job, "if", 4)
        dispatch = context(event="workflow_dispatch")
        dispatch["inputs"] = {"pull-request": "42", "base-sha": "abc123"}
        self.assertTrue(evaluate(expression, dispatch))
        # The Vertrag bump dispatches with no pull request: there is nothing to compare.
        self.assertFalse(evaluate(expression, context(event="workflow_dispatch")))
        queue = context(event="merge_group")
        queue["inputs"] = {"pull-request": "42"}
        self.assertFalse(evaluate(expression, queue))
        for expected in ("github.event.pull_request.head.sha || github.sha",
                         "github.event.pull_request.base.sha || inputs.base-sha",
                         "github.event.pull_request.number || inputs.pull-request"):
            self.assertIn(expected, job)

    def test_label_helper_reruns_the_newest_ci_run_of_any_event(self):
        # After a retarget the newest run on the head was dispatched, not a pull_request run.
        script = shell_of(self.jobs["ci-labels.yml"]["rerun-contract-gate"], "Re-run the contract gate once the newest CI run is done")
        lookup = script.split("gh run list", 1)[1].split("--jq", 1)[0]
        self.assertNotIn("--event", lookup)
        self.assertIn("--limit 1", lookup)


if __name__ == "__main__":
    unittest.main()
