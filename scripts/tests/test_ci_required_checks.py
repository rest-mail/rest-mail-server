"""The live main/merge-queue required contexts cannot disappear or skip green (#2113 AC06)."""

import copy
import json
import os
from pathlib import Path
import re
import subprocess
import unittest

import yaml

from test_stacked_pr_workflows import context, evaluate


ROOT = Path(__file__).resolve().parents[2]
INVENTORY = ROOT / "scripts/ci-required-checks.json"


def workflows():
    return {
        path.name: yaml.safe_load(path.read_text())
        for path in sorted((ROOT / ".github/workflows").glob("*.y*ml"))
    }


def event_contexts(document):
    triggers = document.get("on", document.get(True, {}))
    if isinstance(triggers, str):
        triggers = [triggers]
    for event in ("pull_request", "merge_group"):
        if event not in triggers:
            raise ValueError(f"required workflow has no {event} trigger")
        trigger = triggers[event] if isinstance(triggers, dict) else None
        if trigger and any(key in trigger for key in ("paths", "paths-ignore", "branches", "branches-ignore")):
            raise ValueError(f"required {event} workflow is filtered")
        actions = (trigger or {}).get("types", ["opened", "synchronize", "reopened"] if event == "pull_request" else ["checks_requested"])
        if event == "pull_request" and not {"opened", "synchronize", "reopened"} <= set(actions):
            raise ValueError("required pull_request trigger omits a code event")
        # Retargeting and ignored edits must stay safe even though #1876 excludes
        # edited from today's triggers (ci-retarget.yml dispatches base changes).
        if event == "pull_request":
            actions = sorted(set(actions) | {"edited"})
        for action in actions:
            for base in ("main", "feature/parent"):
                changes = [{}]
                if event == "pull_request" and action == "edited":
                    changes += [
                        {"title": {"from": "old"}}, {"body": {"from": "old"}},
                        {"base": {"sha": {"from": "old"}}},
                        {"base": {"ref": {"from": "main"}}},
                        {"base": {"ref": {"from": "feature/parent"}}},
                        {"base": {"ref": {"from": "feature/other"}}, "title": {"from": "old"}},
                    ]
                for change in changes:
                    yield context(event=event, action=action, base=base, changes=change)


def ignored_edit(values):
    github = values["github"]
    event = github["event"]
    return (github["event_name"] == "pull_request" and event["action"] == "edited"
            and not event["changes"].get("base", {}).get("ref", {}).get("from"))


def job_name(job_id, job, values):
    name = job.get("name", job_id)
    return evaluate(name, values) if name.startswith("${{") else name


def inventory_problems(documents, inventory):
    problems = []
    checks = inventory["checks"]
    if {entry["context"] for entry in checks} != set(inventory["main"]["required_status_checks"]):
        problems.append("main required contexts differ from their producers")
    if inventory["merge_queue"]["required_status_checks"] != inventory["main"]["required_status_checks"]:
        problems.append("merge queue must require the main contexts")
    for entry in checks:
        label = entry["context"]
        document = documents.get(entry["workflow"], {})
        job = document.get("jobs", {}).get(entry["job"])
        if job is None:
            problems.append(f"{label}: required producer missing")
            continue
        # Names may vary only by the webhook fields exercised below. In particular,
        # an actor/input-dependent alias cannot hide behind our example context.
        name_fields = set(re.findall(r"\b(?:github|needs|inputs)(?:\.[\w-]+)+", job.get("name", "")))
        if name_fields - {"github.event_name", "github.event.action", "github.event.changes.base.ref.from"}:
            problems.append(f"{label}: required name depends on unverified context")
        try:
            values_list = list(event_contexts(document))
        except ValueError as error:
            problems.append(f"{label}: {error}")
            continue
        if job.get("continue-on-error") or "matrix" in job.get("strategy", {}):
            problems.append(f"{label}: required producer is optional or matrix-named")
        if "always()" not in job.get("if", ""):
            problems.append(f"{label}: verdict can be skipped after dependency failure")
        for values in values_list:
            if ignored_edit(values):
                if job_name(entry["job"], job, values) == label:
                    problems.append(f"{label}: ignored edit can replace the required check")
                if evaluate(job.get("if", "always()"), values):
                    problems.append(f"{label}: ignored edit runs the verdict")
                continue
            if job_name(entry["job"], job, values) != label:
                problems.append(f"{label}: required job renamed")
                break
            if not evaluate(job.get("if", "always()"), values):
                problems.append(f"{label}: required job skips itself")
                break
            for dependency in job.get("needs", []):
                for status in ("failure", "cancelled", "skipped"):
                    failed = copy.deepcopy(values)
                    failed["needs"].setdefault(dependency, {})["result"] = status
                    if not evaluate(job.get("if", "always()"), failed):
                        problems.append(f"{label}: required job skips after {dependency}={status}")
        for step in job.get("steps", []):
            if step.get("if") or step.get("continue-on-error"):
                problems.append(f"{label}: verdict step can skip or ignore failure")
        for filename, other in documents.items():
            for job_id, candidate in other.get("jobs", {}).items():
                if (filename, job_id) == (entry["workflow"], entry["job"]):
                    continue
                dynamic_alias = re.search(r"(['\"])" + re.escape(label) + r"\1", candidate.get("name", ""))
                if dynamic_alias or any(job_name(job_id, candidate, values) == label for values in values_list):
                    problems.append(f"{label}: duplicate producer {filename}/{job_id}")
    return problems


