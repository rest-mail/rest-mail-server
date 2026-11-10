import copy
import os
import unittest
from unittest.mock import Mock, patch

from test_clean_start_batch_regressions import registered_hooks


class MessageCoverageFixture(unittest.TestCase):
    def setUp(self):
        self.enterContext(patch.dict(os.environ, {
            "CONTRACT_COVERAGE": "1",
            "CONTRACT_RESET_SEED": "",
        }))
        self.callbacks, self.loaded, self.stub = registered_hooks()
        self.import_message = Mock(return_value=700)
        self.reset = Mock()

    def transaction(self, identifier="101", status="200"):
        return {
            "name": f"/api/v1/messages/{{id}} > Get a message by ID > {status} > application/json",
            "fullPath": f"/api/v1/messages/{identifier}?view=full",
            "request": {
                "method": "GET",
                "body": "",
                "headers": {"Authorization": "Bearer original-credential"},
            },
        }

    def invoke(self, transaction):
        for callback in self.callbacks["before"].get("getMessage", []):
            with patch.dict(callback.__globals__, {
                "an_imported_message": self.import_message,
                "start_clean": self.reset,
            }):
                callback(transaction)

    def test_coverage_reads_create_their_message_without_resetting(self):
        for identifier in ("101", "1", "999"):
            with self.subTest(identifier=identifier):
                self.import_message.reset_mock()
                transaction = self.transaction(identifier)
                request = copy.deepcopy(transaction["request"])
                self.invoke(transaction)
                self.import_message.assert_called_once()
                self.assertEqual(transaction["fullPath"], "/api/v1/messages/700?view=full")
                self.assertEqual(transaction["request"], request)
                self.reset.assert_not_called()

    def test_invalid_and_deliberately_missing_ids_are_not_replaced(self):
        for identifier in ("0", "-1", "bad", "1.5", "2147483647"):
            with self.subTest(identifier=identifier):
                transaction = self.transaction(identifier)
                original = copy.deepcopy(transaction)
                self.invoke(transaction)
                self.assertEqual(transaction, original)
        self.import_message.assert_not_called()
        self.reset.assert_not_called()

    def test_documented_not_found_examples_keep_their_ids(self):
        transaction = self.transaction("101", "404")
        original = copy.deepcopy(transaction)
        self.invoke(transaction)
        self.assertEqual(transaction, original)
        self.import_message.assert_not_called()
        self.reset.assert_not_called()

    def test_noncoverage_examples_keep_their_linked_message(self):
        transaction = self.transaction()
        original = copy.deepcopy(transaction)
        with patch.dict(os.environ, {"CONTRACT_COVERAGE": ""}):
            self.invoke(transaction)
        self.assertEqual(transaction, original)
        self.import_message.assert_not_called()
        self.reset.assert_not_called()

    def test_clean_start_still_imports_only_once_after_reset(self):
        transaction = self.transaction()
        with patch.dict(os.environ, {"CONTRACT_RESET_SEED": "1"}):
            self.invoke(transaction)
        self.reset.assert_called_once_with(transaction)
        self.import_message.assert_called_once()
        self.assertEqual(transaction["fullPath"], "/api/v1/messages/700?view=full")

    def test_import_failure_is_not_hidden(self):
        self.import_message.side_effect = RuntimeError("fixture import failed")
        with self.assertRaisesRegex(RuntimeError, "fixture import failed"):
            self.invoke(self.transaction())


if __name__ == "__main__":
    unittest.main()
