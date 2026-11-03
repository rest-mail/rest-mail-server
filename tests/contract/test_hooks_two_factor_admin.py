"""The operations that take a two-factor session run as the two-factor
administrator, and deleteGrant deletes only a grant made for it (#1334)."""

import base64
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
# The name a before hook receives: Vertrag 0.42.0 adds "coverage: " only to the result.
KEY_NAME = "/api/v1/accounts/{id}/api-keys > Make a scoped API key > 201 > application/json"


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
                          "activateGrant", "deactivateGrant", "makeGrantEligible", "createAccount", "linkAccount",
                          "unlinkAccount", "revokeCredential", "updateMailbox", "deleteMailbox", "updateDomain",
                          "deleteDomain", "inviteAccount", "listAuditEntries", "verifyAuditLog",
                          "releaseQuarantine", "changeOwnPassword", "disableTwoFactor"):
            self.assertIn("send_as_the_two_factor_admin", self.registered[operation])
        self.assertIn("an_api_key_from_the_two_factor_admin", self.registered["createAPIKey"])
        self.assertIn("an_api_key_of_its_own_to_edit", self.registered["updateAPIKey"])
        self.assertIn("an_app_password_from_the_two_factor_admin", self.registered["createClientSecret"])
        # The swap runs before the hook that confirms with the maker's password,
        # after only the clean start of the own-data run (#1385).
        self.assertEqual(
            self.registered["createAppPassword"][:2],
            ["a_credential_to_manage", "send_as_the_two_factor_admin"],
        )
        self.assertIn("confirm_with_the_admin_password", self.registered["createAppPassword"])

    def test_every_operation_that_takes_a_second_factor_is_hooked(self):
        # An operation whose refusal of a session without a second factor is
        # proven in process (its pair requires a principal or state) is sent
        # as the two-factor administrator; one whose refusal the live run
        # proves itself, as the fixture admin, is not (#1332).
        examples = json.loads((ROOT / "openapi-example.json").read_text())

        def refusals(pairs):
            def code(pair):
                body = pair["response"].get("body")
                error = body.get("error") if isinstance(body, dict) else None
                return error.get("code") if isinstance(error, dict) else None
            return [pair for pair in pairs.values()
                    if code(pair) in ("two_factor_required", "recent_two_factor_required")]
        guarded = {op for op, pairs in examples.items()
                   if refusals(pairs) and all(pair.get("requires") for pair in refusals(pairs))}
        live = {op for op, pairs in examples.items()
                if any(not pair.get("requires") for pair in refusals(pairs))}
        hooked = {op for op, names in self.registered.items()
                  if {"send_as_the_two_factor_admin", "an_api_key_from_the_two_factor_admin",
                      "an_api_key_of_its_own_to_edit", "an_app_password_from_the_two_factor_admin"} & set(names)}
        self.assertLessEqual(guarded, hooked)
        self.assertFalse(live & hooked)

    def test_a_client_secret_is_made_with_the_two_factor_admins_password(self):
        body = {"name": "Robot", "password": "current-password"}
        t = transaction("/api/v1/accounts/1/client-secrets", session(self.module.ADMIN), json.dumps(body))
        self.module.an_app_password_from_the_two_factor_admin(t)
        self.assertEqual(json.loads(t["request"]["body"]),
                         {"name": "Robot", "password": self.module.mfa_admin.PASSWORD})
        self.assertEqual(t["request"]["headers"]["Authorization"], "Bearer two-factor-token")

    def test_an_app_password_is_confirmed_with_its_makers_password(self):
        for address, password in ((self.module.mfa_admin.ADDRESS, self.module.mfa_admin.PASSWORD),
                                  (self.module.ADMIN, self.module.ADMIN_PASSWORD)):
            with self.subTest(address=address):
                body = {"name": "Thunderbird", "password": "current-password", "expires_at": "x"}
                t = transaction("/api/v1/accounts/1/credentials", session(address), json.dumps(body))
                self.module.confirm_with_the_admin_password(t)
                self.assertEqual(json.loads(t["request"]["body"]), {"name": "Thunderbird", "password": password})

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
        with patch.object(self.module, "mailbox_address", return_value="a@restmail.test"):
            self.module.an_api_key_from_the_two_factor_admin(t)
        self.assertEqual(json.loads(t["request"]["body"])["password"], self.module.mfa_admin.PASSWORD)
        self.assertEqual(t["request"]["headers"]["Authorization"], "Bearer two-factor-token")

    def test_the_key_is_scoped_to_its_accounts_own_mailbox(self):
        permissions = [{"capability": "mail:read", "scope": "mailbox:robot@example.com"}]
        body = {"name": "Invoice robot", "password": "current-password", "permissions": permissions}
        t = transaction("/api/v1/accounts/1/api-keys", session(self.module.ADMIN), json.dumps(body))
        with patch.object(self.module, "admin_token", return_value="admin-token"), \
                patch.object(self.module.urllib.request, "urlopen") as urlopen:
            urlopen.return_value.__enter__.return_value = io.BytesIO(json.dumps(
                {"data": [{"id": 2, "address": "b@restmail.test"}, {"id": 1, "address": "a@restmail.test"}]}).encode())
            self.module.an_api_key_from_the_two_factor_admin(t)
        self.assertEqual(json.loads(t["request"]["body"])["permissions"],
                         [{"capability": "mail:read", "scope": "mailbox:a@restmail.test"}])
        request = urlopen.call_args.args[0]
        self.assertEqual(request.full_url, "http://api:8080/api/v1/admin/mailboxes?limit=200&offset=0")
        self.assertEqual(request.headers["Authorization"], "Bearer admin-token")

    def test_a_key_for_no_account_keeps_its_scopes(self):
        permissions = [{"capability": "mail:read", "scope": "mailbox:robot@example.com"}]
        body = {"name": "Invoice robot", "password": "current-password", "permissions": permissions}
        t = transaction("/api/v1/accounts/2147483647/api-keys", session(self.module.ADMIN), json.dumps(body))
        with patch.object(self.module, "admin_token", return_value="admin-token"), \
                patch.object(self.module.urllib.request, "urlopen") as urlopen:
            urlopen.return_value.__enter__.return_value = io.BytesIO(json.dumps({"data": []}).encode())
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

    def test_the_missing_grant_example_gets_a_fresh_second_factor(self):
        self.assertIn("a_fresh_second_factor_for_a_missing_grant", self.registered["activateGrant"])
        self.assertIn("a_fresh_second_factor_for_a_missing_grant", self.registered["makeGrantEligible"])
        with patch.object(self.module.mfa_admin, "factor", return_value={"recovery_code": "AAAA-BBBB"}) as factor:
            for action in ("activate", "make-eligible"):
                body = {"reason": "r", "totp_code": "123456"}
                t = transaction("/api/v1/admin/grants/2147483647/" + action, session(self.module.mfa_admin.ADDRESS),
                                json.dumps(body))
                self.module.a_fresh_second_factor_for_a_missing_grant(t)
                self.assertEqual(json.loads(t["request"]["body"]), {"reason": "r", "recovery_code": "AAAA-BBBB"})
            for path, who in (("/api/v1/admin/grants/5/activate", self.module.mfa_admin.ADDRESS),
                              ("/api/v1/admin/grants/2147483647/activate", self.module.ADMIN)):
                body = json.dumps({"totp_code": "123456"})
                t = transaction(path, session(who), body)
                self.module.a_fresh_second_factor_for_a_missing_grant(t)
                self.assertEqual(t["request"]["body"], body)
            self.assertEqual(factor.call_count, 2)


