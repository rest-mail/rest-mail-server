"""The fault run's verdicts: a fault counts as caught only in the phase it names."""

import tempfile
import unittest
from pathlib import Path

import faults


def report(directory, cases):
    path = Path(directory) / "examples.xml"
    path.write_text("<testsuites><testsuite>" + "".join(cases) + "</testsuite></testsuites>")
    return path


class Catalogue(unittest.TestCase):
    def test_every_fault_names_a_judged_phase_and_its_tags(self):
        for fault in faults.load():
            self.assertIn(fault["phase"], faults.PHASES)
            self.assertEqual(fault["build_tags"], "faultinject,fault_" + fault["name"])
            self.assertTrue(fault["path"].startswith("/"), fault["name"])

    def test_tags_join_every_fault_once(self):
        tags = faults.all_tags(
            [{"build_tags": "faultinject,fault_a"}, {"build_tags": "faultinject,fault_b"}]
        )
        self.assertEqual(tags, "faultinject,fault_a,fault_b")

    def test_an_unknown_phase_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "faults.yml"
            path.write_text(
                "faults:\n  - {name: a, build_tags: 'faultinject,fault_a', operation: getHealth, path: /api/health, phase: fuzz}\n"
            )
            with self.assertRaises(ValueError):
                faults.load(path)


class Judge(unittest.TestCase):
    def test_a_failed_transaction_of_the_operation_catches_the_fault(self):
        with tempfile.TemporaryDirectory() as directory:
            path = report(
                directory,
                ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"><failure message="body: data is not an object"/></testcase>'],
            )
            passed, why = faults.judge(path, "/api/health", faulted=True)
            self.assertTrue(passed, why)

    def test_a_failure_of_another_operation_does_not(self):
        with tempfile.TemporaryDirectory() as directory:
            path = report(
                directory,
                [
                    '<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"/>',
                    '<testcase name="/api/v1/auth/login &gt; Sign in &gt; 200 &gt; application/json"><failure message="x"/></testcase>',
                ],
            )
            passed, why = faults.judge(path, "/api/health", faulted=True)
            self.assertFalse(passed)
            self.assertIn("not caught", why)

    def test_a_phase_that_never_ran_catches_nothing(self):
        with tempfile.TemporaryDirectory() as directory:
            passed, why = faults.judge(Path(directory) / "examples.xml", "/api/health", faulted=True)
            self.assertFalse(passed)
            self.assertIn("did not run", why)

    def test_the_baseline_must_be_green_for_the_operation(self):
        with tempfile.TemporaryDirectory() as directory:
            green = report(directory, ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"/>'])
            self.assertTrue(faults.judge(green, "/api/health", faulted=False)[0])
            red = report(directory, ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"><error message="e"/></testcase>'])
            passed, why = faults.judge(red, "/api/health", faulted=False)
            self.assertFalse(passed)
            self.assertIn("red without a fault", why)

    def test_a_baseline_that_sent_nothing_is_not_green(self):
        with tempfile.TemporaryDirectory() as directory:
            passed, why = faults.judge(report(directory, []), "/api/health", faulted=False)
            self.assertFalse(passed)
            self.assertIn("sent no transaction", why)

    def test_a_known_gap_passes_uncaught_and_fails_once_caught(self):
        with tempfile.TemporaryDirectory() as directory:
            green = report(directory, ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"/>'])
            passed, why = faults.judge(green, "/api/health", faulted=True, known_gap="#1")
            self.assertTrue(passed, why)
            red = report(directory, ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"><failure/></testcase>'])
            passed, why = faults.judge(red, "/api/health", faulted=True, known_gap="#1")
            self.assertFalse(passed)
            self.assertIn("remove known_gap", why)


if __name__ == "__main__":
    unittest.main()
