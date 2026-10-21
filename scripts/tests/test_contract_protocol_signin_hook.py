"""Protocol sign-in's contract hook supplies Basic auth without changing examples."""

import importlib.util
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


class ContractProtocolSignInHookTests(unittest.TestCase):
    def test_basic_credential_leaves_each_request_body_unchanged(self):
        hooks = types.ModuleType("vertrag_hooks")
        registered = []
        hooks.before = lambda pattern: lambda function: registered.append(pattern) or function
        hooks.after = lambda pattern: lambda function: function
        hooks.stash = {}
        hooks.get_json = lambda transaction: json.loads(transaction["request"]["body"])
        hooks.set_json = lambda transaction, body: transaction["request"].update(body=json.dumps(body))
        spec = importlib.util.spec_from_file_location("contract_protocol_hooks", ROOT / "tests/contract/hooks.py")
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(module)
        self.assertEqual({pattern for pattern in registered if pattern.startswith("/api/v1/auth/protocol-sign-in >")}, {
            "/api/v1/auth/protocol-sign-in > * > 200 > application/json",
            "/api/v1/auth/protocol-sign-in > * > 400 > application/json",
            "/api/v1/auth/protocol-sign-in > * > 422 > application/json",
        })

        with patch.object(module, "principals", return_value={"app_password": {
            "address": "mailbox@example.test", "secret": "app:fixture",
        }}):
            for body in ('{"protocol":"imap"}', "[]", "{}"):
                with self.subTest(body=body):
                    transaction = {"request": {"body": body, "headers": {}}}
                    module.sign_in_a_mail_client(transaction)
                    self.assertEqual(transaction["request"]["body"], body)
                    self.assertEqual(transaction["request"]["headers"]["Authorization"],
                                     module.basic("mailbox@example.test", "app:fixture"))


if __name__ == "__main__":
    unittest.main()
