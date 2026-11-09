import json
import os
from pathlib import Path
import runpy
import sys
import types
import unittest
from unittest.mock import Mock, patch


DIRECTORY = Path(__file__).resolve().parent


def registered_hooks():
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    callbacks = {"before": {}, "after": {}}

    def register(phase):
        def operation(name):
            def remember(function):
                callbacks[phase].setdefault(name, []).append(function)
                return function

            return remember

        return operation

    stub.before = register("before")
    stub.after = register("after")
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    stub.set_json = lambda transaction, body: transaction["request"].__setitem__("body", json.dumps(body))
    source = Path(os.environ.get("RESTMAIL_C1_CONTROL_HOOKS", DIRECTORY / "hooks.py"))
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        loaded = runpy.run_path(str(source))
    return callbacks, loaded, stub


class BatchRegressions(unittest.TestCase):
    def setUp(self):
        environment = patch.dict(os.environ, {"CONTRACT_RESET_SEED": "1"})
        environment.start()
        self.addCleanup(environment.stop)
        self.callbacks, self.loaded, self.stub = registered_hooks()

    def test_public_health_exchange_resets_before_sending(self):
        transaction = {
            "fullPath": "/api/health",
            "name": "/api/health > found > 200 > application/json",
            "request": {"body": "{}"},
        }
        reset = Mock()
        for callback in self.callbacks["before"].get("getHealth", []):
            with patch.dict(callback.__globals__, {"start_clean": reset}):
                callback(transaction)
        reset.assert_called_once_with(transaction)

    def test_hosted_lookup_cannot_pass_when_the_mailbox_is_absent(self):
        transaction = {
            "fullPath": "/api/mailboxes?address=user%40example.com",
            "name": "/api/mailboxes > found > 200 > application/json",
            "request": {"body": "{}"},
            "real": {"statusCode": 200, "body": json.dumps({"data": {"exists": False}})},
        }
        with patch.dict(os.environ, {"CONTRACT_RESET_SEED": ""}):
            self.loaded["look_up_a_hosted_mailbox"](transaction)
            for callback in self.callbacks["after"].get("checkMailboxAddress", []):
                callback(transaction)
        self.assertIn("fail", transaction, "a missing hosted mailbox passed its found example")

    def test_empty_listings_cannot_pass_their_created_row_proof(self):
        for operation in (
            "listCustomFilters", "listDomains", "listAliases", "listDKIMKeys",
            "listCertificates", "listCalendarEvents", "listDmarcReports",
            "getDeliveryLogs", "listPipelineLogs",
        ):
            with self.subTest(operation=operation):
                transaction = {
                    "name": f"/{operation} > found > 200 > application/json",
                    "request": {"body": "{}"},
                    "real": {"statusCode": 200, "body": json.dumps({"data": []})},
                }
                self.loaded["made_for_the_listing"](transaction, "fixture row", {"id": 41})
                for callback in self.callbacks["after"].get(operation, []):
                    callback(transaction)
                self.assertIn("fail", transaction, f"{operation} accepted an empty list")

    def test_listing_guard_is_registered_exactly_once_per_operation(self):
        for operation, callbacks in self.callbacks["after"].items():
            guards = [callback for callback in callbacks if callback.__name__ == "the_listing_holds_what_was_made"]
            with self.subTest(operation=operation):
                self.assertLessEqual(len(guards), 1)


if __name__ == "__main__":
    unittest.main()
