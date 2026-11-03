"""Named custom-filter successes use the resource created for this live run."""

import json
import unittest
from unittest.mock import patch

from test_hooks_delete_domain import load_hooks


class CustomFilterIDs(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, registered = load_hooks()
        self.hooks.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
        for operation in ("getCustomFilter", "updateCustomFilter", "deleteCustomFilter", "testCustomFilter"):
            self.assertIn(operation, registered)
        self.module.keep_the_created_custom_filter({"real": {"body": '{"data":{"id":83}}'}})

    def transaction(self, resource="1", status=200, suffix=""):
        return {"fullPath": f"/api/v1/admin/custom-filters/{resource}{suffix}",
                "name": f"/api/v1/admin/custom-filters/{{id}} > Fixture > {status} > application/json"}

    def test_success_uses_the_real_id_even_when_the_named_value_is_its_own(self):
        for suffix in ("", "/test", "?detail=full"):
            transaction = self.transaction(suffix=suffix)
            self.module.on_the_created_custom_filter(transaction)
            self.assertEqual(transaction["fullPath"], "/api/v1/admin/custom-filters/83" + suffix)

    def test_missing_invalid_and_generated_requests_stay_unchanged(self):
        for resource, status in (("2147483647", 404), ("0", 422), ("1", 403), ("12", 200)):
            transaction = self.transaction(resource, status)
            original = transaction.copy()
            self.module.on_the_created_custom_filter(transaction)
            self.assertEqual(transaction, original)
        transaction = self.transaction()
        original = transaction.copy()
        with patch.dict("os.environ", {"CONTRACT_COVERAGE": "1"}):
            self.module.on_the_created_custom_filter(transaction)
        self.assertEqual(transaction, original)

    def test_without_a_successfully_created_fixture_nothing_is_rewritten(self):
        self.hooks.stash.clear()
        transaction = self.transaction()
        original = transaction.copy()
        self.module.on_the_created_custom_filter(transaction)
        self.assertEqual(transaction, original)
