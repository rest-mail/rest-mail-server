"""The operations that wipe the run's data are sent last, by a gate of their
own, and only there (#1334)."""

import json
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

    def test_the_primary_run_leaves_wiping_examples_for_own_data(self):
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        examples = json.loads((ROOT / "openapi-example.json").read_text())
        self.assertEqual(config["exclude-operation-id"], [])
        self.assertEqual(config["surface"]["exclude"], [])
        self.assertNotIn("disposable_data", config.get("provides", {}).get("state", []))
        for operation, name in (("resetTestData", "admin"), ("restoreTestSnapshot", "admin"), ("restoreTestSnapshot", "unknown_field")):
            with self.subTest(operation=operation, name=name):
                self.assertEqual(examples[operation][name]["requires"], {"state": "disposable_data"})

    def test_ci_sends_it_last_as_a_gate(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/e2e-suite.yml").read_text())
        steps = workflow["jobs"]["suite"]["steps"]
        names = [step.get("name", "") for step in steps]
        teardown_step = next(s for s in steps if s.get("run") == "chore test:contract-own-data")
        self.assertNotIn("continue-on-error", teardown_step)
        self.assertEqual(teardown_step["if"], "inputs.contract-phases == 'gate'")
        self.assertLess(names.index("Contract tests (examples, coverage and secret scan)"),
                        names.index(teardown_step["name"]))
        self.assertLess(names.index("Limits suite"), names.index(teardown_step["name"]))
        chores = (ROOT / "chores.yml").read_text()
        self.assertIn("tests/contract/run.sh own-data", chores)
        self.assertNotIn("tests/contract/run.sh teardown", chores)


if __name__ == "__main__":
    unittest.main()
