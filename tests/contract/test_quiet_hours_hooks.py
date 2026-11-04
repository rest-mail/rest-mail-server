"""Quiet-hours coverage resolves only the successful fixture placeholder."""

import copy
import io
import json
import runpy
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch


class QuietHoursFixtures(unittest.TestCase):
    def setUp(self):
        module = types.ModuleType("vertrag_hooks")
        self.registered = []

        def before(operation):
            def register(function):
                self.registered.append((operation, function))
                return function

            return register

        module.before = before
        with patch.dict(sys.modules, {"vertrag_hooks": module}):
            loaded = runpy.run_path(
                str(Path(__file__).with_name("quiet_hours_hooks.py"))
            )
        self.clean_start = False
        self.prepare = loaded["register_quiet_hours_hooks"](
            lambda _: "setup-session", lambda: self.clean_start
        )
        self.transaction = {
            "protocol": "http:",
            "host": "fixture.test",
            "port": "8080",
            "fullPath": "/api/v1/accounts/1/quiet-hours",
            "request": {
                "method": "PUT",
                "body": '{"enabled":false}',
                "headers": {"Authorization": "Bearer original"},
            },
            "expected": {"statusCode": "200", "body": "unchanged"},
        }

    def test_only_replaces_the_placeholder_and_preserves_probe_bytes(self):
        original = copy.deepcopy(self.transaction)
        response = io.BytesIO(json.dumps({"data": [{"id": 42}]}).encode())
        with patch("urllib.request.urlopen", return_value=response) as send:
            self.prepare(self.transaction)
        self.assertEqual(
            self.transaction["fullPath"], "/api/v1/accounts/42/quiet-hours"
        )
        original["fullPath"] = self.transaction["fullPath"]
        self.assertEqual(self.transaction, original)
        self.assertEqual(
            send.call_args.args[0].headers["Authorization"], "Bearer setup-session"
        )
        self.assertEqual(
            [name for name, _ in self.registered], ["getQuietHours", "setQuietHours"]
        )

    def test_the_account_time_zone_route_gets_the_same_two_things(self):
        registered = []
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = lambda operation: (
            lambda function: registered.append(operation) or function
        )
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            loaded = runpy.run_path(
                str(Path(__file__).with_name("quiet_hours_hooks.py"))
            )
        prepare = loaded["register_quiet_hours_hooks"](
            lambda _: "setup-session",
            route="time-zone",
            operations=("getAccountTimeZone", "setAccountTimeZone"),
        )
        self.assertEqual(registered, ["getAccountTimeZone", "setAccountTimeZone"])
        transaction = copy.deepcopy(self.transaction)
        transaction["fullPath"] = "/api/v1/accounts/1/time-zone"
        transaction["request"]["body"] = '{"time_zone":"x"}'
        response = io.BytesIO(json.dumps({"data": [{"id": 42}]}).encode())
        with patch("urllib.request.urlopen", return_value=response):
            prepare(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/accounts/42/time-zone")
        self.assertEqual(json.loads(transaction["request"]["body"]), {"time_zone": "UTC"})

        untouched = copy.deepcopy(self.transaction)
        original = copy.deepcopy(untouched)
        with patch("urllib.request.urlopen") as send:
            prepare(untouched)
        send.assert_not_called()
        self.assertEqual(untouched, original)

    def test_does_not_rewrite_negative_or_principal_probes(self):
        for update in (
            {"fullPath": "/api/v1/accounts/0/quiet-hours"},
            {"fullPath": "/api/v1/accounts/99/quiet-hours"},
            {"expected": {"statusCode": "422"}},
            {"name": "Quiet hours as ordinary"},
            {"request": {"headers": {}}},
        ):
            transaction = copy.deepcopy(self.transaction)
            transaction.update(update)
            original = copy.deepcopy(transaction)
            with patch("urllib.request.urlopen") as send:
                self.prepare(transaction)
            send.assert_not_called()
            self.assertEqual(transaction, original)

    def test_leaves_the_generated_time_zone_to_the_pin(self):
        self.transaction["request"]["body"] = (
            '{"enabled":false,"start":"","end":"","time_zone":"x"}'
        )
        response = io.BytesIO(json.dumps({"data": [{"id": 42}]}).encode())
        with patch("urllib.request.urlopen", return_value=response):
            self.prepare(self.transaction)
        self.assertEqual(
            json.loads(self.transaction["request"]["body"]),
            {"enabled": False, "start": "", "end": "", "time_zone": "x"},
        )

    def test_a_clean_start_leaves_the_account_to_the_own_data_hook(self):
        # With a reset asked for, the first listed account is the retained
        # fixture admin, shared data: the path stays the examples' 1, which the
        # own-data hook replaces with an account made after the reset (#1767).
        self.clean_start = True
        self.transaction["request"]["body"] = (
            '{"enabled":false,"start":"","end":"","time_zone":"x"}'
        )
        for method in ("GET", "PUT"):
            transaction = copy.deepcopy(self.transaction)
            transaction["request"]["method"] = method
            original = copy.deepcopy(transaction)
            with patch("urllib.request.urlopen") as send:
                self.prepare(transaction)
            send.assert_not_called()
            self.assertEqual(transaction, original)
            self.assertEqual(
                transaction["fullPath"], "/api/v1/accounts/1/quiet-hours"
            )
        # The generated time zone stays the body's, even when invalid.
        self.assertEqual(
            json.loads(transaction["request"]["body"])["time_zone"], "x"
        )
