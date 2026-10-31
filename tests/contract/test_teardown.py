"""The operations that wipe the run's data are sent last, by a gate of their
own, and only there (#1334)."""

from pathlib import Path
import unittest

import yaml

import teardown

ROOT = Path(__file__).resolve().parents[2]


class Teardown(unittest.TestCase):
    def test_the_run_is_narrowed_to_the_wiping_operations(self):
        config = {"exclude-operation-id": ["deliverMessage", "resetTestData", "restoreTestSnapshot"],
                  "surface": {"exclude": []}, "unproven": ".cache/contract/unproven.json", "provides": {}}
        out = teardown.configure(config)
        self.assertEqual(out["exclude-operation-id"], ["deliverMessage"])
        self.assertEqual(out["operation-id"], ["resetTestData", "restoreTestSnapshot"])
        self.assertNotIn("surface", out)
        self.assertEqual(out["unproven"], ".cache/contract/unproven-teardown.json")
        self.assertEqual(out["provides"], {})

    def test_the_primary_run_leaves_them_for_the_teardown(self):
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        for operation in teardown.OPERATIONS:
            with self.subTest(operation=operation):
                self.assertIn(operation, config["exclude-operation-id"])
                reasons = [item["reason"] for item in config["surface"]["exclude"]
                           if item.get("operation") == operation and "status" not in item]
                self.assertEqual(len(reasons), 1)
                self.assertIn("tests/contract/teardown.py", reasons[0])

    def test_ci_sends_it_last_as_a_gate(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/e2e-suite.yml").read_text())
        steps = workflow["jobs"]["suite"]["steps"]
        names = [step.get("name", "") for step in steps]
        teardown_step = next(s for s in steps if s.get("run") == "chore test:contract-teardown")
        self.assertNotIn("continue-on-error", teardown_step)
        self.assertEqual(teardown_step["if"], "inputs.contract-phases == 'gate'")
        self.assertLess(names.index("Contract tests (examples, coverage, secret scan and authorization report)"),
                        names.index(teardown_step["name"]))
        self.assertLess(names.index("Limits suite (report)"), names.index(teardown_step["name"]))
        chores = (ROOT / "chores.yml").read_text()
        self.assertIn("tests/contract/run.sh teardown", chores)


if __name__ == "__main__":
    unittest.main()
