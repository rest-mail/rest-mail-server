"""App-password name refusals survive the fixture password hook."""

import copy
import importlib.util
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


class ContractCredentialHookTests(unittest.TestCase):
    def test_named_requests_keep_their_names_and_principal(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = hooks.after = lambda *args: lambda function: function
        hooks.get_json = lambda transaction: json.loads(transaction["request"]["body"])
        hooks.set_json = lambda transaction, body: transaction["request"].update(body=json.dumps(body))
        spec = importlib.util.spec_from_file_location("contract_credential_hooks", ROOT / "tests/contract/hooks.py")
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(module)
        with open(ROOT / "openapi.yaml") as source:
            operation = yaml.safe_load(source)["paths"]["/api/v1/accounts/{id}/credentials"]["post"]
        examples = operation["requestBody"]["content"]["application/json"]["examples"]
        for name, example in examples.items():
            with self.subTest(example=name):
                body = example["value"]
                transaction = {
                    "fullPath": "/api/v1/accounts/17/credentials",
                    "request": {"body": json.dumps(body), "headers": {"Authorization": "Bearer caller"}},
                }
                before = copy.deepcopy(transaction)
                module.confirm_with_the_admin_password(transaction)
                self.assertEqual(json.loads(transaction["request"]["body"]), dict(body, password=module.ADMIN_PASSWORD))
                self.assertEqual(transaction["request"]["headers"], before["request"]["headers"])
                self.assertEqual(transaction["fullPath"], before["fullPath"])


if __name__ == "__main__":
    unittest.main()
