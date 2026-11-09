"""The local gate runs the pairs oracle whenever the fault catalogue or its judge changes (#2066).

The predecessor's patch for get_quota_may_reach_skipped matched four handlers, so
git apply broke listFolders, the witness pair stayed green, and only the nightly
run reported the fault uncaught. The pairs oracle needs no topology, so the gate
runs it itself, and a fault it misses fails the gate here.
"""

import contextlib
import importlib.util
import io
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("ci_local", ROOT / "scripts/ci_local.py")
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)

PAIRS_COMMAND = ["tests/contract/faults.py", "pairs"]


def pairs_command(local):
    return [local.python, "-B", *PAIRS_COMMAND]


class PairsOracleSelection(unittest.TestCase):
    def test_catalogue_and_judge_changes_require_the_pairs_oracle(self):
        for name in (
            "tests/contract/faults.yml",
            "tests/contract/faults.py",
            "internal/faultinject/catalogue_test.go",
            "internal/faultinject/new_guard_test.go",
        ):
            with self.subTest(name=name):
                self.assertTrue(gate.pairs_oracle_changed([name]))

    def test_other_changes_do_not_require_the_pairs_oracle(self):
        for name in (
            "tests/contract/README.md",
            "tests/contract/secret_scan.py",
            "internal/api/handlers/messages.go",
            "docs/faults.md",
        ):
            with self.subTest(name=name):
                self.assertFalse(gate.pairs_oracle_changed([name]))

    def test_the_step_is_not_required_for_an_unrelated_change(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.files = ["docs/faults.md"]
            with patch.object(local, "run") as commands:
                detail = local.fault_pairs()
        self.assertTrue(detail.startswith("NOT REQUIRED"), detail)
        commands.assert_not_called()

    def test_all_mode_forces_the_pairs_oracle(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), True)
            local.files = ["docs/faults.md"]
            with (
                patch.object(local, "run", return_value="/bin/vertrag\n") as commands,
                contextlib.redirect_stdout(io.StringIO()),
            ):
                self.assertIsNone(local.fault_pairs())
        self.assertIn(pairs_command(local), [call.args[0] for call in commands.call_args_list])


class PairsOracleStep(unittest.TestCase):
    def test_the_step_runs_the_oracle_with_its_vertrag_binary(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.files = ["tests/contract/faults.yml"]
            with (
                patch.object(local, "run", return_value="/bin/vertrag\n") as commands,
                contextlib.redirect_stdout(io.StringIO()),
            ):
                self.assertIsNone(local.fault_pairs())
        self.assertEqual(local.env["RESTMAIL_VERTRAG_BINARY"], "/bin/vertrag")
        self.assertEqual(commands.call_args_list[-1].args[0], pairs_command(local))

    def test_a_fault_the_oracle_does_not_catch_fails_the_gate(self):
        def run(args, **kwargs):
            if args[-1] == "pairs":
                raise subprocess.CalledProcessError(1, args)
            return "/bin/vertrag\n"

        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.files = ["tests/contract/faults.yml"]
            with (
                patch.object(local, "run", side_effect=run),
                contextlib.redirect_stdout(io.StringIO()),
            ):
                code = gate.run_steps([("Contract pairs", local.fault_pairs)])
        self.assertEqual(code, gate.EXIT_FAILED)

    def test_the_step_is_in_the_gate_after_the_fault_switch(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            names = [name for name, _ in local.steps()]
        self.assertIn("Contract pairs", gate.STEP_NAMES)
        self.assertEqual(names, list(gate.STEP_NAMES))
        self.assertEqual(names.index("Contract pairs"), names.index("Contract fault switch") + 1)


if __name__ == "__main__":
    unittest.main()
