"""The contract run's helpers: each phase's configuration and the summary (#627)."""

import os
from pathlib import Path
import tempfile
import unittest

import reports
import summary

ROOT = Path(__file__).resolve().parents[2]


class Reports(unittest.TestCase):
    def config(self, **kw):
        base = {"spec": "./openapi.yaml", "hookfiles": "tests/contract/hooks.py", "endpoint": "http://10.99.0.20:8080",
                "surface": {"require": 100}, "reporter": ["cli", "junit"], "output": ["", "x.xml"]}
        return reports.configure(base, ROOT, "/tmp/run/examples", **kw)

    def test_each_phase_writes_junit_html_and_markdown(self):
        config = self.config()
        self.assertEqual(config["reporter"], ["cli", "junit", "html", "markdown"])
        self.assertEqual(config["output"], ["", "/tmp/run/examples.xml", "/tmp/run/examples.html", "/tmp/run/examples.md"])
        self.assertEqual(config["spec"], str(ROOT / "openapi.yaml"))
        self.assertEqual(config["hookfiles"], str(ROOT / "tests/contract/hooks.py"))
        self.assertIn("surface", config)
        self.assertEqual(config["endpoint"], "http://10.99.0.20:8080")

    def test_an_endpoint_and_a_narrowed_run(self):
        config = self.config(narrowed=True, endpoint="http://localhost:8080")
        self.assertNotIn("surface", config)
        self.assertEqual(config["endpoint"], "http://localhost:8080")


class Summary(unittest.TestCase):
    def test_one_line_per_phase_that_ran(self):
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, "examples.xml").write_text(
                '<testsuites><testsuite><testcase name="a"/><testcase name="b"><failure/></testcase>'
                '<testcase name="c"><skipped/></testcase></testsuite></testsuites>')
            Path(directory, "authorization.xml").write_text('<testsuite><testcase name="a"/></testsuite>')
            table = summary.summary(directory)
            self.assertIn("| examples | 3 | 1 | 1 | 1 |", table)
            self.assertIn("| authorization (report only) | 1 | 1 | 0 | 0 |", table)
            self.assertNotIn("coverage", table)
            summary.main([directory])
            self.assertTrue(Path(directory, "summary.md").read_text().startswith("# Contract run"))

    def test_chores_and_ci_run_the_same_script(self):
        chores = (ROOT / "chores.yml").read_text()
        workflow = (ROOT / ".github/workflows/e2e-suite.yml").read_text()
        for phase in ("gate", "authorization", "fuzz"):
            self.assertIn("tests/contract/run.sh " + phase, workflow)
            self.assertIn("tests/contract/run.sh " + phase, chores)
        self.assertTrue(os.access(ROOT / "tests/contract/run.sh", os.X_OK))


if __name__ == "__main__":
    unittest.main()