class CoverageOfAPIKeysTests(unittest.TestCase):
    """Vertrag ends a generated probe whose body a hook changed, so in the coverage
    process these hooks change only the session and the path."""

    KEYS = {
        "password": "x",
        "permissions": [{"capability": "mail:read", "scope": "mailbox:contract-probe-admin@restmail.test"}],
    }

    def setUp(self):
        self.module, _, self.registered = load_hooks()
        patch.dict(os.environ, {"CONTRACT_COVERAGE": "1"}).start()
        patch.object(self.module, "probe_admin_token", return_value="probe-token").start()
        patch.object(self.module, "mailbox_id", return_value=77).start()
        patch.object(self.module.time, "sleep").start()
        self.addCleanup(patch.stopall)

    def probe(self, path, body, authorization=None):
        """A request after its transaction's first, under the name a hook receives."""
        sent = transaction(path, authorization or session(self.module.ADMIN), body)
        sent["name"] = KEY_NAME
        self.module.hooks.stash["coverage_sends"] = {KEY_NAME: 1}
        return sent

    def test_the_examples_phase_of_the_coverage_process_makes_its_key_as_any_other(self):
        # revokeAPIKey and updateAPIKey follow its link, on the account they name.
        body = {"name": "Invoice robot", "password": "current-password",
                "permissions": [{"capability": "mail:read", "scope": "mailbox:robot@example.com"}]}
        first = transaction("/api/v1/accounts/1/api-keys", session(self.module.ADMIN), json.dumps(body))
        first["name"] = KEY_NAME
        self.module.hooks.stash.pop("coverage_sends", None)
        with patch.object(self.module, "mfa_admin_token", return_value="two-factor-token"), \
                patch.object(self.module, "mailbox_address", return_value="a@restmail.test"):
            self.module.an_api_key_from_the_two_factor_admin(first)
        self.assertEqual(first["fullPath"], "/api/v1/accounts/1/api-keys")
        self.assertEqual(first["request"]["headers"]["Authorization"], "Bearer two-factor-token")
        self.assertEqual(json.loads(first["request"]["body"])["password"], self.module.mfa_admin.PASSWORD)
        second = transaction("/api/v1/accounts/1/api-keys", session(self.module.ADMIN), json.dumps(body))
        second["name"] = KEY_NAME
        self.module.an_api_key_from_the_two_factor_admin(second)
        self.assertEqual(second["fullPath"], "/api/v1/accounts/77/api-keys")
        self.assertEqual(second["request"]["body"], json.dumps(body))

    def test_a_key_probe_keeps_its_body_and_goes_to_the_probe_admins_own_account(self):
        for raw in (json.dumps(dict(self.KEYS, name="!")), json.dumps(dict(self.KEYS, name="")), "[]", '{"name":12345}'):
            with self.subTest(raw=raw):
                t = self.probe("/api/v1/accounts/1/api-keys", raw)
                self.module.an_api_key_from_the_two_factor_admin(t)
                self.assertEqual(t["request"]["body"], raw)
                self.assertEqual(t["fullPath"], "/api/v1/accounts/77/api-keys")
                self.assertEqual(t["request"]["headers"]["Authorization"], "Bearer probe-token")

    def test_an_id_that_is_not_one_reaches_the_handler(self):
        for account in ("0", "1.5", "vertrag-not-a-number"):
            with self.subTest(account=account):
                t = self.probe("/api/v1/accounts/" + account + "/api-keys", "{}")
                self.module.an_api_key_from_the_two_factor_admin(t)
                self.assertEqual(t["fullPath"], "/api/v1/accounts/" + account + "/api-keys")

    def test_another_principal_keeps_its_session_and_account(self):
        ordinary = session("contract-ordinary@restmail.test")
        t = self.probe("/api/v1/accounts/1/api-keys", "{}", ordinary)
        self.module.an_api_key_from_the_two_factor_admin(t)
        self.assertEqual((t["fullPath"], t["request"]["headers"]["Authorization"]), ("/api/v1/accounts/1/api-keys", ordinary))

    def test_a_key_edit_probe_edits_a_key_of_its_own(self):
        raw = json.dumps(self.KEYS)
        t = self.probe("/api/v1/accounts/1/api-keys/2", raw)
        with patch.object(self.module.urllib.request, "urlopen") as urlopen:
            urlopen.return_value.__enter__.return_value = io.BytesIO(json.dumps({"data": {"id": 91}}).encode())
            self.module.an_api_key_of_its_own_to_edit(t)
        self.assertEqual(t["request"]["body"], raw)
        self.assertEqual(t["fullPath"], "/api/v1/accounts/77/api-keys/91")
        made = urlopen.call_args.args[0]
        self.assertEqual(made.full_url, "http://api:8080/api/v1/accounts/77/api-keys")
        self.assertEqual(made.headers["Authorization"], "Bearer probe-token")
        # The setup's password is the fixture's, and the route still checks it.
        self.assertEqual(json.loads(made.data)["password"], self.module.mfa_admin.PROBE_PASSWORD)

    def test_the_missing_key_and_a_malformed_id_make_no_key(self):
        for credential in ("2147483647", "0", "1.5"):
            with self.subTest(credential=credential), patch.object(self.module.urllib.request, "urlopen") as urlopen:
                t = self.probe("/api/v1/accounts/1/api-keys/" + credential, "{}")
                self.module.an_api_key_of_its_own_to_edit(t)
                urlopen.assert_not_called()
                self.assertEqual(t["fullPath"], "/api/v1/accounts/77/api-keys/" + credential)

    def test_a_probe_the_schema_forbids_reaches_the_app_password_route_as_drawn(self):
        for raw in ('{"expires_at":"2099-01-01T10:00:00Z"}', '{"name":12345}', '{"name":"x","password":12345}',
                    '{"name":"x","vertragUnexpected":"extra"}'):
            with self.subTest(raw=raw):
                t = self.probe("/api/v1/accounts/1/credentials", raw, session(self.module.mfa_admin.ADDRESS))
                self.module.confirm_with_the_admin_password(t)
                self.assertEqual(t["request"]["body"], raw)

    def test_a_malformed_account_id_reaches_the_app_password_route_as_drawn(self):
        raw = '{"name":"iPhone Mail"}'
        t = self.probe("/api/v1/accounts/0/credentials", raw, session(self.module.mfa_admin.ADDRESS))
        self.module.confirm_with_the_admin_password(t)
        self.assertEqual(t["request"]["body"], raw)

    def test_a_well_formed_app_password_request_still_gets_the_password(self):
        # The description lets it leave the password out, and the handler refuses
        # that with 403, so the probe ends here and lost-probes-baseline.txt holds it.
        t = self.probe("/api/v1/accounts/1/credentials", '{"name":"x"}', session(self.module.mfa_admin.ADDRESS))
        self.module.confirm_with_the_admin_password(t)
        self.assertEqual(json.loads(t["request"]["body"]), {"name": "x", "password": self.module.mfa_admin.PASSWORD})


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
