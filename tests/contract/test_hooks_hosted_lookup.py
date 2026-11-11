"""A mailbox check's found example finds a hosted mailbox in the primary run (#1801).

The found examples name the in-process fixtures' mailboxes, which the topology
does not have, and a check's 200 has the same shape for a miss (exists: false).
So found asks about the fixture admin, and the after hook fails the 200 unless
it says that mailbox exists, with its address.
"""

import json
import unittest

from test_hooks_two_factor_admin import load_hooks

CHECK = "/api/mailboxes > Check if a mailbox address exists (used by SMTP gateway) > 200 > application/json"
RESTMAIL_CHECK = "/restmail/mailboxes > Check if a recipient mailbox exists > 200 > application/json"
ADMIN = "admin@restmail.test"

FOUND = (
    ("checkMailboxAddress", "/api/mailboxes", "user%40example.com", CHECK),
    ("restmailCheckMailbox", "/restmail/mailboxes", "user%40example.test", RESTMAIL_CHECK),
)


class HostedLookup(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, self.registered = load_hooks()

    def sent(self, path, name):
        transaction = {"name": name, "fullPath": path}
        self.module.look_up_a_hosted_mailbox(transaction)
        return transaction

    def answered(self, transaction, status, data):
        transaction["real"] = {"statusCode": status, "body": json.dumps({"data": data})}
        self.module.the_lookup_finds_the_hosted_mailbox(transaction)
        return transaction

    def test_registered_for_both_checks(self):
        for operation, *_ in FOUND:
            with self.subTest(operation=operation):
                self.assertIn("look_up_a_hosted_mailbox", self.registered[operation])
                # Behind the clean start's hook, which sends found to a mailbox of its own first.
                self.assertLess(
                    self.registered[operation].index("delivery_after_a_reset"),
                    self.registered[operation].index("look_up_a_hosted_mailbox"),
                )

    def test_found_asks_about_the_fixture_admin(self):
        for operation, path, address, name in FOUND:
            with self.subTest(operation=operation):
                sent = self.sent(f"{path}?address={address}", name)
                self.assertEqual(sent["fullPath"], f"{path}?address=admin%40restmail.test")

    def test_other_addresses_are_sent_as_they_are(self):
        for operation, path, address, name in FOUND:
            for query in ("?address=", "", "?address=nobody%40restmail.test", "?address=a%40b.test&x=1"):
                with self.subTest(operation=operation, query=query):
                    sent = self.sent(path + query, name)
                    self.assertEqual(sent["fullPath"], path + query)
                    self.assertNotIn("fail", self.answered(sent, 200, {"exists": False}))
        # Each check's found address is its own: the other's is sent as it is.
        sent = self.sent("/restmail/mailboxes?address=user%40example.com", RESTMAIL_CHECK)
        self.assertEqual(sent["fullPath"], "/restmail/mailboxes?address=user%40example.com")

    def test_the_hosted_mailbox_found_passes(self):
        for operation, path, address, name in FOUND:
            with self.subTest(operation=operation):
                sent = self.sent(f"{path}?address={address}", name)
                answer = {"exists": True, "address": "Admin@restmail.test"}
                if operation == "checkMailboxAddress":
                    answer["mailbox_id"] = 1
                self.assertNotIn("fail", self.answered(sent, 200, answer))

    def test_a_miss_or_another_mailbox_fails(self):
        for operation, path, address, name in FOUND:
            for data, problem in (
                ({"exists": False}, "exists is False"),
                ({"exists": False, "address": ADMIN}, "exists is False"),
                ({"exists": True, "address": "eve@restmail.test"}, "the address is 'eve@restmail.test'"),
                ({}, "exists is None"),
            ):
                with self.subTest(operation=operation, data=data):
                    sent = self.answered(self.sent(f"{path}?address={address}", name), 200, data)
                    self.assertIn(f"did not find the hosted mailbox {ADMIN}", sent["fail"])
                    self.assertIn(problem, sent["fail"])

    def test_another_status_or_exchange_is_left_alone(self):
        path, address, name = "/restmail/mailboxes", "user%40example.test", RESTMAIL_CHECK
        self.assertNotIn("fail", self.answered(self.sent(f"{path}?address={address}", name), 500, {"exists": False}))
        # A lookup the before hook did not send to the admin is not held to it,
        # even when an earlier one was.
        self.sent(f"{path}?address={address}", name)
        other = self.sent(f"{path}?address=nobody%40restmail.test", name)
        self.assertNotIn("fail", self.answered(other, 200, {"exists": False}))
        # Nor is another transaction's answer.
        self.sent(f"{path}?address={address}", name)
        stray = {"name": "/restmail/mailboxes > other > 200 > application/json", "fullPath": path}
        self.assertNotIn("fail", self.answered(stray, 200, {"exists": False}))


if __name__ == "__main__":
    unittest.main()