class RequiredCheckInventory(unittest.TestCase):
    def setUp(self):
        self.documents = workflows()
        self.inventory = json.loads(INVENTORY.read_text())

    def test_ac06_required_inventory_matches_live_rule_snapshot_and_stable_producers(self):
        self.assertEqual(self.inventory["main"]["required_status_checks"], ["CI", "E2E (full topology)"])
        self.assertEqual(self.inventory["merge_queue"]["ruleset_id"], 25840194)
        self.assertTrue(all(entry["app_id"] == 15368 for entry in self.inventory["checks"]))
        self.assertEqual(inventory_problems(self.documents, self.inventory), [])

    def test_ac06_deliberately_renamed_required_job_is_rejected(self):
        for entry in self.inventory["checks"]:
            with self.subTest(context=entry["context"]):
                changed = copy.deepcopy(self.documents)
                changed[entry["workflow"]]["jobs"][entry["job"]]["name"] = "renamed out from under branch protection"
                self.assertIn(f"{entry['context']}: required job renamed", inventory_problems(changed, self.inventory))

    def test_ac06_each_required_event_must_produce_the_exact_inventory_name(self):
        for entry in self.inventory["checks"]:
            document = self.documents[entry["workflow"]]
            for values in event_contexts(document):
                if ignored_edit(values):
                    continue
                github = values["github"]
                with self.subTest(context=entry["context"], event=github["event_name"], action=github["event"]["action"], changes=github["event"]["changes"]):
                    changed = copy.deepcopy(self.documents)
                    changed[entry["workflow"]]["jobs"][entry["job"]]["name"] = (
                        "${{ github.event_name == '" + github["event_name"]
                        + "' && github.event.action == '" + github["event"]["action"]
                        + "' && 'renamed' || '" + entry["context"] + "' }}"
                    )
                    self.assertIn(f"{entry['context']}: required job renamed", inventory_problems(changed, self.inventory))

    def test_ac06_ignored_edits_cannot_report_the_required_name(self):
        for entry in self.inventory["checks"]:
            with self.subTest(context=entry["context"]):
                changed = copy.deepcopy(self.documents)
                changed[entry["workflow"]]["jobs"][entry["job"]]["name"] = entry["context"]
                self.assertIn(f"{entry['context']}: ignored edit can replace the required check", inventory_problems(changed, self.inventory))

    def test_ac06_a_missing_required_producer_is_rejected(self):
        for entry in self.inventory["checks"]:
            with self.subTest(context=entry["context"]):
                changed = copy.deepcopy(self.documents)
                del changed[entry["workflow"]]["jobs"][entry["job"]]
                self.assertIn(f"{entry['context']}: required producer missing", inventory_problems(changed, self.inventory))

    def test_ac06_missing_events_filters_optional_verdicts_and_duplicate_contexts_are_rejected(self):
        for entry in self.inventory["checks"]:
            for mutation in ("event", "paths", "job-if", "step-if", "soft-failure", "duplicate", "conditional-name"):
                with self.subTest(context=entry["context"], mutation=mutation):
                    changed = copy.deepcopy(self.documents)
                    document = changed[entry["workflow"]]
                    job = document["jobs"][entry["job"]]
                    if mutation == "event":
                        document[True].pop("merge_group")
                    elif mutation == "paths":
                        document[True]["pull_request"]["paths"] = ["*.go"]
                    elif mutation == "job-if":
                        job["if"] = "always() && github.event_name != 'merge_group'"
                    elif mutation == "step-if":
                        job["steps"][0]["if"] = "false"
                    elif mutation == "soft-failure":
                        job["continue-on-error"] = True
                    elif mutation == "conditional-name":
                        job["name"] = "${{ github.actor == 'another-owner' && 'renamed' || '" + entry["context"] + "' }}"
                    else:
                        document["jobs"]["imposter"] = {"name": entry["context"]}
                    self.assertTrue(inventory_problems(changed, self.inventory))


