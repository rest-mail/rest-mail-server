"""Link examples keep the invalid fields that provoke their paired responses."""

import copy
import importlib.util
import io
import json
from pathlib import Path
import sys
import types
import unittest
import urllib.error
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


class ContractLinkAccountHookTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.stash = {}
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
        self.requests = []
        self.unlink_status = 204

        def urlopen(request, timeout):
            self.requests.append(request)
            if request.get_method() == "DELETE" and self.unlink_status != 204:
                raise urllib.error.HTTPError(request.full_url, self.unlink_status, "unlink refused", {}, None)
            response = io.BytesIO(json.dumps({"data": [
                {"id": 2, "address": "eve@restmail.test"},
                {"id": 30, "address": "someone@example.com"},
            ]}).encode())
            response.status = 204 if request.get_method() == "DELETE" else 200
            return response

        patcher = patch.object(self.module.urllib.request, "urlopen", side_effect=urlopen)
        patcher.start()
        self.addCleanup(patcher.stop)
        patcher = patch.object(self.module, "admin_token", return_value="fixture-setup")
        patcher.start()
        self.addCleanup(patcher.stop)

    def apply_hook(self, body, name="coverage: linkAccount"):
        transaction = {
            "name": name,
            "protocol": "http:", "host": "example.test", "port": "8080",
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

    def test_repeated_generated_proof_keeps_body_and_unlinks_as_the_caller(self):
        body = {"address": "someone@example.com", "password": "x", "display_name": ""}
        for _ in range(2):
            self.assertEqual(self.apply_hook(body), body)
        self.assertEqual([request.get_method() for request in self.requests], ["GET", "DELETE", "DELETE"])
        for request in self.requests[1:]:
            self.assertEqual(request.full_url, "http://example.test:8080/api/v1/accounts/30")
            self.assertEqual(request.get_header("Authorization"), "Bearer ordinary-account")
        self.assertEqual(self.requests[0].get_header("Authorization"), "Bearer fixture-setup")

    def test_unlinked_fixture_is_ready_for_its_first_proof(self):
        self.unlink_status = 404
        body = {"address": "someone@example.com", "password": "x"}
        self.assertEqual(self.apply_hook(body), body)

    def test_cleanup_failures_are_reported(self):
        for status in (401, 403, 500):
            with self.subTest(status=status):
                self.unlink_status = status
                with self.assertRaises(urllib.error.HTTPError):
                    self.apply_hook({"address": "someone@example.com", "password": "x"})

    def test_invalid_proofs_do_not_prepare_or_unlink_accounts(self):
        for body in ({}, {"address": "someone@example.com"},
                     {"address": "someone@example.com", "password": ""},
                     {"address": "someone@example.com", "password": 123},
                     {"address": "someone@example.com", "password": "wrong"},
                     {"address": "not-an-email", "password": "x"}):
            with self.subTest(body=body):
                self.assertEqual(self.apply_hook(body), body)
        self.assertEqual(self.requests, [])

    def test_missing_session_does_not_prepare_or_unlink_accounts(self):
        transaction = {"request": {"body": {"address": "someone@example.com", "password": "x"}}}
        original = copy.deepcopy(transaction)
        self.registered["linkAccount"](transaction)
        self.assertEqual(transaction, original)
        self.assertEqual(self.requests, [])

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
