"""deleteDomain's hook deletes only the domain the run created (#388)."""

import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    hooks = types.ModuleType("vertrag_hooks")
    hooks.stash = {}
    registered = []
    hooks.before = lambda pattern: lambda function: registered.append(pattern) or function
    hooks.after = lambda pattern: lambda function: function
    spec = importlib.util.spec_from_file_location("contract_delete_domain_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
        spec.loader.exec_module(module)
    return module, hooks, registered


def delete(path):
    return {"fullPath": path, "request": {"body": "", "headers": {}}}


class ContractDeleteDomainHookTests(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, registered = load_hooks()
        self.assertIn("deleteDomain", registered)
        self.hooks.stash["domain_id"] = 42

    def test_the_created_domain_is_deleted(self):
        transaction = delete("/api/v1/admin/domains/42")
        self.module.delete_only_the_created_domain(transaction)
        self.assertNotIn("skip", transaction)

    def test_another_domain_is_not_deleted(self):
        # Coverage's minimum probe: domain 1 is the fixture admin's.
        transaction = delete("/api/v1/admin/domains/1")
        self.module.delete_only_the_created_domain(transaction)
        self.assertTrue(transaction.get("skip"))

    def test_with_no_created_domain_nothing_is_deleted(self):
        self.hooks.stash.clear()
        transaction = delete("/api/v1/admin/domains/1")
        self.module.delete_only_the_created_domain(transaction)
        self.assertTrue(transaction.get("skip"))

    def test_the_missing_example_is_sent_for_its_404(self):
        transaction = delete("/api/v1/admin/domains/2147483647")
        self.module.delete_only_the_created_domain(transaction)
        self.assertNotIn("skip", transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/2147483647")

    def test_has_accounts_names_the_fixture_domain_which_is_refused(self):
        transaction = delete("/api/v1/admin/domains/6")
        transaction.update({"protocol": "http:", "host": "api", "port": "8080",
                            "name": "/api/v1/admin/domains/{id} > Delete a domain > 409 > application/json"})
        with patch.object(self.module, "admin_token", return_value="t"), \
                patch.object(self.module, "hosted_domain_id", return_value=3) as hosted:
            self.module.delete_only_the_created_domain(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/3")
        self.assertEqual(hosted.call_args.args[2], "restmail.test")
        self.assertNotIn("skip", transaction)

    def test_malformed_ids_are_sent_for_their_400(self):
        for domain_id in ("0", "-1", "abc", "1.5"):
            with self.subTest(id=domain_id):
                transaction = delete("/api/v1/admin/domains/" + domain_id)
                self.module.delete_only_the_created_domain(transaction)
                self.assertNotIn("skip", transaction)
                self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/" + domain_id)


if __name__ == "__main__":
    unittest.main()
