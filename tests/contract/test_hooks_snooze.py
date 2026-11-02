"""No hook may rewrite a snooze probe's body: Vertrag would abandon the probe."""

import importlib.util
import json
import runpy
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
# The name a before hook receives. Vertrag 0.42.0 passes the source
# transaction's name in every phase, and adds "coverage: " only to the result it
# reports (cmd/vertrag/coveragecmd.go, runner.Runner.PrepareGenerated).
REAL_NAME = (
    "/api/v1/accounts/{id}/threads/{threadID}/snooze > "
    "Snooze a thread or cancel its snooze > 200 > application/json"
)


def load_snooze_hooks():
    """Every before hook on snoozeThread, from the files entrypoint.py loads."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    registered = {}

    def before(name):
        def register(function):
            registered.setdefault(name, []).append(function)
            return function

        return register

    stub.before = before
    stub.after = lambda name: lambda function: function
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
        snooze = runpy.run_path(str(ROOT / "tests/contract/thread_snooze_hooks.py"))
        snooze["register_thread_snooze_hook"](lambda origin: "fixture-token")
    return registered["snoozeThread"]


HOOKS = load_snooze_hooks()


def sent(body):
    """The body after every hook, for a transaction no fixture applies to."""
    transaction = {
        "name": REAL_NAME,
        "fullPath": "/api/v1/accounts/1/threads/not-the-fixture/snooze",
        "expected": {"statusCode": "200"},
        "protocol": "http:",
        "host": "server.test",
        "port": 8080,
        "request": {"method": "POST", "body": body, "headers": {}},
    }
    for hook in HOOKS:
        hook(transaction)
    return transaction["request"]["body"]


class SnoozeProbe(unittest.TestCase):
    def test_the_generators_past_date_is_left_to_the_pin(self):
        # The date goes forward through fuzz.pin (probes.py). A hook changing it
        # here would end the probe with ErrChangedByHook, sending nothing.
        body = '{"snoozed_until":"2020-01-02T03:04:05Z"}'
        self.assertEqual(sent(body), body)

    def test_the_named_refusal_of_a_past_date_stays_past(self):
        original = '{"snoozed_until":"2000-01-01T00:00:00Z"}'
        self.assertEqual(sent(original), original)

    def test_a_probe_that_is_not_a_date_keeps_its_value(self):
        for original in ('{"snoozed_until":"tomorrow"}', '{"snoozed_until":null}', "[]", "{"):
            with self.subTest(original):
                self.assertEqual(sent(original), original)


if __name__ == "__main__":
    unittest.main()
