"""Keep the retry proof independent of destructive empty-filter bulk examples."""

import copy
import subprocess
import unittest
from unittest.mock import call, patch

from test_hooks_example_state import load_hooks


class QueueRetryHookTests(unittest.TestCase):
    def setUp(self):
        self.hooks, _ = load_hooks()
        self.recipient = self.hooks.FIXTURE_PLAN["queue"]["retry"]["recipient"]

    def transaction(self, entry="10", status="200"):
        return {
            "name": f"/api/v1/admin/queue/{{id}}/retry > Retry a single queue entry > {status} > application/json",
            "fullPath": f"/api/v1/admin/queue/{entry}/retry?proof=kept",
            "request": {"headers": {"Authorization": "Bearer original"}, "body": ""},
        }

    def test_bulk_bounced_retry_fixture_is_replenished_without_changing_credentials(self):
        transaction = self.transaction()
        original = copy.deepcopy(transaction)
        with patch.object(self.hooks, "fixture_call", side_effect=[
            {"recipient": self.recipient, "status": "bounced"},
            {"items": [{"id": 42}]},
        ]) as api, patch.object(self.hooks.subprocess, "run") as prepare:
            self.hooks.retry_a_held_entry_after_bulk_examples(transaction)
        prepare.assert_called_once_with(
            ["chore", "test:contract-retry-fixture"], check=True, capture_output=True, timeout=20,
        )
        self.assertEqual(api.call_args_list, [
            call(transaction, "GET", "/api/v1/admin/queue/10", expect=(404, 422)),
            call(transaction, "GET", "/api/v1/admin/queue?recipient=contract-retry%40retry.invalid&status=deferred"),
        ])
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/queue/42/retry?proof=kept")
        self.assertEqual(transaction["request"], original["request"])
        self.assertEqual(transaction["name"], original["name"])

    def test_eligible_missing_and_unrelated_entries_are_not_replaced(self):
        for current in (
            None,
            {"recipient": self.recipient, "status": "pending"},
            {"recipient": self.recipient, "status": "deferred"},
            {"recipient": "someone@example.test", "status": "bounced"},
        ):
            with self.subTest(current=current):
                transaction = self.transaction()
                original = copy.deepcopy(transaction)
                with patch.object(self.hooks, "fixture_call", return_value=current), \
                        patch.object(self.hooks.subprocess, "run") as prepare:
                    self.hooks.retry_a_held_entry_after_bulk_examples(transaction)
                prepare.assert_not_called()
                self.assertEqual(transaction, original)

    def test_refusals_and_invalid_boundary_ids_reach_the_handler_unchanged(self):
        for entry, status in (
            ("5", "400"), ("2147483647", "404"), ("10", "401"),
            ("0", "200"), ("-1", "200"), ("1.5", "200"), ("not-an-id", "200"),
        ):
            with self.subTest(entry=entry, status=status):
                transaction = self.transaction(entry, status)
                original = copy.deepcopy(transaction)
                with patch.object(self.hooks, "fixture_call") as api, \
                        patch.object(self.hooks.subprocess, "run") as prepare:
                    self.hooks.retry_a_held_entry_after_bulk_examples(transaction)
                api.assert_not_called()
                prepare.assert_not_called()
                self.assertEqual(transaction, original)

    def test_failed_replenishment_fails_the_proof(self):
        transaction = self.transaction()
        with patch.object(self.hooks, "fixture_call", return_value={
            "recipient": self.recipient, "status": "bounced",
        }), patch.object(self.hooks.subprocess, "run", side_effect=subprocess.TimeoutExpired("chore", 20)):
            with self.assertRaises(subprocess.TimeoutExpired):
                self.hooks.retry_a_held_entry_after_bulk_examples(transaction)

    def test_missing_replenished_entry_fails_the_proof(self):
        with patch.object(self.hooks, "fixture_call", side_effect=[
            {"recipient": self.recipient, "status": "bounced"}, {"items": []},
        ]), patch.object(self.hooks.subprocess, "run"):
            with self.assertRaisesRegex(RuntimeError, "replenished contract retry fixture is missing"):
                self.hooks.retry_a_held_entry_after_bulk_examples(self.transaction())


if __name__ == "__main__":
    unittest.main()
