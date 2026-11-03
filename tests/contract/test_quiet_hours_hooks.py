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
        self.prepare = loaded["register_quiet_hours_hooks"](lambda _: "setup-session")
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

    def test_supplies_only_the_compilers_unknown_time_zone_format(self):
        self.transaction["request"]["body"] = (
            '{"enabled":false,"start":"","end":"","time_zone":"x"}'
        )
        response = io.BytesIO(json.dumps({"data": [{"id": 42}]}).encode())
        with patch("urllib.request.urlopen", return_value=response):
            self.prepare(self.transaction)
        self.assertEqual(
            json.loads(self.transaction["request"]["body"]),
            {"enabled": False, "start": "", "end": "", "time_zone": "UTC"},
        )
