"""The merge queue's cheap E2E check (#2113): wiring in e2e.yml and the stage subset in e2e-suite.yml."""

import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

import yaml

from test_stacked_pr_workflows import blocks, shell_of

ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github/workflows"
ALL_STAGES = re.findall(r't\.Run\("(Stage\d+_\w+)"', (ROOT / "tests/e2e/suite_test.go").read_text())


def suite_jobs():
    return blocks(blocks((WORKFLOWS / "e2e-suite.yml").read_text(), 0)["jobs"], 2)


class MergeQueueDelta(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.e2e = yaml.safe_load((WORKFLOWS / "e2e.yml").read_text())
        cls.suite = yaml.safe_load((WORKFLOWS / "e2e-suite.yml").read_text())

    def test_the_suite_still_has_fifteen_stages(self):
        self.assertEqual(len(ALL_STAGES), 15)

    def test_the_delta_runs_the_suite_with_no_contract_phase_and_a_prefix_of_the_stages(self):
        job = self.e2e["jobs"]["e2e-delta"]
        self.assertEqual(job["uses"], "./.github/workflows/e2e-suite.yml")
        self.assertEqual(job["with"]["contract-phases"], "none")
        stages = job["with"]["stages"].split()
        self.assertGreater(len(stages), 0)
        self.assertLess(len(stages), len(ALL_STAGES))
        # A stage reads what the stages before it left, so the subset is a prefix.
        self.assertEqual(stages, ALL_STAGES[:len(stages)])

    def test_the_suite_accepts_the_inputs_the_delta_passes(self):
        inputs = self.suite[True]["workflow_call"]["inputs"]
        self.assertEqual(inputs["stages"]["default"], "")
        self.assertIn('"none"', inputs["contract-phases"]["description"])
        for name in self.e2e["jobs"]["e2e-delta"]["with"]:
            self.assertIn(name, inputs)

    def test_with_no_contract_phase_only_the_topology_its_stages_and_its_logs_run(self):
        steps = self.suite["jobs"]["suite"]["steps"]

        def runs(step):
            condition = step.get("if", "True")
            for phase in ("gate", "pairs", "reports", "live", "fuzz", "faults"):
                condition = condition.replace(f"inputs.contract-phases == '{phase}'", "False")
                condition = condition.replace(f"inputs.contract-phases != '{phase}'", "True")
            condition = condition.replace("inputs.contract-phases != 'none'", "False")
            condition = condition.replace("always()", "True").replace("&&", " and ").replace("||", " or ")
            condition = re.sub(r"steps\.[\w-]+\.outcome == 'failure'", "False", condition)
            return eval(condition, {"__builtins__": {}}, {"True": True, "False": False})

        ran = [step["name"] for step in steps if runs(step)]
        self.assertIn("Bring up e2e topology", ran)
        self.assertIn("Run e2e stages", ran)
        self.assertIn("Assert stages executed (guard against silent skips)", ran)
        self.assertIn("Tear down e2e topology", ran)
        for name in ran:
            self.assertNotRegex(name, r"(?i)contract|vertrag|limits|imapsync|live proofs")
        # The isolation proof runs only beside the gating phases.
        self.assertEqual(self.suite["jobs"]["js-filter-isolation"]["if"], "inputs.contract-phases == 'gate'")

    def test_the_gate_waits_for_the_delta_and_reads_its_result(self):
        gate = self.e2e["jobs"]["e2e"]
        self.assertIn("e2e-delta", gate["needs"])
        env = gate["steps"][0]["env"]
        self.assertEqual(env["DELTA_RESULT"], "${{ needs.e2e-delta.result }}")
        self.assertEqual(env["RUN_DELTA"], "${{ needs.changes.outputs.run_delta }}")
        self.assertEqual(self.e2e["jobs"]["changes"]["outputs"]["run_delta"], "${{ steps.classify.outputs.run_delta }}")

    def verdict(self, **overrides):
        script = shell_of(blocks(blocks((WORKFLOWS / "e2e.yml").read_text(), 0)["jobs"], 2)["e2e"], "Verify e2e outcome")
        env = dict(os.environ, PREFLIGHT_RESULT="success", CHANGES_RESULT="success", RUN_E2E="false", RUN_DELTA="true",
                   E2E_RESULT="skipped", DELTA_RESULT="success", PAIRS_RESULT="skipped", PROOFS_RESULT="skipped",
                   EVENT_NAME="merge_group")
        env.update(overrides)
        return subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, timeout=600)

    def test_the_required_context_passes_on_a_merge_group_only_when_the_delta_passed(self):
        for delta, expected in (("success", 0), ("failure", 1), ("cancelled", 1), ("skipped", 1), ("", 1)):
            with self.subTest(delta=delta):
                result = self.verdict(DELTA_RESULT=delta)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        self.assertEqual(self.verdict(CHANGES_RESULT="failure").returncode, 1)
        self.assertEqual(self.verdict(PREFLIGHT_RESULT="failure").returncode, 1)

    def test_a_delta_cannot_stand_in_for_a_full_run_the_classifier_asked_for(self):
        result = self.verdict(RUN_E2E="true", E2E_RESULT="failure", PAIRS_RESULT="success", PROOFS_RESULT="success")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_a_docs_only_merge_group_still_posts_success(self):
        result = self.verdict(RUN_DELTA="false", DELTA_RESULT="skipped")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


