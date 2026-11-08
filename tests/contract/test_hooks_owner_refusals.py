"""The owner refusals that require owner_key reach the check they document: a
real fresh challenge, signed by a key listed nowhere (not_status_key,
not_owner) or by the run's owner key for an address no account has
(no_account), instead of the placeholder challenge every one of them would be
refused for first (#1162)."""

import json
import tempfile
import threading
import unittest
from http.server import ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

from test_hooks_owner_key import PLACEHOLDER, FakeAPI, transaction
from test_hooks_two_factor_admin import load_hooks

STATUS = "/api/v1/auth/owner/status > Trade a signed challenge for what the server runs > 403 > application/json"
NOT_OWNER = "/api/v1/auth/owner/token > Trade a signed challenge for a reset-admin token > 403 > application/json"
NO_ACCOUNT = "/api/v1/auth/owner/token > Trade a signed challenge for a reset-admin token > 404 > application/json"
# The examples' unlisted key (not_status_key, not_owner).
OTHER = dict(PLACEHOLDER, public_key="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOther other@example")
# A seed the unlisted answers are made from in these tests, in place of a random one.
UNLISTED = bytes(range(32))
CREDENTIALS = (None, "Bearer admin", "Bearer ordinary", "Basic app-password")


class OwnerRefusalsReachTheirChecks(unittest.TestCase):
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
        self.addCleanup(patch.stopall)
        self.owner = self.module.owner_key

    def seed(self):
        return bytes.fromhex(json.loads(self.key.read_text())["seed"])

    def send(self, hook, t):
        t["port"] = str(self.server.server_address[1])
        before = json.loads(t["request"]["body"])
        with patch.object(self.owner.os, "urandom", return_value=UNLISTED) as urandom:
            getattr(self.module, hook)(t)
        self.urandom = urandom
        return before, json.loads(t["request"]["body"])

    def signed(self, seed, purpose, address, challenge):
        return {
            "challenge": challenge,
            "public_key": self.owner.authorized_key(seed),
            "signature": self.owner.ssh_signature(
                seed, self.owner.owner_message(challenge, purpose, address)),
        }

    def test_the_unlisted_key_is_made_fresh_and_is_not_the_runs(self):
        self.assertNotEqual(self.owner.authorized_key(UNLISTED), self.owner.authorized_key(self.seed()))
        for hook in ("refuse_a_status_key_that_is_not_listed",
                     "refuse_a_token_to_a_key_that_is_not_an_owner"):
            with self.subTest(hook=hook):
                name, path = (STATUS, "/api/v1/auth/owner/status") if "status" in hook else \
                    (NOT_OWNER, "/api/v1/auth/owner/token")
                self.send(hook, transaction(name, path, 403, dict(OTHER)))
                self.urandom.assert_called_once_with(32)

    def test_not_status_key_signs_a_fresh_challenge_with_an_unlisted_key(self):
        for credential in CREDENTIALS:
            with self.subTest(credential=credential):
                self.server.calls.clear()
                t = transaction(STATUS, "/api/v1/auth/owner/status", 403, dict(OTHER), credential)
                _, body = self.send("refuse_a_status_key_that_is_not_listed", t)
                self.assertEqual(self.server.calls, [("/api/v1/auth/owner/challenge", None, {})])
                self.assertEqual(body, self.signed(UNLISTED, "status", "", "challenge-1"))
                self.assertEqual(t["request"]["headers"].get("Authorization"), credential)

    def test_not_owner_signs_the_examples_request_with_an_unlisted_key(self):
        example = {"purpose": "reset-admin", "address": "admin@restmail.test", **OTHER}
        for credential in CREDENTIALS:
            with self.subTest(credential=credential):
                self.server.calls.clear()
                t = transaction(NOT_OWNER, "/api/v1/auth/owner/token", 403, dict(example), credential)
                _, body = self.send("refuse_a_token_to_a_key_that_is_not_an_owner", t)
                # Only a challenge: no account is made and no token traded.
                self.assertEqual(self.server.calls, [("/api/v1/auth/owner/challenge", None, {})])
                self.assertEqual(body, {
                    "purpose": "reset-admin", "address": "admin@restmail.test",
                    **self.signed(UNLISTED, "reset-admin", "admin@restmail.test", "challenge-1"),
                })
                self.assertEqual(t["request"]["headers"].get("Authorization"), credential)

    def test_no_account_signs_with_the_runs_key_for_an_address_no_account_has(self):
        example = {"purpose": "reset-admin", "address": "nobody@restmail.test", **PLACEHOLDER}
        addresses = []
        for credential in CREDENTIALS:
            with self.subTest(credential=credential):
                self.server.calls.clear()
                t = transaction(NO_ACCOUNT, "/api/v1/auth/owner/token", 404, dict(example), credential)
                _, body = self.send("refuse_a_token_for_an_account_that_does_not_exist", t)
                # The address is given to no account: only a challenge is asked for.
                self.assertEqual(self.server.calls, [("/api/v1/auth/owner/challenge", None, {})])
                self.urandom.assert_not_called()
                address = body["address"]
                self.assertRegex(address, r"^no-account-\d+@restmail\.test$")
                self.assertEqual(body, {
                    "purpose": "reset-admin", "address": address,
                    **self.signed(self.seed(), "reset-admin", address, "challenge-1"),
                })
                self.assertEqual(t["request"]["headers"].get("Authorization"), credential)
                addresses.append(address)
        self.assertEqual(self.server.accounts, [])
        self.assertEqual(len(set(addresses)), len(CREDENTIALS))

    def test_other_answers_and_bodies_that_are_not_objects_are_left_alone(self):
        token = {"purpose": "reset-admin", "address": "admin@restmail.test", **PLACEHOLDER}
        cases = [
            ("refuse_a_status_key_that_is_not_listed", "/api/v1/auth/owner/status", status, body)
            for status, body in ((200, dict(PLACEHOLDER)), (401, dict(PLACEHOLDER)),
                                 (409, dict(PLACEHOLDER)), (422, dict(PLACEHOLDER)), (403, []))
        ] + [
            (hook, "/api/v1/auth/owner/token", status, body)
            for hook, refused in (("refuse_a_token_to_a_key_that_is_not_an_owner", 403),
                                  ("refuse_a_token_for_an_account_that_does_not_exist", 404))
            for status, body in ((200, dict(token)), (401, dict(token)), (409, dict(token)),
                                 (422, dict(token)), (403 if refused == 404 else 404, dict(token)),
                                 (refused, []))
        ]
        for hook, path, status, body in cases:
            with self.subTest(hook=hook, status=status, body=body):
                before, after = self.send(hook, transaction("name", path, status, body))
                self.assertEqual(after, before)
        self.assertEqual(self.server.calls, [])

    def test_a_missing_owner_key_fails_before_a_challenge_is_spent(self):
        self.key.unlink()
        example = {"purpose": "reset-admin", "address": "nobody@restmail.test", **PLACEHOLDER}
        t = transaction(NO_ACCOUNT, "/api/v1/auth/owner/token", 404, example)
        with self.assertRaisesRegex(RuntimeError, "not registered"):
            self.send("refuse_a_token_for_an_account_that_does_not_exist", t)
        self.assertEqual(self.server.calls, [])


if __name__ == "__main__":
    unittest.main()
