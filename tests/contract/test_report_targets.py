"""The report-only phases top up their own held mail, and no gate does: coverage's
skipped-probe ratchet depends on the gate's single rows being spent."""

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
TASK = "test:contract-report-targets"


def arm(script, phase):
    """One arm of run.sh's phase case statement."""
    found = re.search(rf"^{phase}\)\n(.*?)^  ;;$", script, re.MULTILINE | re.DOTALL)
    if not found:
        raise AssertionError(f"run.sh has no {phase} arm")
    return found.group(1)


class ReportTargets(unittest.TestCase):
    def setUp(self):
        self.script = (ROOT / "tests/contract/run.sh").read_text()

    def test_each_report_phase_tops_up_for_itself_before_it_sends(self):
        for phase in ("authorization", "ownership"):
            with self.subTest(phase=phase):
                body = arm(self.script, phase)
                self.assertIn(f"chore {TASK} -- {phase} ||", body)
                self.assertLess(body.index(TASK), body.index("$CONTRACT_VERTRAG"))

    def test_no_gate_tops_up(self):
        for phase in ("gate", "teardown", "fuzz"):
            with self.subTest(phase=phase):
                self.assertNotIn(TASK, arm(self.script, phase))

    def test_the_contract_command_leaves_it_to_those_phases(self):
        chores = (ROOT / "chores.yml").read_text()
        self.assertEqual(
            chores.count(f"chore {TASK}"), 1, "only the task's own description"
        )
        self.assertEqual(chores.count(f"  {TASK}:\n"), 1)


if __name__ == "__main__":
    unittest.main()
