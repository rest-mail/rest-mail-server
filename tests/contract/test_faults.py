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

    def test_tags_join_every_fault_once(self):
        tags = faults.all_tags(
            [{"build_tags": "faultinject,fault_a"}, {"build_tags": "faultinject,fault_b"}]
        )
        self.assertEqual(tags, "faultinject,fault_a,fault_b")

    def test_an_unknown_phase_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "faults.yml"
            path.write_text(
                "faults:\n  - {name: a, build_tags: 'faultinject,fault_a', operation: getHealth, phase: fuzz}\n"
            )
            with self.assertRaises(ValueError):
                faults.load(path)


class Judge(unittest.TestCase):
    def test_a_failed_transaction_of_the_operation_catches_the_fault(self):
        with tempfile.TemporaryDirectory() as directory:
            path = report(
                directory,
                ['<testcase name="getHealth 200"><failure message="body: data is not an object"/></testcase>'],
            )
            passed, why = faults.judge(path, "getHealth", faulted=True)
            self.assertTrue(passed, why)

    def test_a_failure_of_another_operation_does_not(self):
        with tempfile.TemporaryDirectory() as directory:
            path = report(
                directory,
                [
                    '<testcase name="getHealth 200"/>',
                    '<testcase name="login 200"><failure message="x"/></testcase>',
                ],
            )
            passed, why = faults.judge(path, "getHealth", faulted=True)
            self.assertFalse(passed)
            self.assertIn("not caught", why)

    def test_a_phase_that_never_ran_catches_nothing(self):
        with tempfile.TemporaryDirectory() as directory:
            passed, why = faults.judge(Path(directory) / "examples.xml", "getHealth", faulted=True)
            self.assertFalse(passed)
            self.assertIn("did not run", why)

    def test_the_baseline_must_be_green_for_the_operation(self):
        with tempfile.TemporaryDirectory() as directory:
            green = report(directory, ['<testcase name="getHealth 200"/>'])
            self.assertTrue(faults.judge(green, "getHealth", faulted=False)[0])
            red = report(directory, ['<testcase name="getHealth 200"><error message="e"/></testcase>'])
            passed, why = faults.judge(red, "getHealth", faulted=False)
            self.assertFalse(passed)
            self.assertIn("red without a fault", why)


if __name__ == "__main__":
    unittest.main()
