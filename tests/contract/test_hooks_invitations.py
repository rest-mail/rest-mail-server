"""Invitation exchanges start clean in the own-data phase, each with an account
of its own, and nothing names those accounts by a count carried between
exchanges (#1385)."""

import io
import json
import re
import unittest
from unittest.mock import patch

from test_hooks_delete_domain import load_hooks

INVITE = "/api/v1/admin/mailboxes/{}/invitation"
INVITE_NAME = (
    "/api/v1/admin/mailboxes/{id}/invitation > Invite an account to choose its first password"
    " > %s > application/json"
)
ACCEPT = "/api/v1/auth/invitation"
ACCEPT_NAME = (
    "/api/v1/auth/invitation > Choose your first password with an invitation > %s > application/json"
)


class Invitations(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, registered = load_hooks()
        for operation in ("inviteAccount", "acceptInvitation"):
            self.assertIn(operation, registered)
        self.hooks.get_json = lambda transaction, which="request": json.loads(
            transaction[which]["body"]
        )
        self.hooks.set_json = lambda transaction, body: transaction["request"].update(
            body=json.dumps(body)
        )
        self.start_clean = patch.object(self.module, "start_clean").start()
        patch.object(self.module, "mfa_admin_token", return_value="two-factor").start()
        self.sent = []
        self.serial = 40

        def urlopen(request, timeout=10):
            body = json.loads(request.data) if request.data else None
            self.sent.append((request.get_method(), request.full_url, body))
            if request.full_url.endswith("/api/v1/accounts"):
                self.serial += 1
                return io.BytesIO(json.dumps({"data": {"id": self.serial}}).encode())
            return io.BytesIO(json.dumps({"data": {"token": "fresh"}}).encode())

        patch.object(self.module.urllib.request, "urlopen", urlopen).start()
        self.addCleanup(patch.stopall)

    def transaction(self, name, path, body=None):
        return {
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": "8080",
            "fullPath": path,
            "name": name,
            "request": {"body": json.dumps(body), "headers": {}},
        }

    def clean_starts_on(self):
        patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"}).start()

    def invite(self, status, placeholder="12"):
        transaction = self.transaction(INVITE_NAME % status, INVITE.format(placeholder))
        self.module.invite_an_account_without_credentials(transaction)
        return transaction

    def created(self):
        return [body for method, url, body in self.sent if url.endswith("/api/v1/accounts")]

    def test_without_a_reset_nothing_resets_and_the_admin_holds_the_credentials(self):
        paths = {status: self.invite(status)["fullPath"] for status in ("201", "409", "404")}
        self.assertEqual(
            paths,
            {
                "201": INVITE.format(41),
                "409": INVITE.format(1),
                "404": INVITE.format(2147483647),
            },
        )
        self.start_clean.assert_not_called()
        self.assertEqual(len(self.created()), 1)
        self.assertNotIn("password", self.created()[0])

    def test_each_exchange_starts_clean_with_an_account_of_its_own(self):
        self.clean_starts_on()
        created = self.invite("201")
        refused = self.invite("409")
        missing = self.invite("404")
        self.assertEqual(self.start_clean.call_count, 3)
        self.assertEqual(created["fullPath"], INVITE.format(41))
        self.assertEqual(refused["fullPath"], INVITE.format(42))
        self.assertEqual(missing["fullPath"], INVITE.format(2147483647))
        invitable, credentialed = self.created()
        self.assertNotIn("password", invitable)
        self.assertEqual(credentialed["password"], self.module.CREDENTIALED_PASSWORD)
        for method, url, _ in self.sent:
            self.assertEqual((method, url), ("POST", "http://127.0.0.1:8080/api/v1/accounts"))

    def test_other_ids_keep_theirs_after_the_reset(self):
        self.clean_starts_on()
        for placeholder in ("0", "9", "abc"):
            self.assertEqual(self.invite("201", placeholder)["fullPath"], INVITE.format(placeholder))
        self.assertEqual(self.created(), [])

    def test_accounts_are_named_by_time_and_the_stash_holds_no_count(self):
        self.invite("201")
        self.invite("201")
        first, second = (body["address"] for body in self.created())
        self.assertNotEqual(first, second)
        for address in (first, second):
            self.assertRegex(address, re.compile(r"^invited-\d+@restmail\.test$"))
        self.assertNotIn("invitable_accounts", self.hooks.stash)

    def test_the_accepted_example_starts_clean_with_an_invitation_of_its_own(self):
        self.clean_starts_on()
        transaction = self.transaction(
            ACCEPT_NAME % "200",
            ACCEPT,
            {"token": "documented", "password": "the recipient chooses this password", "client": "native"},
        )
        self.module.redeem_a_fresh_invitation(transaction)
        self.start_clean.assert_called_once_with(transaction)
        self.assertEqual(json.loads(transaction["request"]["body"])["token"], "fresh")
        self.assertEqual(
            [(method, url) for method, url, _ in self.sent],
            [
                ("POST", "http://127.0.0.1:8080/api/v1/accounts"),
                ("POST", "http://127.0.0.1:8080" + INVITE.format(41)),
            ],
        )
        self.assertNotIn("invitable_accounts", self.hooks.stash)

    def test_refusals_start_clean_and_reach_the_handler_unchanged(self):
        self.clean_starts_on()
        cases = (
            ("401", {"token": "AAAA", "password": "the recipient chooses this password"}),
            ("422", {"token": "AAAA", "password": "short"}),
            ("422", []),
        )
        for status, body in cases:
            transaction = self.transaction(ACCEPT_NAME % status, ACCEPT, body)
            self.module.redeem_a_fresh_invitation(transaction)
            self.assertEqual(json.loads(transaction["request"]["body"]), body)
        self.assertEqual(self.start_clean.call_count, 3)
        self.assertEqual(self.sent, [])

    def test_without_a_reset_the_accepted_example_does_not_reset(self):
        transaction = self.transaction(ACCEPT_NAME % "200", ACCEPT, {"token": "documented"})
        self.module.redeem_a_fresh_invitation(transaction)
        self.start_clean.assert_not_called()
        self.assertEqual(json.loads(transaction["request"]["body"])["token"], "fresh")


if __name__ == "__main__":
    unittest.main()
