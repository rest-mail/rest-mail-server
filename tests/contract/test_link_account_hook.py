"""Link examples keep the invalid fields that provoke their paired responses."""

import copy
import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


class ContractLinkAccountHookTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        self.registered = {}

        def before(pattern):
            def register(function):
                self.registered[pattern] = function
                return function
            return register

        hooks.before = before
        hooks.after = lambda pattern: lambda function: function
        hooks.get_json = lambda transaction: copy.deepcopy(transaction["request"]["body"])
        hooks.set_json = lambda transaction, body: transaction["request"].update(body=body)
        spec = importlib.util.spec_from_file_location("contract_link_account_hooks", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)

    def apply_hook(self, body, name="coverage: linkAccount"):
        transaction = {
            "name": name,
            "request": {
                "body": copy.deepcopy(body),
                "uri": "http://example.test/api/v1/accounts/link",
                "headers": {"Authorization": "Bearer ordinary-account"},
            },
        }
        original = copy.deepcopy(transaction)
        self.registered["linkAccount"](transaction)
        self.assertEqual(transaction["name"], original["name"])
        self.assertEqual(transaction["request"]["uri"], original["request"]["uri"])
        self.assertEqual(transaction["request"]["headers"], original["request"]["headers"])
        return transaction["request"]["body"]

    def test_named_requests_keep_their_invalid_fields(self):
        with open(ROOT / "openapi.yaml") as f:
            operation = yaml.safe_load(f)["paths"]["/api/v1/accounts/link"]["post"]
        requests = operation["requestBody"]["content"]["application/json"]["examples"]
        for name, example in requests.items():
            with self.subTest(example=name):
                expected = copy.deepcopy(example["value"])
                if expected.get("password") == "supplied-by-contract-hook":
                    expected["password"] = self.module.LINKABLE_PASSWORD
                self.assertEqual(self.apply_hook(example["value"], name), expected)

    def test_hook_preserves_coverage_mutations_and_other_fields(self):
        bodies = [
            [], None, {}, {"address": ""}, {"address": None},
            {"address": "eve@restmail.test", "password": ""},
            {"address": "eve@restmail.test", "password": None},
            {"address": "eve@restmail.test", "password": 123},
            {"address": "eve@restmail.test", "password": "wrong-password"},
            {"password": "supplied-by-contract-hook", "display_name": "Keep me", "totp_code": "123456"},
            {"address": "not-an-address", "password": "supplied-by-contract-hook", "recovery_code": "keep-me"},
        ]
        for body in bodies:
            with self.subTest(body=body):
                expected = copy.deepcopy(body)
                if isinstance(expected, dict) and expected.get("password") == "supplied-by-contract-hook":
                    expected["password"] = self.module.LINKABLE_PASSWORD
                self.assertEqual(self.apply_hook(body), expected)


if __name__ == "__main__":
    unittest.main()
