"""Every example of an operation the live run never sends is proven in process
(#1334), as every example that needs setup is (#1237)."""

import importlib.util
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("example_proofs", ROOT / "scripts/example_proofs.py")
example_proofs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(example_proofs)


class ExampleProofs(unittest.TestCase):
    def run_proof_check(self, examples, *proofs):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            examples_path = root / "examples.json"
            examples_path.write_text(json.dumps(examples))
            proof_paths = []
            for index, proof in enumerate(proofs):
                path = root / f"proofs-{index}.json"
                path.write_text(json.dumps(proof))
                proof_paths.append(str(path))
            return subprocess.run(
                [sys.executable, str(ROOT / "scripts/example_proofs.py"),
                 str(examples_path), *proof_paths],
                capture_output=True, text=True, check=False,
            )

    def test_cli_rejects_every_unproven_requirement_family(self):
        for requires in (
            {"principal": "anonymous"},
            {"body": "over_cap"},
            {"limit": "rate"},
            {"fault": "database"},
            {"state": "two_factor_session"},
            {"state": "two_factor_session", "fault": "database"},
        ):
            with self.subTest(requires=requires):
                result = self.run_proof_check({"operation": {"refused": {"requires": requires}}}, {})
                needs = ", ".join(f"{kind} {value}" for kind, value in sorted(requires.items()))
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(
                    result.stdout,
                    f"operation/refused requires {needs}, and no in-process test proved it\n"
                    "0 of 1 examples that need setup are proven in process\n",
                )

    def test_cli_requires_the_exact_operation_and_pair(self):
        examples = {"operation": {
            "first": {"requires": {"fault": "database"}},
            "second": {"requires": {"body": "over_cap"}},
        }}
        result = self.run_proof_check(
            examples, {"operation": ["first", "another"], "another_operation": ["second"]},
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(
            result.stdout,
            "operation/second requires body over_cap, and no in-process test proved it\n"
            "1 of 2 examples that need setup are proven in process\n",
        )

    def test_cli_unions_runner_proofs_without_double_counting(self):
        examples = {"operation": {
            "first": {"requires": {"fault": "database"}},
            "second": {"requires": {"body": "over_cap"}},
            "live": {},
        }}
        result = self.run_proof_check(
            examples, {"operation": ["first"]}, {"operation": ["first", "second"]},
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "2 of 2 examples that need setup are proven in process\n")

    def test_every_pass_sends_what_the_primary_run_leaves_out(self):
        # Each operation the primary run leaves out is sent by the teardown
        # pass, so none goes unsent.
        self.assertEqual(example_proofs.never_sent(ROOT), frozenset())

    def test_an_unsent_operations_examples_need_proofs(self):
        root = Path(tempfile.mkdtemp())
        (root / "tests/contract").mkdir(parents=True)
        (root / "vertrag.yml").write_text("a: 1\nexclude-operation-id:\n  - sentByAPass\n  - neverSent\n\nb: 2\n")
        (root / "tests/contract/teardown.py").write_text('OPERATIONS = ("sentByAPass",)\n')
        unsent = example_proofs.never_sent(root)
        self.assertEqual(unsent, frozenset({"neverSent"}))
        examples = {"neverSent": {"found": {}, "gone": {}}, "live": {"found": {}}}
        self.assertEqual(example_proofs.unproven(examples, {"neverSent": ["found"]}, unsent),
                         [("neverSent", "gone", {"sent": "never"})])


ANONYMOUS = {"principal": "anonymous"}
LIVE_PAIR = {"requires": ANONYMOUS, "response": {"status": 401}}
LIVE_EXAMPLES = {"getThing": {"refused": LIVE_PAIR}}
LIVE_VERDICT = {
    "summary": {"passed": True},
    "results": [{"status": "pass", "expected": "401", "actual": "401"}],
}


def run_proofs(root, examples, proofs):
    examples_path = root / "plain-examples.json"
    examples_path.write_text(json.dumps(examples))
    proofs_path = root / "plain-proofs.json"
    proofs_path.write_text(json.dumps(proofs))
    return subprocess.run(
        [sys.executable, str(ROOT / "scripts/example_proofs.py"), str(examples_path), str(proofs_path)],
        capture_output=True, text=True, check=False,
    )


class LiveProofs(unittest.TestCase):
    """The union counts a live proof only when the runner really proved it (#1237)."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.live = self.root / "live"
        self.run_dir = self.live / "anonymous"
        self.run_dir.mkdir(parents=True)
        self.write("proofs", {"getThing": ["refused"]})
        self.write("unproven", [])
        self.write("inventory", {"requires": ANONYMOUS, "proven": 1, "unproven": 0})
        self.write("getThing-refused", LIVE_VERDICT)

    def write(self, name, value):
        (self.run_dir / f"anonymous-{name}.json").write_text(json.dumps(value))

    def judge(self, examples=None, in_process=None):
        examples_path = self.root / "examples.json"
        examples_path.write_text(json.dumps(LIVE_EXAMPLES if examples is None else examples))
        proofs_path = self.root / "in-process.json"
        proofs_path.write_text(json.dumps(in_process or {}))
        return subprocess.run(
            [sys.executable, str(ROOT / "scripts/example_proofs.py"), "--live", str(self.live),
             str(examples_path), str(proofs_path)],
            capture_output=True, text=True, check=False,
        )

    def assert_refused(self, reason):
        result = self.judge()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn(reason, result.stdout)
        self.assertIn(
            "getThing/refused requires principal anonymous, and no runner proved it", result.stdout
        )
        self.assertIn("0 of 1 examples that need setup are proven, 0 of them only live", result.stdout)

    def test_a_live_proof_counts_as_proven(self):
        result = self.judge()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("1 of 1 examples that need setup are proven, 1 of them only live", result.stdout)

    def test_the_same_pair_without_live_results_is_unproven(self):
        result = run_proofs(self.root, LIVE_EXAMPLES, {})
        self.assertEqual(result.returncode, 1)
        self.assertIn("no in-process test proved it", result.stdout)

    def test_a_failed_exchange_does_not_count(self):
        self.write("getThing-refused", {
            "summary": {"passed": False},
            "results": [{"status": "fail", "expected": "401", "actual": "500"}],
        })
        self.assert_refused("getThing/refused has no passing verdict")

    def test_a_skipped_exchange_does_not_count(self):
        self.write("getThing-refused", {
            "summary": {"passed": True},
            "results": [{"status": "skip", "expected": "401", "actual": ""}],
        })
        self.assert_refused("getThing/refused has no passing verdict")

    def test_a_tool_error_with_no_verdict_does_not_count(self):
        (self.run_dir / "anonymous-getThing-refused.json").unlink()
        self.assert_refused("getThing/refused has no verdict")

    def test_an_empty_verdict_does_not_count(self):
        self.write("getThing-refused", {})
        self.assert_refused("getThing/refused has no passing verdict")

    def test_a_missing_result_does_not_count(self):
        (self.run_dir / "anonymous-proofs.json").unlink()
        self.assert_refused("live runner anonymous/anonymous: no readable result")

    def test_a_missing_runner_directory_does_not_count(self):
        shutil.rmtree(self.run_dir)
        self.assert_refused("live runner anonymous/anonymous: no readable result")

    def test_an_empty_result_does_not_count(self):
        self.write("proofs", {})
        self.assert_refused("live runner anonymous/anonymous: proved nothing")

    def test_a_malformed_result_does_not_count(self):
        (self.run_dir / "anonymous-proofs.json").write_text("{not json")
        self.assert_refused("no readable result")
        self.write("proofs", {"getThing": "refused"})
        self.assert_refused("its proof list is malformed")

    def test_a_run_that_did_not_finish_does_not_count(self):
        (self.run_dir / "anonymous-inventory.json").unlink()
        self.assert_refused("did not finish")

    def test_an_inventory_that_disagrees_does_not_count(self):
        self.write("inventory", {"requires": ANONYMOUS, "proven": 2, "unproven": 0})
        self.assert_refused("its inventory disagrees with its proof list")

    def test_a_pair_also_listed_as_unproven_does_not_count(self):
        self.write("unproven", [{"operation": "getThing", "example": "refused"}])
        self.assert_refused("getThing/refused is also listed as unproven")

    def test_a_run_that_provided_something_else_does_not_count(self):
        self.write("inventory", {"requires": {"body": "over_cap"}, "proven": 1, "unproven": 0})
        self.assert_refused("needs something other than what this run provided")

    def test_a_failed_exchange_elsewhere_keeps_the_runners_real_proofs(self):
        examples = {"getThing": {
            "refused": LIVE_PAIR,
            "other": {"requires": ANONYMOUS, "response": {"status": 401}},
        }}
        self.write("unproven", [{"operation": "getThing", "example": "other"}])
        result = self.judge(examples)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("getThing/other requires principal anonymous, and no runner proved it", result.stdout)
        self.assertIn("1 of 2 examples that need setup are proven, 1 of them only live", result.stdout)

    def test_an_in_process_proof_stands_when_the_live_runner_fails(self):
        (self.run_dir / "anonymous-proofs.json").unlink()
        result = self.judge(in_process={"getThing": ["refused"]})
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("no readable result", result.stdout)
        self.assertIn("1 of 1 examples that need setup are proven, 0 of them only live", result.stdout)

    def test_a_principal_cell_counts_only_the_pair_it_sends(self):
        cell = self.live / "principals"
        cell.mkdir()
        requires = {"principal": "without_capability"}
        examples = {"getDomain": {"outside_domain": {"requires": requires, "response": {"status": 403}}},
                    "getThing": {"forged": {"requires": requires, "response": {"status": 403}}}}
        verdict = {"summary": {"passed": True},
                   "results": [{"status": "pass", "expected": "403", "actual": "403"}]}
        for name, value in (
            ("proofs", {"getDomain": ["outside_domain"], "getThing": ["forged"]}),
            ("unproven", []),
            ("inventory", {"requires": requires, "proven": 2, "unproven": 0}),
            ("getDomain-outside_domain", verdict),
            ("getThing-forged", verdict),
        ):
            (cell / f"ordinary-{name}.json").write_text(json.dumps(value))
        (self.run_dir / "anonymous-proofs.json").unlink()
        result = self.judge(examples)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("getThing/forged is not what this run sends", result.stdout)
        self.assertIn("1 of 2 examples that need setup are proven, 1 of them only live", result.stdout)

    def test_the_cells_are_those_the_principal_runner_sends(self):
        source = (ROOT / "tests/contract/live_principals.py").read_text()
        for label, (operation, name) in example_proofs.PRINCIPAL_CELLS.items():
            with self.subTest(label=label):
                self.assertRegex(source, rf'"{label}",\s+"{operation}",\s+"{name}"')


class DeferLive(unittest.TestCase):
    def judge(self, examples):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            examples_path = root / "examples.json"
            examples_path.write_text(json.dumps(examples))
            proofs_path = root / "proofs.json"
            proofs_path.write_text("{}")
            return subprocess.run(
                [sys.executable, str(ROOT / "scripts/example_proofs.py"), "--defer-live",
                 str(examples_path), str(proofs_path)],
                capture_output=True, text=True, check=False,
            )

    def test_only_what_a_live_runner_selects_is_left_to_it(self):
        for requires in (ANONYMOUS, {"body": "over_cap"}):
            with self.subTest(requires=requires):
                result = self.judge({"op": {"pair": {"requires": requires}}})
                self.assertEqual(result.returncode, 0, result.stdout)
                self.assertIn("op/pair: no in-process test proved it; left to the live proofs", result.stdout)
                self.assertIn("0 of 1 examples that need setup are proven in process", result.stdout)
        cell = {"getDomain": {"outside_domain": {"requires": {"principal": "without_capability"}}}}
        self.assertEqual(self.judge(cell).returncode, 0)

    def test_every_other_pair_still_fails_here(self):
        for requires in (
            {"fault": "database"},
            {"limit": "rate"},
            {"principal": "without_capability"},
            {"principal": "anonymous", "fault": "database"},
        ):
            with self.subTest(requires=requires):
                result = self.judge({"op": {"pair": {"requires": requires}}})
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertIn("and no in-process test proved it", result.stdout)


class CIProofFetch(unittest.TestCase):
    """scripts/ci_example_proofs.sh, against a stand-in for the gh command."""

    def fetch(self, report):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gh = root / "gh"
            gh.write_text(
                "#!/bin/sh\n"
                'echo "$*" >> "$GH_CALLS"\n'
                'case "$1 $2" in\n'
                f"'run list') echo {'7' if report != 'no-run' else ''} ;;\n"
                f"'run view') echo '{report}' ;;\n"
                "'run download') exit 0 ;;\n"
                "esac\n"
            )
            gh.chmod(0o755)
            calls = root / "calls"
            result = subprocess.run(
                ["bash", str(ROOT / "scripts/ci_example_proofs.sh"), "abc", "merge_group", str(root / "out")],
                capture_output=True, text=True, check=False,
                env={**os.environ, "PATH": f"{root}:{os.environ['PATH']}", "GH_CALLS": str(calls),
                     "CI_PROOFS_WAIT_SECONDS": "0", "CI_PROOFS_POLL_SECONDS": "0"},
            )
            return result, calls.read_text() if calls.exists() else ""

    def test_downloads_once_the_test_report_passed(self):
        result, calls = self.fetch("completed success")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("run list --workflow ci.yml --commit abc --event merge_group", calls)
        self.assertIn("run download 7 --pattern example-proofs-*", calls)

    def test_refuses_anything_else(self):
        for report in ("completed failure", "completed skipped", "completed cancelled",
                       "in_progress null", "", "no-run"):
            with self.subTest(report=report):
                result, calls = self.fetch(report)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("::error::", result.stderr)
                self.assertNotIn("run download", calls)


class Workflows(unittest.TestCase):
    """The live proofs reach the union; only the merge queue proves it runs."""

    def setUp(self):
        workflows = ROOT / ".github/workflows"
        self.suite = yaml.safe_load((workflows / "e2e-suite.yml").read_text())["jobs"]["suite"]
        self.e2e = yaml.safe_load((workflows / "e2e.yml").read_text())["jobs"]
        self.ci = yaml.safe_load((workflows / "ci.yml").read_text())["jobs"]

    def test_the_live_stage_runs_after_the_gate_and_reports(self):
        names = [step.get("name", "") for step in self.suite["steps"]]
        stage = next(s for s in self.suite["steps"] if s.get("run") == "chore test:contract-live-proofs")
        gate = next(s for s in self.suite["steps"] if s.get("id") == "contract")
        self.assertEqual(stage["if"], "inputs.contract-phases == 'gate'")
        # A report, while the owner has not said a failing runner should turn the run red.
        self.assertIs(stage["continue-on-error"], True)
        self.assertIn("timeout-minutes", stage)
        self.assertLess(names.index(gate["name"]), names.index(stage["name"]))
        # The limits suite spends the API's buckets; the teardown clears every principal.
        self.assertLess(names.index(stage["name"]), names.index("Limits suite"))
        upload = next(s for s in self.suite["steps"] if s.get("with", {}).get("name") == "live-example-proofs")
        self.assertEqual(upload["if"], "always() && inputs.contract-phases == 'gate'")
        paths = upload["with"]["path"].splitlines()
        self.assertEqual(paths[0], stage["env"]["CONTRACT_LIVE_PROOFS"])
        self.assertEqual(paths[1], "!" + paths[0] + "/**/vertrag.yml")

    def test_the_union_gates_the_required_check_and_reads_the_live_results(self):
        union = self.e2e["e2e-proofs"]
        self.assertEqual(set(union["needs"]), {"changes", "e2e-run"})
        self.assertEqual(union["permissions"]["actions"], "read")
        download = next(s for s in union["steps"] if s.get("with", {}).get("name") == "live-example-proofs")
        # No artifact counts for nothing; the judgement below says so.
        self.assertIs(download["continue-on-error"], True)
        judge = next(s for s in union["steps"] if "scripts/example_proofs.py" in s.get("run", ""))
        self.assertIn('--live "$RUNNER_TEMP/live-proofs"', judge["run"])
        self.assertIn("-lt 4", judge["run"])
        self.assertNotIn("continue-on-error", judge)
        gate = self.e2e["e2e"]
        self.assertIn("e2e-proofs", gate["needs"])
        self.assertTrue(gate["if"].startswith("always()"), gate["if"])
        self.assertIn("PROOFS_RESULT", gate["steps"][0]["env"])
        self.assertIn('"$PROOFS_RESULT" != "success"', gate["steps"][0]["run"])

    def test_ci_defers_only_with_the_union_behind_it(self):
        report = self.ci["test-report"]["steps"]
        judge = next(s for s in report if "scripts/example_proofs.py" in s.get("run", ""))
        self.assertIn("--defer-live", judge["run"])
        self.assertNotIn("--live ", judge["run"])

    def test_the_live_stage_is_the_chore_a_person_runs(self):
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        command = tasks["test:contract-live-proofs"]["cmds"][0]
        self.assertIn("--api-keys", command)
        self.assertIn("tests/contract/live_proofs.sh", command)
        self.assertTrue(os.access(ROOT / "tests/contract/live_proofs.sh", os.X_OK))
        self.assertTrue(os.access(ROOT / "scripts/ci_example_proofs.sh", os.X_OK))

    def test_the_live_stage_clears_old_results_first(self):
        script = (ROOT / "tests/contract/live_proofs.sh").read_text()
        self.assertLess(script.index('rm -rf "$out"'), script.index("run_live anonymous"))


if __name__ == "__main__":
    unittest.main()
