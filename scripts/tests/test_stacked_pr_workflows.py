"""Exercise the workflow rules and real gate scripts without a GitHub runner (#1775)."""

import json
import os
from pathlib import Path
import re
import shutil
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

    expression = re.sub(r"\b(?:github|needs)(?:\.[\w-]+)+", lookup, expression)
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
        cls.full = {name: source for name, source in cls.workflows.items() if name != "ci-labels.yml"}

    def test_every_pr_workflow_is_covered_and_keeps_its_existing_events(self):
        self.assertEqual(set(self.full), {
            "ci.yml", "e2e.yml", "email-explained.yml", "pulumi.yml", "release-cli.yml",
        })
        for name, source in self.full.items():
            with self.subTest(workflow=name):
                trigger = blocks(blocks(source, 0)["on"], 2)["pull_request"]
                types = set(field(trigger, "types", 4).strip("[]").split(", "))
                expected = DEFAULT_TYPES | {"edited"}
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

    def test_stacked_prs_run_real_gates_even_with_run_e2e(self):
        for action in (*DEFAULT_TYPES, "ready_for_review"):
            self.assert_jobs(context(action=action, base="feature/parent"), True)
        for previous in ("main", "feature/other-parent"):
            self.assert_jobs(context(action="edited", base="feature/parent", changes={"base": {"ref": {"from": previous}}}), True)

    def test_main_prs_and_retargeting_start_the_usual_jobs(self):
        for action in (*DEFAULT_TYPES, "ready_for_review"):
            self.assert_jobs(context(action=action), True)
        self.assert_jobs(context(action="edited", changes={"base": {"ref": {"from": "feature/parent"}}}), True)
        self.assert_jobs(context(action="edited", changes={"base": {"ref": {"from": "feature/parent"}}, "title": {"from": "old"}}), True)

    def test_title_body_and_base_sha_edits_allocate_no_runners(self):
        for changes in ({}, {"title": {"from": "old"}}, {"body": {"from": "old"}}, {"base": {"sha": {"from": "old"}}}):
            for base in ("main", "feature/parent"):
                self.assert_jobs(context(action="edited", base=base, changes=changes), False)

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

    def test_required_contexts_report_real_verdicts_on_stacks_but_edits_cannot_replace_them(self):
        for workflow, (job_name, required_name) in REQUIRED.items():
            job = self.jobs[workflow][job_name]
            for values in (context(), context(base="feature/parent"), context(event="merge_group"),
                           context(action="edited", changes={"base": {"ref": {"from": "feature/parent"}}})):
                with self.subTest(workflow=workflow, values=values):
                    self.assertEqual(evaluate(field(job, "name", 4), values), required_name)
            self.assertNotEqual(evaluate(field(job, "name", 4), context(action="edited")), required_name)
            self.assertIn("always()", field(job, "if", 4))

    def test_ignored_edits_cannot_cancel_running_or_pending_work(self):
        expected = {"ci.yml": "ci-refs/pull/42/merge", "e2e.yml": "e2e-refs/pull/42/merge",
                    "email-explained.yml": "email-explained-publish"}
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
                    result = subprocess.run(["bash", "-c", script], env=dict(os.environ, RESULTS=results), capture_output=True, text=True, timeout=600)
                    self.assertEqual(result.returncode, int(status not in {"success", "skipped"}), result.stdout + result.stderr)

    def test_e2e_verdict_preserves_intentional_skips_and_rejects_failed_work(self):
        script = shell_of(self.jobs["e2e.yml"]["e2e"], "Verify e2e outcome")
        cases = [("success", "false", "skipped", 0), ("success", "true", "success", 0),
                 ("success", "true", "failure", 1), ("success", "true", "cancelled", 1),
                 ("success", "true", "skipped", 1), ("failure", "", "skipped", 1),
                 ("cancelled", "", "skipped", 1), ("skipped", "", "skipped", 1)]
        for changes, decision, heavy, expected in cases:
            with self.subTest(changes=changes, decision=decision, heavy=heavy):
                result = subprocess.run(["bash", "-c", script], env=dict(os.environ, CHANGES_RESULT=changes, RUN_E2E=decision, E2E_RESULT=heavy, PAIRS_RESULT="success" if decision == "true" else "skipped", PROOFS_RESULT="success", EVENT_NAME="pull_request"), capture_output=True, text=True, timeout=600)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)

    def test_e2e_verdict_requires_the_proof_union_when_the_heavy_suite_ran(self):
        script = shell_of(self.jobs["e2e.yml"]["e2e"], "Verify e2e outcome")
        for proofs, event, expected in (("failure", "merge_group", 1), ("skipped", "merge_group", 1),
                                        ("success", "merge_group", 0), ("skipped", "workflow_dispatch", 0)):
            with self.subTest(proofs=proofs, event=event):
                result = subprocess.run(["bash", "-c", script], env=dict(os.environ, CHANGES_RESULT="success", RUN_E2E="true", E2E_RESULT="success", PAIRS_RESULT="success", PROOFS_RESULT=proofs, EVENT_NAME=event), capture_output=True, text=True, timeout=600)
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
                    ["bash", "-c", script], capture_output=True, text=True, timeout=600,
                    env=dict(os.environ, EVENT_NAME=event, RUN_E2E_LABEL=label, GITHUB_OUTPUT=str(output)),
                )
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(output.read_text().strip(), f"run_e2e={expected}")

    def test_label_helper_reruns_stack_contract_gates_but_ignores_claim_labels(self):
        expression = field(self.jobs["ci-labels.yml"]["rerun-contract-gate"], "if", 4)
        for label in ("contract-change", "permission-change", "claimed:macbook", "run-e2e"):
            for base in ("main", "parent"):
                self.assertEqual(bool(evaluate(expression, context(base=base, label=label))), label in {"contract-change", "permission-change"})

    @unittest.skipUnless(shutil.which("jq"), "jq is needed to exercise gh's run selection")
    def test_label_helper_ignores_metadata_receipts_even_if_they_conclude_success(self):
        script = shell_of(self.jobs["ci-labels.yml"]["rerun-contract-gate"], "Re-run the contract gate once the newest CI run is done")
        query = re.search(r"--jq '([^']+)'", script.split("gh run list", 1)[1])[1]
        title = evaluate(field(self.workflows["ci.yml"], "run-name", 0), context(action="edited"))
        runs = [{"databaseId": 102, "status": "completed", "conclusion": "success", "displayTitle": title},
                {"databaseId": 101, "status": "completed", "displayTitle": "Real CI"}]
        for candidates, expected in ((runs, "101 completed"), (runs[:1], ""), ([], "")):
            with self.subTest(candidates=candidates):
                result = subprocess.run(["jq", "-r", query], input=json.dumps(candidates), capture_output=True, text=True, check=True, timeout=600)
                self.assertEqual(result.stdout.strip(), expected)


if __name__ == "__main__":
    unittest.main()
