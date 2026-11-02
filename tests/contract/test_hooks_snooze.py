"""The hook that lets a coverage probe of a snooze reach its 200."""

import importlib.util
import json
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.before = stub.after = lambda name: lambda fn: fn
    stub.get_json = lambda transaction, which="request": json.loads(
        transaction[which]["body"]
    )

    def set_json(transaction, value):
        transaction["request"]["body"] = json.dumps(value)

    stub.set_json = set_json
    spec = importlib.util.spec_from_file_location(
        "contract_snooze_hooks", ROOT / "tests/contract/hooks.py"
    )
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module


HOOKS = load_hooks()


def sent(name, body):
    transaction = {"name": name, "request": {"body": body}}
    HOOKS.a_future_date_for_a_snooze_probe(transaction)
    return transaction["request"]["body"]


class SnoozeProbe(unittest.TestCase):
    def test_a_coverage_probe_of_a_past_date_moves_forward(self):
        body = sent("coverage: snooze", '{"snoozed_until":"2020-01-02T03:04:05Z"}')
        self.assertEqual(json.loads(body), {"snoozed_until": "2120-01-02T03:04:05Z"})

    def test_the_named_refusal_of_a_past_date_stays_past(self):
        original = '{"snoozed_until":"2000-01-01T00:00:00Z"}'
        self.assertEqual(sent("snoozeThread > past", original), original)

    def test_a_probe_that_is_not_a_date_keeps_its_value(self):
        for original in ('{"snoozed_until":"tomorrow"}', '{"snoozed_until":null}', "[]", "{"):
            with self.subTest(original):
                self.assertEqual(sent("coverage: snooze", original), original)


if __name__ == "__main__":
    unittest.main()
