"""The operations that take a two-factor session run as the two-factor
administrator, and deleteGrant deletes only a grant made for it (#1334)."""

import base64
import importlib.util
import io
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    hooks = types.ModuleType("vertrag_hooks")
    hooks.stash = {}
    registered = {}

    def before(pattern):
        def register(function):
            registered.setdefault(pattern, []).append(function.__name__)
            return function
        return register

    hooks.before = before
    hooks.after = lambda pattern: lambda function: function
    hooks.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"] or "{}")
    hooks.set_json = lambda transaction, value, which="request": transaction[which].__setitem__("body", json.dumps(value))
    spec = importlib.util.spec_from_file_location("contract_two_factor_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
        spec.loader.exec_module(module)
    return module, hooks, registered


def session(address):
    claims = base64.urlsafe_b64encode(json.dumps({"account_id": 1, "address": address}).encode()).decode().rstrip("=")
    return "Bearer header." + claims + ".signature"


def transaction(path, authorization=None, body=""):
    headers = {} if authorization is None else {"Authorization": authorization}
    return {"protocol": "http:", "host": "api", "port": "8080", "fullPath": path,
            "request": {"method": "POST", "headers": headers, "body": body}}


class TwoFactorAdminHookTests(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, self.registered = load_hooks()
        self.mfa = patch.object(self.module, "mfa_admin_token", return_value="two-factor-token").start()
        self.addCleanup(patch.stopall)

    def test_every_two_factor_operation_is_hooked(self):
        for operation in ("listAPIKeys", "revokeAPIKey", "confirmTwoFactor", "createGrant", "deleteGrant",
                          "activateGrant", "deactivateGrant", "makeGrantEligible"):
            self.assertIn("send_as_the_two_factor_admin", self.registered[operation])
        self.assertIn("an_api_key_from_the_two_factor_admin", self.registered["createAPIKey"])

    def test_the_fixture_admins_session_is_replaced(self):
        t = transaction("/api/v1/admin/grants", session(self.module.ADMIN))
        self.module.send_as_the_two_factor_admin(t)
        self.assertEqual(t["request"]["headers"]["Authorization"], "Bearer two-factor-token")
        self.mfa.assert_called_once_with("http://api:8080")

    def test_other_principals_keep_their_credential(self):
        for authorization in (None, session("contract-ordinary@restmail.test"), "Basic YTpi", "Bearer not-a-jwt"):
            with self.subTest(authorization=authorization):
                t = transaction("/api/v1/admin/grants", authorization)
                self.module.send_as_the_two_factor_admin(t)
                self.assertEqual(t["request"]["headers"].get("Authorization"), authorization)
        self.mfa.assert_not_called()

    def test_an_api_key_is_made_with_the_two_factor_admins_password(self):
        body = {"name": "Invoice robot", "password": "current-password", "permissions": []}
        t = transaction("/api/v1/accounts/1/api-keys", session(self.module.ADMIN), json.dumps(body))
        self.module.an_api_key_from_the_two_factor_admin(t)
        self.assertEqual(json.loads(t["request"]["body"])["password"], self.module.mfa_admin.PASSWORD)
        self.assertEqual(t["request"]["headers"]["Authorization"], "Bearer two-factor-token")

    def test_the_fixture_admins_key_is_scoped_to_its_own_mailbox(self):
        permissions = [{"capability": "mail:read", "scope": "mailbox:robot@example.com"}]
        body = {"name": "Invoice robot", "password": "current-password", "permissions": permissions}
        t = transaction("/api/v1/accounts/1/api-keys", session(self.module.ADMIN), json.dumps(body))
        self.module.an_api_key_from_the_two_factor_admin(t)
        self.assertEqual(json.loads(t["request"]["body"])["permissions"],
                         [{"capability": "mail:read", "scope": "mailbox:" + self.module.ADMIN}])

    def test_a_key_for_another_account_keeps_its_scopes(self):
        permissions = [{"capability": "mail:read", "scope": "mailbox:robot@example.com"}]
        body = {"name": "Invoice robot", "password": "current-password", "permissions": permissions}
        t = transaction("/api/v1/accounts/7/api-keys", session(self.module.ADMIN), json.dumps(body))
        self.module.an_api_key_from_the_two_factor_admin(t)
        self.assertEqual(json.loads(t["request"]["body"])["permissions"], permissions)

    def test_the_body_array_refusal_is_sent_as_it_is(self):
        t = transaction("/api/v1/accounts/1/api-keys", session(self.module.ADMIN), "[]")
        self.module.an_api_key_from_the_two_factor_admin(t)
        self.assertEqual(t["request"]["body"], "[]")

    def test_another_principals_key_keeps_its_password(self):
        body = json.dumps({"name": "n", "password": "current-password"})
        t = transaction("/api/v1/accounts/1/api-keys", session("contract-ordinary@restmail.test"), body)
        self.module.an_api_key_from_the_two_factor_admin(t)
        self.assertEqual(t["request"]["body"], body)


class DeleteGrantHookTests(unittest.TestCase):
    def setUp(self):
        self.module, _, _ = load_hooks()
        patch.object(self.module, "mfa_admin_token", return_value="two-factor-token").start()
        patch.object(self.module, "an_account_without_credentials", return_value=55).start()
        self.urlopen = patch.object(self.module.urllib.request, "urlopen").start()
        self.urlopen.return_value.__enter__.return_value = io.BytesIO(json.dumps({"data": {"id": 31}}).encode())
        self.addCleanup(patch.stopall)

    def test_grant_one_becomes_a_grant_made_for_it(self):
        t = transaction("/api/v1/admin/grants/1")
        self.module.delete_a_grant_made_for_it(t)
        self.assertEqual(t["fullPath"], "/api/v1/admin/grants/31")
        request = self.urlopen.call_args.args[0]
        self.assertEqual((request.get_method(), request.full_url), ("POST", "http://api:8080/api/v1/admin/grants"))
        self.assertEqual(request.headers["Authorization"], "Bearer two-factor-token")
        self.assertEqual(json.loads(request.data), {"holder": {"kind": "account", "id": 55}, "role": "admin",
                                                    "scope": "domain:restmail.test", "mode": "eligible"})

    def test_other_ids_are_sent_as_they_are(self):
        for grant_id in ("0", "9", "2147483647", "abc"):
            with self.subTest(id=grant_id):
                t = transaction("/api/v1/admin/grants/" + grant_id)
                self.module.delete_a_grant_made_for_it(t)
                self.assertEqual(t["fullPath"], "/api/v1/admin/grants/" + grant_id)
        self.urlopen.assert_not_called()


if __name__ == "__main__":
    unittest.main()
