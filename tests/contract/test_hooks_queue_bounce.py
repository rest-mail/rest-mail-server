"""Give the single bounce's examples an entry in the state each one documents."""

import copy
import os
import unittest
from unittest.mock import call, patch

from test_hooks_example_state import load_hooks


class QueueBounceHookTests(unittest.TestCase):
    def setUp(self):
        self.hooks, _ = load_hooks()
        self.listing = (
            "/api/v1/admin/queue?recipient=contract-bounce%40retry.invalid&status=deferred"
        )

    def transaction(self, entry="10", status="200"):
        return {
            "name": f"/api/v1/admin/queue/{{id}}/bounce > Bounce a single queue entry > {status} > application/json",
            "fullPath": f"/api/v1/admin/queue/{entry}/bounce?proof=kept",
            "request": {"headers": {"Authorization": "Bearer original"}, "body": ""},
        }

    def test_the_success_takes_the_held_entry(self):
        transaction = self.transaction()
        with patch.object(
            self.hooks, "fixture_call", return_value={"items": [{"id": 42}]}
        ) as api, patch.object(self.hooks, "a_disposable_queue_entry") as prepare:
            self.hooks.bounce_the_held_bounce_entry(transaction)
        api.assert_called_once_with(transaction, "GET", self.listing)
        prepare.assert_not_called()
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/queue/42/bounce?proof=kept")

    def test_the_success_gets_a_new_entry_after_a_bulk_bounce_took_the_held_one(self):
        # A bulk bounce's empty filter bounces the held entry, and a bounced
        # entry is refused, so the success needs one that is still deferred.
        transaction = self.transaction()
        original = copy.deepcopy(transaction)
        with patch.object(
            self.hooks, "fixture_call", return_value={"items": []}
        ) as api, patch.object(
            self.hooks, "a_disposable_queue_entry", return_value=77
        ) as prepare:
            self.hooks.bounce_the_held_bounce_entry(transaction)
        api.assert_called_once_with(transaction, "GET", self.listing)
        prepare.assert_called_once_with()
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/queue/77/bounce?proof=kept")
        self.assertEqual(transaction["request"], original["request"])
        self.assertEqual(transaction["name"], original["name"])

    def test_the_refusal_gets_an_entry_bounced_first(self):
        # The refusal must not take the held entry, which the success needs.
        transaction = self.transaction(status="400")
        with patch.object(self.hooks, "fixture_call") as api, patch.object(
            self.hooks, "a_disposable_queue_entry", return_value=77
        ):
            self.hooks.bounce_the_held_bounce_entry(transaction)
        self.assertEqual(
            api.call_args_list,
            [call(transaction, "POST", "/api/v1/admin/queue/77/bounce")],
        )
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/queue/77/bounce?proof=kept")

    def test_other_refusals_and_probes_keep_their_targets(self):
        for entry, status in (("2147483647", "404"), ("0", "422"), ("x", "200"), ("0", "400")):
            with self.subTest(entry=entry, status=status):
                transaction = self.transaction(entry, status)
                with patch.object(self.hooks, "fixture_call") as api, patch.object(
                    self.hooks, "a_disposable_queue_entry"
                ) as prepare:
                    self.hooks.bounce_the_held_bounce_entry(transaction)
                api.assert_not_called()
                prepare.assert_not_called()
                self.assertEqual(
                    transaction["fullPath"], f"/api/v1/admin/queue/{entry}/bounce?proof=kept"
                )

    def test_the_coverage_phase_is_left_to_its_own_hook(self):
        transaction = self.transaction(status="400")
        with patch.dict(os.environ, {"CONTRACT_COVERAGE": "1"}), patch.object(
            self.hooks, "fixture_call"
        ) as api:
            self.hooks.bounce_the_held_bounce_entry(transaction)
        api.assert_not_called()
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/queue/10/bounce?proof=kept")


if __name__ == "__main__":
    unittest.main()