class RequiredVerdicts(unittest.TestCase):
    def setUp(self):
        self.documents = workflows()

    def run_verdict(self, filename, job_id, environment):
        job = self.documents[filename]["jobs"][job_id]
        step, = [step for step in job["steps"] if "run" in step]
        result = subprocess.run(
            ["bash", "-c", step["run"]],
            env={**os.environ, **environment}, capture_output=True, text=True, timeout=600,
        )
        return result

    def ci_environment(self, event, *, actor="someone", dispatch_pr="", **results):
        job = self.documents["ci.yml"]["jobs"]["ci"]
        values = context(event=event, actor=actor)
        values["inputs"] = {"pull-request": dispatch_pr}
        statuses = {}
        for dependency in job["needs"]:
            condition = self.documents["ci.yml"]["jobs"][dependency].get("if", "always()")
            statuses[dependency] = "success" if evaluate(condition, values) else "skipped"
        statuses.update(results)
        return {
            "RESULTS": "\n".join(f"{name}={status}" for name, status in statuses.items()),
            "EVENT_NAME": event, "CI_ACTOR": actor, "DISPATCH_PR": dispatch_pr,
        }

    def test_ac06_ci_reports_every_dependency_and_rejects_every_unexpected_skip(self):
        job = self.documents["ci.yml"]["jobs"]["ci"]
        step, = job["steps"]
        self.assertEqual(set(re.findall(r"needs\.([\w-]+)\.result", step["env"]["RESULTS"])), set(job["needs"]))
        for event in ("pull_request", "merge_group"):
            environment = self.ci_environment(event)
            good = self.run_verdict("ci.yml", "ci", environment)
            self.assertEqual(good.returncode, 0, good.stdout + good.stderr)
            for dependency in job["needs"]:
                if f"{dependency}=skipped" in environment["RESULTS"].splitlines():
                    continue
                for status in ("skipped", "failure", "cancelled"):
                    with self.subTest(event=event, dependency=dependency, status=status):
                        bad = self.run_verdict("ci.yml", "ci", self.ci_environment(event, **{dependency: status}))
                        self.assertNotEqual(bad.returncode, 0, bad.stdout + bad.stderr)

    def test_ac06_e2e_cannot_skip_a_required_pr_or_merge_queue_suite_green(self):
        for event in ("pull_request", "merge_group"):
            environment = {
                "PREFLIGHT_RESULT": "success", "CHANGES_RESULT": "success",
                "RUN_E2E": "true" if event == "pull_request" else "false",
                "RUN_DELTA": "true" if event == "merge_group" else "false",
                "E2E_RESULT": "success", "DELTA_RESULT": "success",
                "PAIRS_RESULT": "success", "PROOFS_RESULT": "success", "EVENT_NAME": event,
            }
            good = self.run_verdict("e2e.yml", "e2e", environment)
            self.assertEqual(good.returncode, 0, good.stdout + good.stderr)
            required = ["PREFLIGHT_RESULT", "CHANGES_RESULT"] + (["E2E_RESULT", "PAIRS_RESULT", "PROOFS_RESULT"] if event == "pull_request" else ["DELTA_RESULT"])
            for dependency in required:
                for status in ("skipped", "failure", "cancelled"):
                    with self.subTest(event=event, dependency=dependency, status=status):
                        bad = self.run_verdict("e2e.yml", "e2e", {**environment, dependency: status})
                        self.assertNotEqual(bad.returncode, 0, bad.stdout + bad.stderr)

    def test_ac06_existing_push_and_dispatch_skip_policies_still_pass(self):
        for event, actor, dispatch_pr in (
            ("push", "someone", ""),
            ("push", "github-merge-queue[bot]", ""),
            ("workflow_dispatch", "someone", ""),
            ("workflow_dispatch", "someone", "123"),
        ):
            with self.subTest(event=event, actor=actor, dispatch_pr=dispatch_pr):
                environment = self.ci_environment(event, actor=actor, dispatch_pr=dispatch_pr)
                good = self.run_verdict("ci.yml", "ci", environment)
                self.assertEqual(good.returncode, 0, good.stdout + good.stderr)

    def test_ac06_e2e_requires_explicit_boolean_skip_decisions(self):
        environment = {
            "PREFLIGHT_RESULT": "success", "CHANGES_RESULT": "success",
            "RUN_E2E": "false", "RUN_DELTA": "false",
            "E2E_RESULT": "skipped", "DELTA_RESULT": "skipped",
            "PAIRS_RESULT": "skipped", "PROOFS_RESULT": "skipped", "EVENT_NAME": "pull_request",
        }
        good = self.run_verdict("e2e.yml", "e2e", environment)
        self.assertEqual(good.returncode, 0, good.stdout + good.stderr)
        for decision in ("RUN_E2E", "RUN_DELTA"):
            for malformed in ("", "unknown", "False"):
                with self.subTest(decision=decision, malformed=malformed):
                    bad = self.run_verdict("e2e.yml", "e2e", {**environment, decision: malformed})
                    self.assertNotEqual(bad.returncode, 0, bad.stdout + bad.stderr)


if __name__ == "__main__":
    unittest.main()
