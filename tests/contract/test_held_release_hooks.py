"""The own-data release must spend the sender's budget and observe persisted mail."""

import copy
import importlib.util
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import Mock, patch


class HeldReleaseProof(unittest.TestCase):
    def setUp(self):
        self.hooks = types.ModuleType("vertrag_hooks")
        self.hooks.stash = {}
        self.hooks.before = self.hooks.after = lambda _: lambda callback: callback
        self.hooks.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
        spec = importlib.util.spec_from_file_location("held_release_under_test", Path(__file__).with_name("held_release_hooks.py"))
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": self.hooks}):
            spec.loader.exec_module(self.module)
        self.shared = {
            "asked_for_a_clean_start": Mock(return_value=True),
            "setup_origin": Mock(return_value="http://api:8080"),
            "mfa_admin_token": Mock(return_value="real-mfa-session"),
            "ADMIN": "admin@restmail.test",
            "session_address": Mock(return_value="admin@restmail.test"),
            "CREDENTIALED_PASSWORD": "fixture-password",
            "a_made_account": Mock(return_value={"id": 17, "address": "sender@restmail.test"}),
            "mfa_admin": types.SimpleNamespace(ADDRESS="mfa@restmail.test", _sign_in=Mock(return_value={"access_token": "sender-session"})),
        }
        self.before, self.after = self.module.register_held_release_hooks(self.shared)
        self.transaction = {
            "fullPath": "/api/v1/messages/scheduled/3/release",
            "request": {"headers": {"Authorization": "Bearer fixture-session"}},
            "expected": {"statusCode": "200"},
        }
        self.held = {"id": 41, "account_id": 17, "status": "held", "subject": "Quarterly report", "last_error": "account:17: messages_per_hour"}
        self.call = self.enterContext(patch.object(self.module, "call"))
        self.call.side_effect = [{}, {"id": 10}, self.held]

    def prepare(self):
        self.before(self.transaction)

    def release(self, **changes):
        released = {**self.held, "status": "sent", "sent_message_id": 51, **changes}
        released.pop("last_error", None)
        self.transaction["real"] = {"statusCode": "200", "body": json.dumps({"data": released})}
        self.call.side_effect = [
            {"error": {"code": "not_held"}},
            {"id": 51, "folder": "Sent", "sender": "sender@restmail.test", "subject": "Quarterly report"},
            [],
        ]
        self.after(self.transaction)

    def test_release_spends_the_disposable_senders_own_budget_and_keeps_the_actual_202_id(self):
        self.prepare()
        self.shared["a_made_account"].assert_called_once_with("http://api:8080", "contract-held-release", "fixture-password")
        calls = self.call.call_args_list
        self.assertEqual([c.args[1] for c in calls], ["real-mfa-session", "sender-session", "sender-session"])
        self.assertEqual([c.kwargs.get("status", 200) for c in calls], [200, 201, 202])
        self.assertEqual(calls[1].args[4], calls[2].args[4])
        self.assertEqual(self.transaction["fullPath"], "/api/v1/messages/scheduled/41/release")
        self.assertEqual(self.transaction["request"]["headers"]["Authorization"], "Bearer real-mfa-session")
        self.release()
        self.assertNotIn("fail", self.transaction)

    def test_release_requires_the_same_row_to_transition_and_a_persisted_sent_copy(self):
        for changes in ({"id": 99}, {"status": "held"}, {"sent_message_id": None}):
            with self.subTest(changes=changes):
                self.call.side_effect = [{}, {}, self.held]
                self.prepare()
                self.release(**changes)
                self.assertIn("held release proof:", self.transaction.pop("fail"))

    def test_schema_valid_200_without_setup_or_persistence_is_red(self):
        self.release()
        self.assertIn("no real held record", self.transaction.pop("fail"))
        for again, sent, remaining in (
            ({"error": {"code": "not_found"}}, {}, []),
            ({"error": {"code": "not_held"}}, {"id": 51, "folder": "INBOX"}, []),
            ({"error": {"code": "not_held"}}, {"id": 51, "folder": "Sent", "sender": "sender@restmail.test", "subject": "Quarterly report"}, [self.held]),
        ):
            self.call.side_effect = [{}, {}, self.held]
            self.prepare()
            self.transaction["real"] = {"statusCode": "200", "body": json.dumps({"data": {**self.held, "status": "sent", "sent_message_id": 51}})}
            self.call.side_effect = [again, sent, remaining]
            self.after(self.transaction)
            self.assertIn("held release proof:", self.transaction.pop("fail"))

    def test_refusals_and_ordinary_non_clean_start_keep_their_target_and_credentials(self):
        for clean, status in ((False, "200"), (True, "401"), (True, "403"), (True, "404"), (True, "409"), (True, "422")):
            with self.subTest(clean=clean, status=status):
                self.shared["asked_for_a_clean_start"].return_value = clean
                self.transaction["expected"]["statusCode"] = status
                before = copy.deepcopy(self.transaction)
                self.before(self.transaction)
                self.after(self.transaction)
                self.assertEqual(self.transaction, before)
        self.call.assert_not_called()

    def test_wrong_principal_is_never_replaced_and_an_existing_mfa_target_keeps_its_token(self):
        original = copy.deepcopy(self.transaction)
        self.shared["session_address"].return_value = "unrelated@restmail.test"
        self.before(self.transaction)
        self.assertEqual(self.transaction, original)
        self.call.assert_not_called()
        self.shared["session_address"].return_value = "mfa@restmail.test"
        self.prepare()
        self.assertEqual(self.transaction["request"]["headers"], original["request"]["headers"])

    def test_a_hold_from_another_account_or_budget_is_not_a_fixture(self):
        for changed in ({"account_id": 18}, {"status": "pending"}, {"last_error": "server: messages_per_hour"}):
            self.call.side_effect = [{}, {}, {**self.held, **changed}]
            with self.subTest(changed=changed), self.assertRaises(RuntimeError):
                self.prepare()


if __name__ == "__main__":
    unittest.main()
