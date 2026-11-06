"""Custom-filter exchanges get a filter of their own, and none is carried (#1385)."""

import json
import unittest
from unittest.mock import patch

from test_hooks_delete_domain import load_hooks

PATH = "/api/v1/admin/custom-filters/"
OPERATIONS = ("getCustomFilter", "updateCustomFilter", "deleteCustomFilter", "testCustomFilter")


class CustomFilterIDs(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, registered = load_hooks()
        self.hooks.get_json = lambda transaction, which="request": json.loads(
            transaction[which]["body"]
        )
        self.hooks.set_json = lambda transaction, body: transaction["request"].update(
            body=json.dumps(body)
        )
        for operation in OPERATIONS + ("createCustomFilter",):
            self.assertIn(operation, registered)
        self.made = []

        def fixture_call(transaction, method, path, body=None, expect=(200, 201, 204)):
            self.assertEqual((method, path), ("POST", "/api/v1/admin/custom-filters"))
            self.made.append(body)
            return {"id": 82 + len(self.made)}

        patcher = patch.object(self.module, "fixture_call", fixture_call)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.start_clean = patch.object(self.module, "start_clean").start()
        self.addCleanup(patch.stopall)

    def transaction(self, resource="1", status=200, suffix=""):
        return {
            "fullPath": f"{PATH}{resource}{suffix}",
            "name": f"{PATH}{{id}} > Fixture > {status} > application/json",
        }

    def clean_starts_on(self):
        patcher = patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"})
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_success_gets_a_new_filter_of_its_own(self):
        for suffix in ("", "/test", "?detail=full"):
            with self.subTest(suffix=suffix):
                transaction = self.transaction(suffix=suffix)
                self.module.on_a_custom_filter_of_its_own(transaction)
                self.assertEqual(transaction["fullPath"], PATH + str(82 + len(self.made)) + suffix)
        names = [row.pop("name") for row in self.made]
        self.assertEqual(len(set(names)), 3)
        self.assertTrue(all(name.startswith("contract-own-filter-") for name in names))
        for row in self.made:
            # As the documented create makes one: no domain, disabled, and a
            # script testCustomFilter can run.
            self.assertEqual(row, self.module.CUSTOM_FILTER)
            self.assertNotIn("domain_id", row)
            self.assertIs(row["enabled"], False)
            self.assertIn("function filter", row["config"]["script"])
        self.assertNotIn("created_custom_filter_id", self.hooks.stash)
        self.start_clean.assert_not_called()

    def test_without_a_reset_refusals_and_generated_ids_stay_unchanged(self):
        for resource, status in (("2147483647", 404), ("0", 422), ("1", 403), ("1", 422), ("12", 200)):
            with self.subTest(resource=resource, status=status):
                transaction = self.transaction(resource, status)
                original = transaction.copy()
                self.module.on_a_custom_filter_of_its_own(transaction)
                self.assertEqual(transaction, original)
        transaction = self.transaction()
        original = transaction.copy()
        with patch.dict("os.environ", {"CONTRACT_COVERAGE": "1"}):
            self.module.on_a_custom_filter_of_its_own(transaction)
        self.assertEqual(transaction, original)
        self.assertEqual(self.made, [])

    def test_after_a_reset_every_exchange_starts_clean_and_positive_ids_get_their_own(self):
        self.clean_starts_on()
        for resource, status, suffix in (("1", 200, ""), ("1", 422, ""), ("7", 200, "/test"), ("1", 422, "/test")):
            with self.subTest(resource=resource, status=status):
                transaction = self.transaction(resource, status, suffix)
                self.module.on_a_custom_filter_of_its_own(transaction)
                self.start_clean.assert_called_once_with(transaction)
                self.start_clean.reset_mock()
                self.assertEqual(transaction["fullPath"], PATH + str(82 + len(self.made)) + suffix)

    def test_after_a_reset_refusals_keep_their_ids(self):
        self.clean_starts_on()
        for resource, status in (("2147483647", 404), ("55", 404), ("0", 422), ("-1", 422), ("x", 422)):
            with self.subTest(resource=resource):
                transaction = self.transaction(resource, status)
                original = transaction.copy()
                self.module.on_a_custom_filter_of_its_own(transaction)
                self.start_clean.assert_called_once_with(transaction)
                self.start_clean.reset_mock()
                self.assertEqual(transaction, original)
        self.assertEqual(self.made, [])

    def test_after_a_reset_a_create_is_sent_as_documented(self):
        self.clean_starts_on()
        # A domain the run made earlier is gone after the reset: not used.
        self.hooks.stash["domain_id"] = 42
        for body in ({"name": "tag-invoices", "enabled": False}, {"domain_id": 1, "enabled": "false"}, []):
            with self.subTest(body=body):
                transaction = {"fullPath": PATH[:-1], "request": {"body": json.dumps(body)}}
                self.module.a_custom_filter_on_the_created_domain(transaction)
                self.start_clean.assert_called_once_with(transaction)
                self.start_clean.reset_mock()
                self.assertEqual(json.loads(transaction["request"]["body"]), body)

    def test_without_a_reset_none_is_asked_for(self):
        self.hooks.stash["domain_id"] = 42
        transaction = {"fullPath": PATH[:-1], "request": {"body": json.dumps({"name": "x"})}}
        self.module.a_custom_filter_on_the_created_domain(transaction)
        self.assertEqual(json.loads(transaction["request"]["body"]), {"name": "x", "domain_id": 42})
        self.module.on_a_custom_filter_of_its_own(self.transaction())
        self.start_clean.assert_not_called()


if __name__ == "__main__":
    unittest.main()
