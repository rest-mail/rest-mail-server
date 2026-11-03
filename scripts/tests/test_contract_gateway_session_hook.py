"""The gateway session hooks put the mail client's password in the open body only."""

import importlib.util
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    hooks = types.ModuleType("vertrag_hooks")
    registered = []
    hooks.before = lambda pattern: lambda function: registered.append(pattern) or function
    hooks.after = lambda pattern: lambda function: function
    hooks.stash = {}
    hooks.get_json = lambda transaction: json.loads(transaction["request"]["body"])
    hooks.set_json = lambda transaction, body: transaction["request"].update(body=json.dumps(body))
    spec = importlib.util.spec_from_file_location("contract_gateway_session_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
        spec.loader.exec_module(module)
    return module, registered


class ContractGatewaySessionHookTests(unittest.TestCase):
    def test_open_body_carries_the_app_password_and_keeps_its_protocol(self):
        module, registered = load_hooks()
        self.assertEqual({pattern for pattern in registered if pattern.startswith("/api/v1/gateway-sessions >")}, {
            "/api/v1/gateway-sessions > * > 201 > application/json",
            "/api/v1/gateway-sessions > * > 422 > application/json",
        })
        self.assertFalse([pattern for pattern in registered if "protocol-sign-in" in pattern])
        with patch.object(module, "principals", return_value={"app_password": {
            "address": "mailbox@example.test", "secret": "app:fixture",
        }}):
            for protocol in ("imap", "pop3"):
                with self.subTest(protocol=protocol):
                    transaction = {"request": {"body": json.dumps({"protocol": protocol, "password": "x"}), "headers": {}}}
                    module.sign_in_a_mail_client(transaction)
                    self.assertEqual(json.loads(transaction["request"]["body"]), {
                        "protocol": protocol, "address": "mailbox@example.test", "password": "app:fixture",
                    })
                    self.assertNotIn("Authorization", transaction["request"]["headers"])

    def test_close_sends_the_token_of_a_session_the_gateway_opened(self):
        module, registered = load_hooks()
        self.assertIn("/api/v1/gateway-sessions/current > * > 204", registered)
        with patch.object(module, "open_gateway_session", return_value="rmgs_fixture"):
            transaction = {"request": {"body": "", "headers": {"Authorization": "Bearer session"}}}
            module.close_a_mail_clients_session(transaction)
        self.assertEqual(transaction["request"]["headers"]["Authorization"], "Bearer rmgs_fixture")


if __name__ == "__main__":
    unittest.main()
