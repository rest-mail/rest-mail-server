"""Generated account probes never act on the account the run signs in as."""

import copy
import json
import runpy
import sys
import threading
import types
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
OWN_ID = 4
DELETE = "/api/v1/admin/mailboxes/{id} > Delete an account"


class FixtureAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, value):
        payload = json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self):
        self.rfile.read(int(self.headers["Content-Length"]))
        self.server.calls.append((self.path, dict(self.headers)))
        self.reply({"data": {"access_token": "setup-session"}})

    def do_GET(self):
        self.server.calls.append((self.path, dict(self.headers)))
        self.reply({"data": self.server.accounts})


class OwnAccountProbes(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.calls = []
        self.server.accounts = [{"id": OWN_ID, "is_primary": True}, {"id": 9}]
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
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        with patch.dict(sys.modules, {"vertrag_hooks": module}):
            self.loaded = runpy.run_path(str(ROOT / config["hookfiles"]))
        pacing = patch.dict(
            self.loaded["shared"]["admin_token"].__globals__,
            {"pace_auth_requests": lambda _: None},
        )
        pacing.start()
        self.addCleanup(pacing.stop)

    def hooks_for(self, operation):
        return [
            fn
            for name, fn in self.before
            if name == operation and fn.__name__ == "skip_the_own_account"
        ]

    def transaction(self, path, status="204", name=None):
        return {
            "name": name or DELETE + " > " + status,
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_port),
            "fullPath": path,
            "request": {"method": "DELETE", "headers": {}, "body": ""},
            "expected": {"statusCode": status},
        }

    def test_each_operation_on_the_own_account_is_registered_once(self):
        for operation in ("deleteMailbox", "endMailboxSessions", "inviteAccount"):
            with self.subTest(operation=operation):
                self.assertEqual(len(self.hooks_for(operation)), 1)

    def test_success_probes_of_the_own_account_are_skipped(self):
        cases = [
            ("deleteMailbox", f"/api/v1/admin/mailboxes/{OWN_ID}", "204"),
            ("endMailboxSessions", f"/api/v1/admin/mailboxes/{OWN_ID}/sessions", "204"),
            ("inviteAccount", f"/api/v1/admin/mailboxes/{OWN_ID}/invitation", "201"),
        ]
        for operation, path, status in cases:
            with self.subTest(operation=operation):
                transaction = self.transaction(path, status)
                self.hooks_for(operation)[0](transaction)
                self.assertIs(transaction.get("skip"), True)
        # The own account is looked up once per origin, with the setup session.
        self.assertEqual(
            [path for path, _ in self.server.calls],
            ["/api/v1/auth/login", "/api/v1/accounts"],
        )
        self.assertEqual(
            self.server.calls[-1][1]["Authorization"], "Bearer setup-session"
        )

    def test_other_ids_refusals_and_other_principals_are_sent_unchanged(self):
        hook = self.hooks_for("deleteMailbox")[0]
        cases = [
            self.transaction("/api/v1/admin/mailboxes/9"),
            self.transaction("/api/v1/admin/mailboxes/0"),
            self.transaction("/api/v1/admin/mailboxes/a"),
            self.transaction(f"/api/v1/admin/mailboxes/{OWN_ID}", "403"),
            self.transaction(f"/api/v1/admin/mailboxes/{OWN_ID}", "409"),
            self.transaction(
                f"/api/v1/admin/mailboxes/{OWN_ID}", name=DELETE + " > 204 as ordinary"
            ),
        ]
        for transaction in cases:
            with self.subTest(path=transaction["fullPath"], name=transaction["name"]):
                before = copy.deepcopy(transaction)
                hook(transaction)
                self.assertEqual(transaction, before)

    def test_no_own_account_fails_instead_of_skipping_nothing(self):
        self.server.accounts = []
        transaction = self.transaction(f"/api/v1/admin/mailboxes/{OWN_ID}")
        with self.assertRaisesRegex(RuntimeError, "positive ID"):
            self.hooks_for("deleteMailbox")[0](transaction)
        self.assertNotIn("skip", transaction)


if __name__ == "__main__":
    unittest.main()
