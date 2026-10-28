"""A fresh verdict removes reports, while preserving fixtures and other phases."""

import tempfile
import unittest
from pathlib import Path

import report_files


class ReportFiles(unittest.TestCase):
    def test_full_reset_removes_reports_and_snapshots_but_keeps_fixtures(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = [
                phase + suffix
                for phase in (
                    "examples",
                    "coverage",
                    "authorization",
                    "fuzz",
                    "browser-sessions",
                )
                for suffix in (".xml", ".html", ".md", ".har", ".json")
            ] + [
                "description-equivalence.json",
                "summary.md",
                "openapi.json",
                "vertrag-run.yml",
                "vertrag-examples.yml",
                "vertrag-probes.yml",
                "vertrag-authorization.yml",
            ]
            for name in old + ["api-key-fixture.json", "operator-notes.txt"]:
                (root / name).write_text("keep only fixtures and notes")
            report_files.reset_reports(root, reset_run=True)
            self.assertEqual(
                {path.name for path in root.iterdir()},
                {"api-key-fixture.json", "operator-notes.txt"},
            )

    def test_authorization_reset_preserves_gate_reports_and_its_saved_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            keep = (
                "examples.xml",
                "coverage.json",
                "fuzz.html",
                "openapi.json",
                "vertrag-run.yml",
            )
            for name in keep + (
                "authorization.xml",
                "authorization.json",
                "description-equivalence.json",
                "summary.md",
            ):
                (root / name).write_text("previous")
            report_files.reset_reports(root, ["authorization"])
            self.assertEqual({p.name for p in root.iterdir()}, set(keep))

    def test_unknown_phase_does_not_remove_the_existing_summary(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "summary.md"
            output.write_text("previous")
            with self.assertRaisesRegex(ValueError, "unknown contract phase"):
                report_files.reset_reports(directory, ["typo"])
            self.assertEqual(output.read_text(), "previous")
