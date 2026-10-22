"""Select getDomain's missing ID without repairing successful boundary probes."""

import copy
import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


class MissingDomainTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = hooks.after = lambda *args: lambda function: function
        spec = importlib.util.spec_from_file_location("missing_domain_hooks", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)

    def test_refusal_uses_documented_missing_parameter_and_preserves_credentials(self):
        with open(ROOT / "openapi.yaml") as file:
            operation = yaml.safe_load(file)["paths"]["/api/v1/admin/domains/{id}"]["get"]
        missing = operation["parameters"][0]["examples"]["missing"]["value"]
        self.assertIn("missing", operation["responses"]["404"]["content"]["application/json"]["examples"])
        transaction = {
            "name": "/api/v1/admin/domains/{id} > Get a domain by ID > 404 > application/json",
            "fullPath": "/api/v1/admin/domains/42",
            "request": {"headers": {"Authorization": "Bearer test-token"}},
        }
        request = copy.deepcopy(transaction["request"])
        self.module.get_a_missing_domain(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/%d" % missing)
        self.assertEqual(transaction["request"], request)

    def test_success_and_its_boundary_probes_are_unchanged(self):
        for status in ("200", "401"):
            for value in ("42", "0", "-1", "not-an-id", "1.5", "2147483647"):
                with self.subTest(status=status, value=value):
                    transaction = {
                        "name": "/api/v1/admin/domains/{id} > Get a domain by ID > %s > application/json" % status,
                        "fullPath": "/api/v1/admin/domains/" + value,
                        "request": {"headers": {}},
                    }
                    original = copy.deepcopy(transaction)
                    self.module.get_a_missing_domain(transaction)
                    self.assertEqual(transaction, original)


if __name__ == "__main__":
    unittest.main()
