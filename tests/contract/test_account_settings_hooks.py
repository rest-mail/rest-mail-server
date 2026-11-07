"""Absent-setting fixtures through the configured contract entrypoint."""

import copy
import json
import runpy
import sys
import types
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


class AbsentSettingsHooks(unittest.TestCase):
    def setUp(self):
        module = types.ModuleType("vertrag_hooks")
        module.stash = {}
        self.registered = []

        def before(name):
            def register(function):
                self.registered.append((name, function))
                return function
            return register

        module.before = before
        module.after = lambda _: lambda function: function
        with patch.dict(sys.modules, {"vertrag_hooks": module}):
            config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
            self.loaded = runpy.run_path(str(ROOT / config["hookfiles"]))
        self.shared = self.loaded["shared"]
        self.shared["with_the_fixture_admins_session"] = lambda transaction: transaction["request"]["headers"].get("Authorization") == "Bearer original-admin"
        self.calls = []

        def create_account(*args):
            self.calls.append(copy.deepcopy(args))
            return 42

        self.shared["an_account_without_credentials"] = create_account
        self.prepare = next(function for name, function in self.registered if name == "getSieveScript" and function.__name__ == "prepare_absent_account_setting")

    def transaction(self, setting="sieve", method="GET"):
        return {
            "protocol": "http:", "host": "localhost", "port": "8080",
            "fullPath": f"/api/v1/accounts/2/{setting}?keep=1",
            "request": {"method": method, "headers": {"Authorization": "Bearer original-admin"}},
            "expected": {"statusCode": "404", "body": json.dumps({"error": {"code": "not_found", "message": "Sieve script not found" if setting == "sieve" else "Vacation reply not found"}})},
        }

    def test_registers_all_three_and_preserves_credential_and_expectation(self):
        for operation in ("getSieveScript", "deleteSieveScript", "disableVacation"):
            self.assertEqual(sum(name == operation and function.__name__ == "prepare_absent_account_setting" for name, function in self.registered), 1)
        for setting, method in (("sieve", "GET"), ("sieve", "DELETE"), ("vacation", "DELETE")):
            transaction = self.transaction(setting, method)
            before = copy.deepcopy(transaction)
            self.prepare(transaction)
            before["fullPath"] = f"/api/v1/accounts/42/{setting}?keep=1"
            self.assertEqual(transaction, before)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.calls[0], ("http://localhost:8080",))

    def test_other_principals_successes_and_refusals_are_untouched(self):
        for change in ("principal", "success", "account_absent", "invalid", "method"):
            transaction = self.transaction()
            if change == "principal":
                transaction["request"]["headers"] = {}
            elif change == "success":
                transaction["expected"]["statusCode"] = "200"
            elif change == "account_absent":
                transaction["expected"]["body"] = '{"error":{"code":"not_found","message":"Account not found"}}'
            elif change == "invalid":
                transaction["fullPath"] = "/api/v1/accounts/0/sieve"
            else:
                transaction["request"]["method"] = "PUT"
            before = copy.deepcopy(transaction)
            self.prepare(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.calls, [])

    def test_setup_failure_is_not_hidden(self):
        transaction = self.transaction()
        before = copy.deepcopy(transaction)
        self.shared["an_account_without_credentials"] = lambda *args: False
        with self.assertRaisesRegex(RuntimeError, "positive account ID"):
            self.prepare(transaction)
        self.assertEqual(transaction, before)

    def stored_setting_hook(self):
        stored = []

        def fixture_call(*args, expect):
            self.assertEqual(expect, (200, 201, 404))
            stored.append(args[1:])

        self.shared["fixture_call"] = fixture_call
        return next(function for name, function in self.registered if name == "getSieveScript" and function.__name__ == "a_stored_setting_to_act_on"), stored

    def test_all_three_successes_register_the_stored_setting(self):
        for operation in ("getSieveScript", "deleteSieveScript", "disableVacation"):
            self.assertEqual(sum(name == operation and function.__name__ == "a_stored_setting_to_act_on" for name, function in self.registered), 1)

    def test_a_success_has_its_setting_stored_first(self):
        hook, stored = self.stored_setting_hook()
        for setting, method, status in (("sieve", "GET", "200"), ("sieve", "DELETE", "200"), ("vacation", "DELETE", "204")):
            transaction = self.transaction(setting, method)
            transaction["fullPath"] = f"/api/v1/accounts/1/{setting}?keep=1"
            transaction["expected"]["statusCode"] = status
            before = copy.deepcopy(transaction)
            hook(transaction)
            self.assertEqual(transaction, before)
        script = ("PUT", "/api/v1/accounts/1/sieve", {"script": "keep;", "active": False})
        reply = ("PUT", "/api/v1/accounts/1/vacation", {"enabled": True, "subject": "Away", "body": "Back soon."})
        self.assertEqual(stored, [script, script, reply])

    def test_missing_fuzz_account_does_not_abort_or_rewrite_the_request(self):
        hook = next(function for name, function in self.registered if name == "getSieveScript" and function.__name__ == "a_stored_setting_to_act_on")
        fixture_call = self.shared["fixture_call"]
        for setting, method, status in (("sieve", "GET", "200"), ("sieve", "DELETE", "200"), ("vacation", "DELETE", "204")):
            with self.subTest(setting=setting, method=method):
                transaction = self.transaction(setting, method)
                transaction["fullPath"] = f"/api/v1/accounts/1000/{setting}?keep=1"
                transaction["expected"]["statusCode"] = status
                before = copy.deepcopy(transaction)
                error = urllib.error.HTTPError("http://localhost", 404, "Not Found", {}, None)
                with patch.dict(fixture_call.__globals__, {"admin_token": lambda _: "fixture-admin"}), patch("urllib.request.urlopen", side_effect=error) as request:
                    hook(transaction)
                self.assertEqual(transaction, before)
                self.assertEqual(request.call_args.args[0].full_url, f"http://localhost:8080/api/v1/accounts/1000/{setting}")

    def test_stored_setting_other_setup_failures_are_not_hidden(self):
        hook = next(function for name, function in self.registered if name == "getSieveScript" and function.__name__ == "a_stored_setting_to_act_on")
        fixture_call = self.shared["fixture_call"]
        for status in (403, 422, 500):
            with self.subTest(status=status):
                transaction = self.transaction()
                transaction["expected"]["statusCode"] = "200"
                error = urllib.error.HTTPError("http://localhost", status, "Setup failed", {}, None)
                with patch.dict(fixture_call.__globals__, {"admin_token": lambda _: "fixture-admin"}), patch("urllib.request.urlopen", side_effect=error):
                    with self.assertRaises(urllib.error.HTTPError):
                        hook(transaction)

    def test_only_the_fixture_admins_success_stores_a_setting(self):
        hook, stored = self.stored_setting_hook()
        absent = self.transaction()
        other_principal = self.transaction()
        other_principal["expected"]["statusCode"] = "200"
        other_principal["request"]["headers"] = {}
        invalid = self.transaction()
        invalid["expected"]["statusCode"] = "200"
        invalid["fullPath"] = "/api/v1/accounts/0/sieve"
        for transaction in (absent, other_principal, invalid):
            hook(transaction)
        self.assertEqual(stored, [])


if __name__ == "__main__":
    unittest.main()