class StageSubset(unittest.TestCase):
    def run_stages(self, stages):
        script = shell_of(suite_jobs()["suite"], "Run e2e stages")
        with tempfile.TemporaryDirectory() as directory:
            calls = Path(directory) / "calls"
            chore = Path(directory) / "chore"
            # A stand-in for chore: it hands its arguments to a shell, as chore does.
            chore.write_text(f'#!/bin/bash\nbash -c "printf \'[%s]\\\\n\' $*" >> "{calls}"\necho "--- PASS: TestStages/Stage1_Infrastructure (0.01s)"\n')
            chore.chmod(0o755)
            env = dict(os.environ, PATH=f"{directory}:{os.environ['PATH']}", STAGES=stages, RUNNER_TEMP=directory)
            result = subprocess.run(["bash", "-eo", "pipefail", "-c", script], env=env, capture_output=True, text=True, timeout=600)
            return result, calls.read_text().splitlines() if calls.exists() else []

    def test_named_stages_become_one_go_test_run_pattern(self):
        result, calls = self.run_stages("Stage1_Infrastructure Stage2_TraditionalMail")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(calls, ["[test:e2e]", "[--]", "[-run]", "[^TestStages$/^(Stage1_Infrastructure|Stage2_TraditionalMail)$]"])

    def test_no_stages_runs_the_whole_suite(self):
        result, calls = self.run_stages("")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(calls, ["[test:e2e]"])

    def test_anything_but_a_stage_name_is_refused_before_chore_runs(self):
        for stages in ("Stage1_Infrastructure;id", "Stage1_Infrastructure $(id)", "TestLimits", "Stage1_Infra|Stage2"):
            with self.subTest(stages=stages):
                result, calls = self.run_stages(stages)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertEqual(calls, [])

    def assert_stages(self, stages, passed, skipped=()):
        script = shell_of(suite_jobs()["suite"], "Assert stages executed (guard against silent skips)")
        with tempfile.TemporaryDirectory() as directory:
            lines = [f"--- PASS: TestStages/{stage} (1.00s)" for stage in passed]
            lines += [f"--- SKIP: TestStages/{stage} (0.00s)" for stage in skipped]
            (Path(directory) / "e2e.log").write_text("\n".join(lines) + "\n")
            env = dict(os.environ, STAGES=stages, RUNNER_TEMP=directory)
            return subprocess.run(["bash", "-eo", "pipefail", "-c", script], env=env, capture_output=True, text=True, timeout=600)

    def test_the_named_stages_must_each_pass_and_nothing_else_may_run(self):
        named = "Stage1_Infrastructure Stage2_TraditionalMail"
        for passed, skipped, expected in (
            (["Stage1_Infrastructure", "Stage2_TraditionalMail"], [], 0),
            (["Stage1_Infrastructure"], [], 1),
            ([], [], 1),
            (["Stage1_Infrastructure", "Stage2_TraditionalMail", "Stage3_GatewayInbound"], [], 1),
            (["Stage1_Infrastructure"], ["Stage2_TraditionalMail"], 1),
        ):
            with self.subTest(passed=passed, skipped=skipped):
                result = self.assert_stages(named, passed, skipped)
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)

    def test_the_whole_suite_still_needs_all_fifteen(self):
        self.assertEqual(self.assert_stages("", ALL_STAGES).returncode, 0)
        self.assertEqual(self.assert_stages("", ALL_STAGES[:14]).returncode, 1)
        self.assertEqual(self.assert_stages("", ALL_STAGES[:14], ALL_STAGES[14:]).returncode, 1)


if __name__ == "__main__":
    unittest.main()
