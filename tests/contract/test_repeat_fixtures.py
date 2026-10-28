"""Repeated example setup changes only the disposable admin's named fixtures."""
import copy
import io
import json
import unittest
import urllib.error
import urllib.parse
from unittest.mock import patch

import repeat_fixtures


class Response(io.BytesIO):
    def __init__(self, status, value=None):
        super().__init__(json.dumps(value).encode() if value is not None else b"")
        self.status = status

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()


class RepeatFixtureTests(unittest.TestCase):
    def setUp(self):
        self.config = {
            "endpoint": "http://fixture.test:8080",
            "auth": {"login": {"path": "/api/v1/auth/login", "body": {
                "email": "admin@restmail.test", "password": "fixture", "client": "native"}}},
        }
        self.accounts = [{"id": 1, "address": "other@restmail.test"}, {"id": 4, "address": "admin@restmail.test"}]
        self.contacts = [{"id": 14, "email": "spam@example.org"}, {"id": 15, "email": "other@example.org"},
                         {"id": 16, "email": "prefixspam@example.org"}]
        self.folder = True
        self.calls = []
        self.folder_error = None

    def api(self, request, timeout):
        self.assertEqual(timeout, 10)
        path = urllib.parse.urlsplit(request.full_url).path
        method = request.get_method()
        self.calls.append((method, path))
        if path == "/api/v1/auth/login":
            self.assertEqual(method, "POST")
            self.assertEqual(json.loads(request.data), self.config["auth"]["login"]["body"])
            return Response(200, {"data": {"access_token": "setup-token"}})
        if method == "DELETE" and path.startswith("/api/v1/admin/mailboxes/"):
            # Deleting an account always takes a session signed in with 2FA.
            self.assertEqual(request.get_header("Authorization"), "Bearer mfa-token")
            account_id = int(path.rsplit("/", 1)[1])
            self.accounts = [a for a in self.accounts if a["id"] != account_id]
            return Response(204)
        self.assertEqual(request.get_header("Authorization"), "Bearer setup-token")
        if method == "GET" and path == "/api/v1/admin/mailboxes":
            return Response(200, {"data": self.accounts})
        if method == "DELETE" and path == "/api/v1/accounts/4/folders/Receipts":
            if self.folder_error or not self.folder:
                raise urllib.error.HTTPError(request.full_url, self.folder_error or 404, "fixture error", {}, None)
            self.folder = False
            return Response(204)
        if method == "GET" and path == "/api/v1/accounts/4/contacts":
            self.assertEqual(urllib.parse.parse_qs(urllib.parse.urlsplit(request.full_url).query), {"search": ["spam@example.org"]})
            return Response(200, {"data": self.contacts})
        if method == "DELETE" and path == "/api/v1/accounts/4/contacts/14":
            self.contacts = [contact for contact in self.contacts if contact["id"] != 14]
            return Response(204)
        self.fail("unexpected fixture mutation: %s %s" % (method, path))

    def test_repeat_resets_only_the_named_fixtures_and_is_idempotent(self):
        before = copy.deepcopy(self.config)
        with patch.object(repeat_fixtures.urllib.request, "urlopen", side_effect=self.api):
            repeat_fixtures.prepare(self.config)
            repeat_fixtures.prepare(self.config)
        self.assertEqual(self.config, before)
        self.assertFalse(self.folder)
        self.assertEqual([contact["id"] for contact in self.contacts], [15, 16])
        self.assertEqual(self.calls.count(("DELETE", "/api/v1/accounts/4/contacts/14")), 1)

    def test_removes_the_created_example_account_with_a_two_factor_session(self):
        self.accounts.append({"id": 9, "address": "new-account@restmail.test"})
        with patch.object(repeat_fixtures.urllib.request, "urlopen", side_effect=self.api), \
                patch.object(repeat_fixtures.mfa_admin, "session", return_value="mfa-token") as mfa:
            repeat_fixtures.prepare(self.config)
            repeat_fixtures.prepare(self.config)
        self.assertNotIn(9, [a["id"] for a in self.accounts])
        self.assertEqual(self.calls.count(("DELETE", "/api/v1/admin/mailboxes/9")), 1)
        self.assertEqual(mfa.call_count, 1)
        self.assertEqual([a["id"] for a in self.accounts], [1, 4])

    def test_other_accounts_are_rejected_before_any_request(self):
        self.config["auth"]["login"]["body"]["email"] = "admin@production.example"
        with patch.object(repeat_fixtures.urllib.request, "urlopen") as send:
            with self.assertRaises(ValueError):
                repeat_fixtures.prepare(self.config)
            send.assert_not_called()

    def test_missing_or_ambiguous_admin_stops_before_deleting_anything(self):
        for accounts in ([], [self.accounts[1], self.accounts[1]]):
            with self.subTest(accounts=accounts):
                self.accounts = accounts
                self.calls.clear()
                with patch.object(repeat_fixtures.urllib.request, "urlopen", side_effect=self.api):
                    with self.assertRaises(RuntimeError):
                        repeat_fixtures.prepare(self.config)
                self.assertFalse(any(method == "DELETE" for method, _ in self.calls))

    def test_setup_errors_propagate_instead_of_hiding_them(self):
        for status in (403, 409, 500):
            with self.subTest(status=status):
                self.folder_error = status
                with patch.object(repeat_fixtures.urllib.request, "urlopen", side_effect=self.api):
                    with self.assertRaises(urllib.error.HTTPError) as error:
                        repeat_fixtures.prepare(self.config)
                self.assertEqual(error.exception.code, status)
                self.assertTrue(self.folder)
                self.assertEqual(len(self.contacts), 3)


if __name__ == "__main__":
    unittest.main()
