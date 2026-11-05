"""Offline gate fixtures; these never invoke Go or run its suite.

That the gate runs at all is proved at the repository root, in
scripts/tests/test_go_imap_gates.py, beside the workflow that runs it.
"""

import importlib.util
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / ".github/scripts/check_coverage.py"
spec = importlib.util.spec_from_file_location("check_coverage", SCRIPT)
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class CoverageGateFixtures(unittest.TestCase):
    def test_threshold_and_upper_bound(self):
        for value in ("95", "95.0", "95.01", "99.9", "100", "100.0"):
            with self.subTest(value=value):
                self.assertEqual(
                    gate.coverage_total(
                        f"example.go:10: function 0.0%\ntotal:\t(statements)\t{value}%\n"
                    ),
                    gate.Decimal(value),
                )

    def test_below_threshold_and_out_of_range_fail(self):
        for value in ("0", "85", "92.4", "94.9", "94.99", "100.01", "101"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                gate.coverage_total(f"total: (statements) {value}%\n")

    def test_missing_duplicate_and_malformed_totals_fail(self):
        for report in (
            "",
            "example.go:10: function 100.0%\n",
            "total: (statements) 95%\ntotal: (statements) 100%\n",
            "total: (statements) 95%\ntotal: broken\n",
            "total: (statements) NaN%\n",
            "total: (statements) Infinity%\n",
            "total: (statements) -1%\n",
            "total: (statements) 9.5e1%\n",
            "total: (statements) 95\n",
            "total: 95%\n",
            "total: (statements) 95% extra\n",
        ):
            with self.subTest(report=report), self.assertRaises(ValueError):
                gate.coverage_total(report)

    def test_command_exit_status(self):
        for report, success in (
            ("total: (statements) 95.0%\n", True),
            ("total: (statements) 94.99%\n", False),
            ("", False),
            ("total: broken\n", False),
        ):
            result = subprocess.run(
                [sys.executable, "-B", str(SCRIPT)],
                input=report,
                text=True,
                capture_output=True,
                check=False,
            )
            with self.subTest(report=report):
                self.assertEqual(result.returncode == 0, success, result.stderr)
                if success:
                    self.assertIn("Statement coverage:", result.stdout)
                else:
                    self.assertTrue(result.stderr)
                    self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
