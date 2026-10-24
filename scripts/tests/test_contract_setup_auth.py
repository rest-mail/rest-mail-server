"""Fixture setup signs in separately from the principal whose access is tested."""

import copy
import importlib.util
import io
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

ROOT = Path(__file__).resolve().parents[2]


class Response(io.BytesIO):
    def __init__(self, data=None, status=200):
        super().__init__(json.dumps(data).encode())
        self.status = status


class ContractSetupAuthTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = hooks.after = lambda *args: lambda function: function
        hooks.stash = {}
        hooks.get_json = lambda transaction: json.loads(transaction["request"]["body"])
        spec = importlib.util.spec_from_file_location("contract_setup_hooks", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)
        self.pacing = patch.object(self.module.time, "sleep")
        self.wait = self.pacing.start()
        self.addCleanup(self.pacing.stop)
        self.logins = []
        self.calls = []
        self.responses = []
        self.network = patch.object(self.module.urllib.request, "urlopen", side_effect=self.serve)
        self.network.start()
        self.addCleanup(self.network.stop)

    def serve(self, request, timeout):
        self.assertEqual(timeout, 10)
        if request.full_url.endswith("/api/v1/auth/login"):
            self.assertEqual(request.get_method(), "POST")
            self.assertEqual(request.get_header("Content-type"), "application/json")
            self.assertIsNone(request.get_header("Authorization"))
            self.assertEqual(json.loads(request.data), {
                "email": self.module.ADMIN, "password": self.module.ADMIN_PASSWORD, "client": "native",
            })
            self.logins.append(request.full_url)
            return Response({"data": {"access_token": "setup-session"}})
        # Model the server refusing an account password sent as Basic auth.
        if request.get_header("Authorization") != "Bearer setup-session":
            raise HTTPError(request.full_url, 401, "app password required", {}, None)
        self.calls.append(request)
        self.assertTrue(self.responses, "unexpected setup request: " + request.full_url)
        data, status = self.responses.pop(0)
        return Response(data, status)

    def transaction(self, resource, body, headers=None):
        return {
            "protocol": "http:", "host": "fixture.test", "port": "8080",
            "fullPath": "/api/v1/accounts/7/" + resource,
            "request": {"headers": dict(headers or {}), "body": json.dumps(body)},
        }

    def test_rename_prepares_sources_without_replacing_any_principals_credential(self):
        principals = ({}, {"Authorization": "Bearer admin-session"},
                      {"Authorization": "Bearer ordinary-session"},
                      {"Authorization": "Basic app-password"})
        for index, headers in enumerate(principals, 1):
            with self.subTest(headers=headers):
                transaction = self.transaction("folders/old", {"name": "renamed"}, headers)
                original = copy.deepcopy(transaction["request"])
                self.responses = [({}, 201), (None, 204)]
                self.module.give_each_rename_probe_a_folder(transaction)
                self.assertEqual(transaction["request"], original)
                self.assertEqual(transaction["fullPath"],
                                 "/api/v1/accounts/7/folders/contract-rename-%d" % index)
                setup, cleanup = self.calls[-2:]
                self.assertEqual(setup.full_url, "http://fixture.test:8080/api/v1/accounts/7/folders")
                self.assertEqual(json.loads(setup.data), {"name": "contract-rename-%d" % index})
                # The previous probe's destination goes, or this rename is 409.
                self.assertEqual(cleanup.get_method(), "DELETE")
                self.assertEqual(cleanup.full_url, "http://fixture.test:8080/api/v1/accounts/7/folders/renamed")
        self.assertEqual(len(self.logins), 1)

    def test_rename_cleanup_tolerates_an_absent_destination_and_skips_standard_ones(self):
        def absent(request, timeout):
            if request.get_method() == "DELETE":
                self.calls.append(request)
                raise HTTPError(request.full_url, 404, "not found", {}, None)
            return self.serve(request, timeout)
        self.network.stop()
        with patch.object(self.module.urllib.request, "urlopen", side_effect=absent):
            self.responses = [({}, 201)]
            self.module.give_each_rename_probe_a_folder(self.transaction("folders/old", {"name": "Paid receipts"}))
            self.assertTrue(self.calls[-1].full_url.endswith("/folders/Paid%20receipts"))
            for name in ("INBOX", "inbox", "Trash", "", "  ", None, 12):
                with self.subTest(name=name):
                    self.responses = [({}, 201)]
                    self.calls.clear()
                    self.module.give_each_rename_probe_a_folder(self.transaction("folders/old", {"name": name}))
                    self.assertEqual([request.get_method() for request in self.calls], ["POST"])
        self.network.start()

    def test_folder_cleanup_removes_only_a_previous_probe_and_keeps_request_intact(self):
        transaction = self.transaction("folders", {"name": "contract probe"})
        original = copy.deepcopy(transaction)
        self.responses = [({"data": []}, 200), (None, 204)]
        self.module.remove_a_previous_folder_probe(transaction)
        self.module.remove_a_previous_folder_probe(transaction)
        self.assertEqual(transaction, original)
        self.assertEqual([request.get_method() for request in self.calls], ["GET", "DELETE"])
        self.assertTrue(self.calls[-1].full_url.endswith("/folders/contract%20probe"))
        self.assertEqual(len(self.logins), 1)

    def test_preexisting_folder_is_never_claimed_or_deleted(self):
        transaction = self.transaction("folders", {"name": "personal"})
        self.responses = [({"data": [{"name": "personal"}]}, 200)] * 2
        self.module.remove_a_previous_folder_probe(transaction)
        self.module.remove_a_previous_folder_probe(transaction)
        self.assertEqual([request.get_method() for request in self.calls], ["GET", "GET"])

    def test_contact_cleanup_preserves_body_and_principal_and_reuses_setup_session(self):
        transaction = self.transaction("contacts", {"email": "probe@example.test"},
                                       {"Authorization": "Basic app-password"})
        original = copy.deepcopy(transaction)
        self.responses = [({"data": []}, 200),
                          ({"data": [{"id": 42, "email": "probe@example.test"}]}, 200),
                          (None, 204), ({}, 201), (None, 204)]
        self.module.remove_a_previous_contact_probe(transaction)
        self.module.remove_a_previous_contact_probe(transaction)
        self.assertEqual(transaction, original)
        self.assertEqual([request.get_method() for request in self.calls], ["GET", "GET", "DELETE"])
        self.assertTrue(self.calls[-1].full_url.endswith("/contacts/42"))
        # All setup hooks share the session, rather than logging in per probe.
        self.module.give_each_rename_probe_a_folder(self.transaction("folders/old", {"name": "new"}))
        self.assertEqual(len(self.logins), 1)

    def test_invalid_account_probes_do_not_attempt_setup(self):
        for account in ("0", "-1", "not-an-id", "1.5"):
            for resource, body, hook in (
                    ("folders", {"name": "new"}, self.module.remove_a_previous_folder_probe),
                    ("folders/old", {"name": "new"}, self.module.give_each_rename_probe_a_folder),
                    ("contacts", {"email": "probe@example.test"}, self.module.remove_a_previous_contact_probe)):
                with self.subTest(account=account, resource=resource):
                    transaction = self.transaction(resource, body)
                    transaction["fullPath"] = transaction["fullPath"].replace("/7/", "/" + account + "/")
                    original = copy.deepcopy(transaction)
                    hook(transaction)
                    self.assertEqual(transaction, original)
        self.assertFalse(self.logins)
        self.assertFalse(self.calls)

    def test_setup_sessions_are_not_shared_across_origins(self):
        for origin in ("http://fixture.test:8080", "http://other.test:8080", "http://fixture.test:8080"):
            self.assertEqual(self.module.admin_token(origin), "setup-session")
        self.assertEqual(self.logins, ["http://fixture.test:8080/api/v1/auth/login",
                                      "http://other.test:8080/api/v1/auth/login"])
        # Only actual sign-ins spend a token; reusing a cached session does not.
        self.assertEqual(self.wait.call_count, 2)
        self.wait.assert_called_with(1.1)

    def test_failed_login_is_not_cached(self):
        origin = "http://fixture.test:8080"
        error = HTTPError(origin + "/api/v1/auth/login", 401, "invalid credentials", {}, None)
        with patch.object(self.module.urllib.request, "urlopen", side_effect=error):
            with self.assertRaises(HTTPError):
                self.module.admin_token(origin)
        self.assertEqual(self.module.admin_token(origin), "setup-session")
        self.assertEqual(len(self.logins), 1)

    def test_missing_access_token_stops_setup_and_is_not_cached(self):
        origin = "http://fixture.test:8080"
        for token in (None, "", 123):
            with self.subTest(token=token):
                with patch.object(self.module.urllib.request, "urlopen",
                                  return_value=Response({"data": {"access_token": token}})):
                    with self.assertRaisesRegex(RuntimeError, "no access token"):
                        self.module.admin_token(origin)
        self.assertEqual(self.module.admin_token(origin), "setup-session")


if __name__ == "__main__":
    unittest.main()
