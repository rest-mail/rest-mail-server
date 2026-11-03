"""Offline gate and workflow fixtures; these never invoke Go or run its suite."""

import importlib.util
import os
import re
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / ".github/scripts/check_coverage.py"
WORKFLOW = (ROOT / ".github/workflows/ci.yml").read_text()
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


class WorkflowFixtures(unittest.TestCase):
    def test_race_coverage_includes_external_wire_proofs(self):
        self.assertIn(
            "run: go test -race -count=1 -covermode=atomic "
            "-coverpkg=./... -coverprofile=coverage.out ./...\n",
            WORKFLOW,
        )
        self.assertLess(
            WORKFLOW.index("-coverprofile=coverage.out"),
            WORKFLOW.index("go tool cover -func=coverage.out"),
        )
        self.assertNotIn("continue-on-error", WORKFLOW)
        self.assertEqual(WORKFLOW.count("name: Build & Test"), 1)

    def test_gate_is_required_and_pipeline_fails_closed(self):
        block = WORKFLOW.split(
            "      - name: Require at least 95% statement coverage\n", 1
        )[1]
        block = block.split("      - name: Upload coverage evidence", 1)[0]
        self.assertEqual(
            block.strip(),
            """shell: bash
        run: |
          set -euo pipefail
          go tool cover -func=coverage.out | tee coverage.txt | python3 -B .github/scripts/check_coverage.py""",
        )
        # A failed coverage producer must fail even if its output looks valid.
        command = "(printf 'total: (statements) 100.0%%\\n'; exit 1) | "
        result = subprocess.run(
            ["bash", "-euo", "pipefail", "-c", command + '"$PYTHON" -B "$GATE"'],
            env={**os.environ, "PYTHON": sys.executable, "GATE": str(SCRIPT)},
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertNotEqual(result.returncode, 0)

    def test_failed_gate_keeps_coverage_evidence(self):
        self.assertIn(
            """      - name: Upload coverage evidence
        if: ${{ !cancelled() }}
        uses: actions/upload-artifact@v4
        with:
          name: statement-coverage
          path: |
            coverage.out
            coverage.txt
          if-no-files-found: ignore
""",
            WORKFLOW,
        )

    def test_fixtures_and_checklist_still_run(self):
        self.assertIn(
            "run: python3 -B -m unittest discover -s .github/tests -v\n", WORKFLOW
        )
        self.assertIn("run: go run ./internal/checklist pkg/imap/rfctest\n", WORKFLOW)

    def test_execution_bounded_fuzz_inventory_is_preserved(self):
        # Each target must run from the package directory that declares it.
        actual = re.findall(
            r"run: go test -run=(?:\^\$|'\^\$') "
            r"-fuzz=(?:\^(Fuzz\w+)\$|'\^(Fuzz\w+)\$') "
            r"-fuzztime=500000x \./(\S+)\s*$",
            WORKFLOW,
            re.MULTILINE,
        )
        targets = [(a or b, pkg) for a, b, pkg in actual]
        declared = []
        for path in ROOT.rglob("*_test.go"):
            pkg = path.parent.relative_to(ROOT).as_posix()
            declared.extend(
                (name, pkg)
                for name in re.findall(
                    r"^func (Fuzz\w+)\(\w+ \*testing\.F\)",
                    path.read_text(),
                    re.MULTILINE,
                )
            )
        self.assertEqual(len(targets), 9)
        self.assertCountEqual(targets, declared)


if __name__ == "__main__":
    unittest.main()
