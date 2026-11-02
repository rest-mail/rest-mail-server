"""The due fixture never rewrites a principal, body, path or expected answer."""

import base64
import copy
import io
import json
from pathlib import Path
import runpy
import sys
import types
import unittest
import urllib.error
from unittest.mock import patch


class ThreadDueFixtureTests(unittest.TestCase):
    def setUp(self):
        module = types.ModuleType("vertrag_hooks")
        self.registered = []

        def before(name):
            def register(function):
                self.registered.append((name, function))
                return function

            return register

        module.before = before
        with patch.dict(sys.modules, {"vertrag_hooks": module}):
            loaded = runpy.run_path(
                str(Path(__file__).with_name("thread_due_hooks.py"))
            )
        self.prepare = loaded["register_thread_due_hook"](
            lambda _: "private-setup-session"
        )
        self.transaction = {
            "protocol": "http:",
            "host": "fixture.test",
            "port": "8080",
            "fullPath": "/api/v1/accounts/7/threads/%3Cdue%2F2026%40example.net%3E/due",
            "request": {
                "method": "POST",
                "headers": {"Authorization": "Bearer original-session"},
                "body": '{"due":null,"due_source":"user"}',
            },
            "expected": {"statusCode": "200", "body": "unchanged"},
        }

    def response(self):
        response = io.BytesIO(b'{"data":{"id":5}}')
        response.status = 201
        return response

    def test_prepares_only_the_thread_and_preserves_the_transaction(self):
        original = copy.deepcopy(self.transaction)
        with patch("urllib.request.urlopen", return_value=self.response()) as send:
            self.prepare(self.transaction)
        self.assertEqual(self.transaction, original)
        self.assertEqual([name for name, _ in self.registered], ["setThreadDue"])
        request = send.call_args.args[0]
        self.assertEqual(
            request.full_url,
            "http://fixture.test:8080/api/v1/accounts/7/messages/import",
        )
        self.assertEqual(
            request.get_header("Authorization"), "Bearer private-setup-session"
        )
        body = json.loads(request.data)
        self.assertEqual(body["folder"], "Contract due")
        self.assertIn(
            b"Message-ID: <due/2026@example.net>\r\n",
            base64.b64decode(body["raw_message"]),
        )

    def test_refusals_mutated_identities_and_anonymous_requests_stay_untouched(self):
        for field, value in (
            ("status", "404"),
            ("status", "422"),
            ("path", "/api/v1/accounts/0/threads/%3Cdue%2F2026%40example.net%3E/due"),
            ("path", "/api/v1/accounts/7/threads/missing/due"),
            ("headers", {}),
            ("method", "GET"),
        ):
            transaction = copy.deepcopy(self.transaction)
            if field == "status":
                transaction["expected"]["statusCode"] = value
            elif field == "path":
                transaction["fullPath"] = value
            else:
                transaction["request"][field] = value
            original = copy.deepcopy(transaction)
            with patch("urllib.request.urlopen") as send:
                self.prepare(transaction)
            send.assert_not_called()
            self.assertEqual(transaction, original)

    def test_an_unknown_account_is_left_to_the_real_probe(self):
        error = urllib.error.HTTPError("http://fixture.test", 404, "missing", {}, None)
        with patch("urllib.request.urlopen", side_effect=error):
            self.prepare(self.transaction)

    def test_setup_failure_is_not_hidden(self):
        error = urllib.error.HTTPError("http://fixture.test", 500, "failed", {}, None)
        with patch("urllib.request.urlopen", side_effect=error):
            with self.assertRaises(urllib.error.HTTPError):
                self.prepare(self.transaction)
