"""The owner endpoints' successes are signed by the run's throwaway owner key,
and a token is minted and redeemed only for an account made for that request
(#1162)."""

import json
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

from test_hooks_two_factor_admin import load_hooks

STATUS = "/api/v1/auth/owner/status > Trade a signed challenge for what the server runs > 200 > application/json"
TOKEN = "/api/v1/auth/owner/token > Trade a signed challenge for a reset-admin token > 200 > application/json"
RESET = "/api/v1/auth/reset > Let an account back in with a reset token; also hands a server over > 200 > application/json"
PLACEHOLDER = {"challenge": "c2lnbiB0aGlzIGNoYWxsZW5nZQ",
               "public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample owner@example",
               "signature": "c2lnbmF0dXJl"}


class FakeAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])) or b"{}")
        self.server.calls.append((self.path, self.headers.get("Authorization"), body))
        if self.path == "/api/v1/accounts":
            self.server.accounts.append(body)
            data = {"id": len(self.server.accounts), "address": body["address"]}
        elif self.path == "/api/v1/auth/owner/challenge":
            data = {"challenge": "challenge-%d" % len(self.server.calls)}
        elif self.path == "/api/v1/auth/owner/token":
            data = {"token": "token-for-" + body["address"], "purpose": body["purpose"]}
        else:
            self.send_response(404)
            self.end_headers()
            return
        payload = json.dumps({"data": data}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def transaction(name, path, status, body, authorization=None):
    headers = {} if authorization is None else {"Authorization": authorization}
    return {"name": name, "protocol": "http:", "host": "127.0.0.1", "port": "0",
            "fullPath": path, "expected": {"statusCode": status},
            "request": {"method": "POST", "headers": headers, "body": json.dumps(body)}}


class SignedWithTheRunsOwnerKey(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeAPI)
        self.server.calls, self.server.accounts = [], []
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.module, self.hooks, self.registered = load_hooks()
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.key = Path(directory.name) / "owner-key.json"
        self.module.owner_key.new(self.key)
        patch.object(self.module.owner_key, "KEY", self.key).start()
        patch.object(self.module, "mfa_admin_token", return_value="two-factor-token").start()
        patch.object(self.module.time, "sleep").start()
        patch.object(self.module.owner_key.rate_limits.time, "sleep").start()
        self.addCleanup(patch.stopall)

    def seed(self):
        return bytes.fromhex(json.loads(self.key.read_text())["seed"])

    def send(self, hook, t):
        t["port"] = str(self.server.server_address[1])
        before = json.loads(t["request"]["body"])
        getattr(self.module, hook)(t)
        return before, json.loads(t["request"]["body"])

    def signature(self, message):
        return self.module.owner_key.ssh_signature(self.seed(), message)

    def test_each_runs_after_its_pacing(self):
        self.assertEqual(self.registered["ownerStatus"],
                         ["pace_owner_requests", "sign_the_status_with_the_runs_owner_key",
                          "refuse_a_status_key_that_is_not_listed"])
        self.assertEqual(self.registered["ownerToken"],
                         ["pace_owner_requests", "mint_for_an_account_of_its_own",
                          "refuse_a_token_to_a_key_that_is_not_an_owner",
                          "refuse_a_token_for_an_account_that_does_not_exist"])
        self.assertEqual(self.registered["resetAdmin"],
                         ["pace_auth_requests", "reset_an_account_of_its_own"])
        # A challenge needs nothing but the registered key: it is only paced.
        self.assertEqual(self.registered["ownerChallenge"], ["pace_owner_requests"])

    def test_every_principals_status_is_signed_with_a_fresh_challenge(self):
        for credential in (None, "Bearer ordinary", "Basic app-password"):
            with self.subTest(credential=credential):
                self.server.calls.clear()
                t = transaction(STATUS, "/api/v1/auth/owner/status", 200, dict(PLACEHOLDER), credential)
                _, body = self.send("sign_the_status_with_the_runs_owner_key", t)
                self.assertEqual(self.server.calls, [("/api/v1/auth/owner/challenge", None, {})])
                self.assertEqual(body, {
                    "challenge": "challenge-1",
                    "public_key": self.module.owner_key.authorized_key(self.seed()),
                    "signature": self.signature(b"restmail-owner-v1\nstatus\n\nchallenge-1"),
                })
                self.assertEqual(t["request"]["headers"].get("Authorization"), credential)

    def test_a_token_is_minted_for_an_account_made_for_the_request(self):
        for credential in (None, "Bearer admin"):
            with self.subTest(credential=credential):
                self.server.calls.clear()
                example = {"purpose": "reset-admin", "address": "admin@restmail.test", **PLACEHOLDER}
                t = transaction(TOKEN, "/api/v1/auth/owner/token", 200, example, credential)
                _, body = self.send("mint_for_an_account_of_its_own", t)
                (made, by, account), (challenge, _, _) = self.server.calls
                self.assertEqual((made, by, challenge),
                                 ("/api/v1/accounts", "Bearer two-factor-token", "/api/v1/auth/owner/challenge"))
                self.assertRegex(account["address"], r"^owner-reset-\d+@restmail\.test$")
                self.assertNotIn("password", account)
                address = account["address"]
                self.assertEqual(body, {
                    "purpose": "reset-admin", "address": address, "challenge": "challenge-2",
                    "public_key": self.module.owner_key.authorized_key(self.seed()),
                    "signature": self.signature(
                        ("restmail-owner-v1\nreset-admin\n%s\nchallenge-2" % address).encode()),
                })
                self.assertEqual(t["request"]["headers"].get("Authorization"), credential)
        self.assertEqual(len({a["address"] for a in self.server.accounts}), 2)

    def test_a_reset_redeems_a_token_for_an_account_made_for_the_request(self):
        example = {"token": "example-reset-token", "password": "a new password of my own", "client": "native"}
        for credential in (None, "Bearer ordinary"):
            with self.subTest(credential=credential):
                self.server.calls.clear()
                t = transaction(RESET, "/api/v1/auth/reset", 200, dict(example), credential)
                _, body = self.send("reset_an_account_of_its_own", t)
                paths = [call[0] for call in self.server.calls]
                self.assertEqual(paths, ["/api/v1/accounts", "/api/v1/auth/owner/challenge",
                                         "/api/v1/auth/owner/token"])
                address = self.server.calls[0][2]["address"]
                self.assertNotEqual(address, "admin@restmail.test")
                self.assertEqual(self.server.calls[2][2]["address"], address)
                self.assertEqual(body, {"token": "token-for-" + address,
                                        "password": self.module.OWNER_RESET_PASSWORD, "client": "native"})
                self.assertGreaterEqual(len(body["password"]), 12)
                self.assertEqual(t["request"]["headers"].get("Authorization"), credential)

    def test_refusals_and_bodies_that_are_not_objects_are_left_alone(self):
        cases = [
            ("sign_the_status_with_the_runs_owner_key", STATUS, "/api/v1/auth/owner/status", 401, dict(PLACEHOLDER)),
            ("sign_the_status_with_the_runs_owner_key", STATUS, "/api/v1/auth/owner/status", 200, []),
            ("mint_for_an_account_of_its_own", TOKEN, "/api/v1/auth/owner/token", 403, dict(PLACEHOLDER)),
            ("mint_for_an_account_of_its_own", TOKEN, "/api/v1/auth/owner/token", 200, []),
            ("reset_an_account_of_its_own", RESET, "/api/v1/auth/reset", 401, {"token": "t"}),
            ("reset_an_account_of_its_own", RESET, "/api/v1/auth/reset", 200, []),
        ]
        for hook, name, path, status, body in cases:
            with self.subTest(hook=hook, status=status):
                before, after = self.send(hook, transaction(name, path, status, body))
                self.assertEqual(after, before)
        self.assertEqual(self.server.calls, [])


if __name__ == "__main__":
    unittest.main()
