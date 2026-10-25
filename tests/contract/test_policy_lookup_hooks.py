"""Exercise fixture registration and public-API setup through the configured entrypoint."""

import copy
import json
import runpy
import sys
import threading
import types
import unittest
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
REFUSAL = {
    "error": {
        "code": "not_found",
        "message": "No MTA-STS policy found for this domain",
    }
}


class FixtureAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.calls.append((self.path, dict(self.headers), body))
        if self.path == "/api/v1/auth/login":
            status, value = 200, {"data": {"access_token": "setup-session"}}
        elif self.path == "/api/v1/admin/domains":
            status, value = (
                self.server.create_status,
                {"data": {"id": self.server.domain_id}},
            )
        else:
            status, value = 404, {}
        payload = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


class PolicyAbsentFixture(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.calls = []
        self.server.domain_id = 42
        self.server.create_status = 201
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

        module = types.ModuleType("vertrag_hooks")
        module.stash = {}
        self.before = []

        def before(name):
            def register(function):
                self.before.append((name, function))
                return function

            return register

        module.before = before
        module.after = lambda _: lambda function: function
        module.get_json = lambda transaction, which="request": json.loads(
            transaction[which]["body"]
        )
        module.set_json = lambda transaction, value: transaction["request"].update(
            body=json.dumps(value)
        )
        self.hooks = module
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        with patch.dict(sys.modules, {"vertrag_hooks": module}):
            self.loaded = runpy.run_path(str(ROOT / config["hookfiles"]))
        callbacks = [fn for name, fn in self.before if name == "getMtaStsPolicy"]
        self.assertEqual(len(callbacks), 1, "fixture hook must register exactly once")
        self.prepare = callbacks[0]
        self.assertTrue(any(name == "createAccount" for name, _ in self.before))
        # Pacing itself is covered by the shared hook's tests. Keep the real
        # setup session helper and exercise both of its HTTP requests here.
        pacing = patch.dict(
            self.loaded["shared"]["admin_token"].__globals__,
            {"pace_auth_requests": lambda _: None},
        )
        pacing.start()
        self.addCleanup(pacing.stop)

    def transaction(self):
        return {
            "operationId": "getMtaStsPolicy",
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_port),
            "fullPath": "/api/v1/admin/domains/2147483646/mta-sts",
            "request": {
                "method": "GET",
                "headers": {"Authorization": "Bearer original-read-session"},
                "body": "",
            },
            "expected": {"statusCode": "404", "body": json.dumps(REFUSAL)},
        }

    def test_setup_uses_its_session_and_preserves_the_principal_and_expectation(self):
        transaction = self.transaction()
        before = copy.deepcopy(transaction)
        self.prepare(transaction)
        before["fullPath"] = "/api/v1/admin/domains/42/mta-sts"
        self.assertEqual(transaction, before)
        self.assertEqual(
            [path for path, _, _ in self.server.calls],
            ["/api/v1/auth/login", "/api/v1/admin/domains"],
        )
        _, headers, body = self.server.calls[-1]
        self.assertEqual(headers["Authorization"], "Bearer setup-session")
        self.assertRegex(body["name"], r"^contract-policy-absent-[a-f0-9]{32}\.test$")
        self.assertEqual(set(body), {"name", "server_type"})
        self.assertEqual(body["server_type"], "restmail")

    def test_repeated_pair_reuses_only_its_private_fixture(self):
        for _ in range(2):
            transaction = self.transaction()
            self.prepare(transaction)
            self.assertEqual(
                transaction["fullPath"], "/api/v1/admin/domains/42/mta-sts"
            )
        self.assertEqual(len(self.server.calls), 2)
        self.assertNotIn("domain", self.hooks.stash)

    def test_success_missing_domain_mutations_and_other_refusals_are_untouched(self):
        mutations = [
            ("fullPath", "/api/v1/admin/domains/1/mta-sts"),
            ("fullPath", "/api/v1/admin/domains/2147483647/mta-sts"),
            ("fullPath", "/api/v1/admin/domains/0/mta-sts"),
            ("fullPath", "/api/v1/admin/domains/2147483646/mta-sts?invalid=true"),
            ("method", "PUT"),
            ("method", "DELETE"),
            ("statusCode", "200"),
            ("statusCode", "401"),
            ("statusCode", "403"),
            ("statusCode", "422"),
            (
                "body",
                json.dumps(
                    {"error": {"code": "not_found", "message": "Domain not found"}}
                ),
            ),
            ("body", "not-json"),
            ("headers", {}),
        ]
        for key, value in mutations:
            with self.subTest(key=key, value=value):
                transaction = self.transaction()
                target = (
                    transaction
                    if key == "fullPath"
                    else transaction[
                        "request" if key in ("method", "headers") else "expected"
                    ]
                )
                target[key] = value
                before = copy.deepcopy(transaction)
                self.prepare(transaction)
                self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_bad_setup_id_fails_without_rewriting_the_lookup(self):
        for domain_id in (0, -1, True, "42", None):
            with self.subTest(domain_id=domain_id):
                self.server.domain_id = domain_id
                transaction = self.transaction()
                before = copy.deepcopy(transaction)
                with self.assertRaisesRegex(RuntimeError, "positive ID"):
                    self.prepare(transaction)
                self.assertEqual(transaction, before)

    def test_setup_failure_is_not_hidden_or_retried(self):
        self.server.create_status = 500
        transaction = self.transaction()
        before = copy.deepcopy(transaction)
        with self.assertRaises(urllib.error.HTTPError) as raised:
            self.prepare(transaction)
        self.assertEqual(raised.exception.code, 500)
        raised.exception.close()
        self.assertEqual(transaction, before)
        self.assertEqual(len(self.server.calls), 2)
        self.assertEqual(self.hooks.stash["policy_absent_domains"], {})

    def test_another_origin_gets_its_own_fixture_and_setup_session(self):
        self.prepare(self.transaction())
        other = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        other.calls, other.domain_id, other.create_status = [], 84, 201
        threading.Thread(target=other.serve_forever, daemon=True).start()
        self.addCleanup(other.server_close)
        self.addCleanup(other.shutdown)
        transaction = self.transaction()
        transaction["port"] = str(other.server_port)
        self.prepare(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/84/mta-sts")
        self.assertEqual(len(other.calls), 2)
        self.assertEqual(len(self.server.calls), 2)


if __name__ == "__main__":
    unittest.main()
