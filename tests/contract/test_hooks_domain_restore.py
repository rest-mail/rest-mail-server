import unittest
from unittest.mock import patch

from test_hooks_delete_domain import load_hooks


class DomainRestoreHookTests(unittest.TestCase):
    def setUp(self):
        self.module, _, registered = load_hooks()
        self.assertIn("restoreDomain", registered)

    def test_success_restores_only_its_archived_domain(self):
        transaction = {"fullPath": "/api/v1/admin/domains/1/restore?x=1", "name": "Restore > 200 > application/json"}
        with patch.object(self.module, "asked_for_a_clean_start", return_value=False), patch.object(self.module, "fixture_call", return_value={"id": 42}) as setup, patch.object(self.module, "origin_of", return_value="https://api"), patch.object(self.module, "as_the_two_factor_admin_call") as retire:
            self.module.an_archived_domain_to_restore(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/42/restore?x=1")
        self.assertEqual(setup.call_count, 1)
        retire.assert_called_once_with("https://api", "DELETE", "/api/v1/admin/domains/42")

    def test_name_conflict_is_a_real_recreated_name(self):
        transaction = {"fullPath": "/api/v1/admin/domains/1/restore", "name": "Restore > 409 > application/json"}
        with patch.object(self.module, "asked_for_a_clean_start", return_value=False), patch.object(self.module, "fixture_call", return_value={"id": 42}) as setup, patch.object(self.module, "origin_of", return_value="https://api"), patch.object(self.module, "as_the_two_factor_admin_call"), patch.object(self.module.time, "time_ns", return_value=123):
            self.module.an_archived_domain_to_restore(transaction)
        self.assertEqual(setup.call_count, 2)
        self.assertEqual(setup.call_args_list[0].args[3], {"name": "contract-restore-123.test"})
        self.assertEqual(setup.call_args_list[0].args, setup.call_args_list[1].args)

    def test_malformed_ids_and_missing_targets_stay_malformed_or_missing(self):
        for domain_id, status in (("0", 200), ("-1", 200), ("abc", 200), ("2147483647", 404)):
            transaction = {"fullPath": f"/api/v1/admin/domains/{domain_id}/restore", "name": f"Restore > {status} > application/json"}
            before = dict(transaction)
            with patch.object(self.module, "fixture_call") as setup:
                self.module.an_archived_domain_to_restore(transaction)
            self.assertEqual(transaction, before)
            setup.assert_not_called()
