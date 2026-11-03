"""Queue coverage preserves generated requests through the configured entrypoint."""

import copy
import json
import os
import runpy
import subprocess
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

import yaml
from test_hooks_two_factor_admin import session

ROOT = Path(__file__).resolve().parents[2]
OPERATIONS = {
    "deleteQueueEntry": ("DELETE", "", "Delete a queue entry", 204),
    "getQueueEntry": ("GET", "", "Get a single queue entry", 200),
    "retryQueueEntry": ("POST", "/retry", "Retry a single queue entry", 200),
    "bounceQueueEntry": ("POST", "/bounce", "Bounce a single queue entry", 200),
}


class QueueEntryHooks(unittest.TestCase):
    def setUp(self):
        stub = types.ModuleType("vertrag_hooks")
        stub.stash = {}
        self.callbacks = {}

        def before(operation):
            def register(callback):
                self.callbacks.setdefault(operation, []).append(callback)
                return callback

            return register

        stub.before = before
        stub.after = lambda _: lambda callback: callback
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        with patch.dict(sys.modules, {"vertrag_hooks": stub}):
            self.loaded = runpy.run_path(str(ROOT / config["hookfiles"]))
        self.serial = 100
        self.fixture = patch("subprocess.run", side_effect=self.replenish).start()
        self.environment = patch.dict(os.environ, {"CONTRACT_COVERAGE": "1"})
        self.environment.start()
        self.addCleanup(patch.stopall)
        self.addCleanup(self.environment.stop)

    def replenish(self, *args, **kwargs):
        self.serial += 1
        return types.SimpleNamespace(
            stdout="QUEUE_ENTRY_FIXTURE="
            + json.dumps({"id": self.serial})
            + "\ncontract fixtures prepared\n"
        )

    def transaction(self, operation, identifier="1", status=None):
        method, suffix, summary, success = OPERATIONS[operation]
        status = status or success
        media = " > application/json" if status != 204 else ""
        return {
            "name": f"/api/v1/admin/queue/{{id}}{suffix} > {summary} > {status}{media}",
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": "8080",
            "fullPath": f"/api/v1/admin/queue/{identifier}{suffix}?keep=%2F&keep=&flag",
            "request": {
                "method": method,
                "headers": {"Authorization": session("admin@restmail.test")},
                "body": '{ "generated": [null, false, 0] }',
            },
            "expected": {"statusCode": str(status)},
        }

    def send(self, operation, transaction, coverage_only=False):
        for callback in self.callbacks[operation]:
            if (
                not coverage_only
                or callback.__name__ == "a_disposable_queue_entry_for_coverage"
            ):
                callback(transaction)

    def test_each_success_and_repeat_gets_its_own_target(self):
        targets = set()
        for operation in OPERATIONS:
            for identifier in ("1", "41", "155"):
                with self.subTest(operation=operation):
                    transaction = self.transaction(operation, identifier)
                    original = copy.deepcopy(transaction)
                    self.send(operation, transaction)
                    target = transaction["fullPath"].split("/")[5].split("?")[0]
                    self.assertNotEqual(target, "1")
                    self.assertNotIn(target, targets)
                    targets.add(target)
                    original["fullPath"] = transaction["fullPath"]
                    self.assertEqual(transaction, original)
                    self.assertTrue(
                        transaction["fullPath"].endswith("?keep=%2F&keep=&flag")
                    )
        self.assertEqual(self.fixture.call_count, 12)
        self.fixture.assert_called_with(
            ["chore", "test:contract-queue-entry-fixture"],
            check=True,
            capture_output=True,
            timeout=20,
            text=True,
        )

    def test_generated_body_bytes_and_credentials_are_not_rewritten(self):
        for operation in OPERATIONS:
            for body in ("", "[]", "null", "{broken", '{ "id": 0 }'):
                transaction = self.transaction(operation)
                transaction["request"]["body"] = body
                transaction["request"]["headers"]["X-API-Key"] = "unchanged"
                request = copy.deepcopy(transaction["request"])
                self.send(operation, transaction)
                self.assertEqual(transaction["request"], request)
                self.assertNotIn("/queue/1?", transaction["fullPath"])
                self.assertNotIn("/queue/1/", transaction["fullPath"])

    def test_refusals_and_malformed_ids_are_untouched(self):
        for operation in OPERATIONS:
            for identifier in (
                "0",
                "-1",
                "nope",
                "1.5",
                "01",
                "4294967296",
                "99999999999999999999",
                "%31",
            ):
                transaction = self.transaction(operation, identifier)
                original = copy.deepcopy(transaction)
                self.send(operation, transaction)
                self.assertEqual(transaction, original)
            for status in (400, 401, 403, 404, 422, 500):
                for identifier in ("1", "41", "2147483647"):
                    transaction = self.transaction(operation, identifier, status=status)
                    original = copy.deepcopy(transaction)
                    self.send(operation, transaction)
                    self.assertEqual(transaction, original)
        self.fixture.assert_not_called()

    def test_other_phases_do_not_get_coverage_targets(self):
        for phase in ("", "0", "examples", "fuzz", "authorization", "ownership"):
            with patch.dict(os.environ, {"CONTRACT_COVERAGE": phase}):
                for operation in OPERATIONS:
                    transaction = self.transaction(operation)
                    original = copy.deepcopy(transaction)
                    self.send(operation, transaction, coverage_only=True)
                    self.assertEqual(transaction, original)
        self.fixture.assert_not_called()

    def test_other_principals_and_unauthenticated_requests_are_untouched(self):
        for operation in OPERATIONS:
            for authorization in (
                None,
                "",
                "Basic refused",
                "Bearer invalid",
                session("other@restmail.test"),
            ):
                transaction = self.transaction(operation)
                transaction["request"]["headers"] = {}
                if authorization is not None:
                    transaction["request"]["headers"]["Authorization"] = authorization
                original = copy.deepcopy(transaction)
                self.send(operation, transaction)
                self.assertEqual(transaction, original)
        self.fixture.assert_not_called()

    def test_methods_and_other_routes_are_untouched(self):
        for operation in OPERATIONS:
            for field, value in (
                ("method", "PATCH"),
                ("path", "/api/v1/admin/queue/1/other"),
            ):
                transaction = self.transaction(operation)
                if field == "method":
                    transaction["request"]["method"] = value
                else:
                    transaction["fullPath"] = value
                original = copy.deepcopy(transaction)
                self.send(operation, transaction)
                self.assertEqual(transaction, original)
        self.fixture.assert_not_called()

    def test_setup_failures_propagate(self):
        for operation in OPERATIONS:
            for failure in (
                subprocess.CalledProcessError(1, "chore"),
                subprocess.TimeoutExpired("chore", 20),
            ):
                self.fixture.side_effect = failure
                with self.assertRaises(type(failure)):
                    self.send(operation, self.transaction(operation))

    def test_missing_malformed_and_nonpositive_fixture_ids_fail(self):
        self.fixture.side_effect = None
        for output in (
            "",
            "QUEUE_ENTRY_FIXTURE=not-json",
            "QUEUE_ENTRY_FIXTURE={}",
            'QUEUE_ENTRY_FIXTURE={"id":0}',
            'QUEUE_ENTRY_FIXTURE={"id":true}',
            'QUEUE_ENTRY_FIXTURE={"id":"2"}',
            'QUEUE_ENTRY_FIXTURE={"id":-1}',
            'QUEUE_ENTRY_FIXTURE={"id":4294967296}',
        ):
            for operation in OPERATIONS:
                self.fixture.return_value = types.SimpleNamespace(stdout=output)
                with self.assertRaises((RuntimeError, ValueError)):
                    self.send(operation, self.transaction(operation))


if __name__ == "__main__":
    unittest.main()
