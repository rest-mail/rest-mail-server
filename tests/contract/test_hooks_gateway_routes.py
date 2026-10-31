"""The gateways' machine routes are sent in the primary run: the E2E topology
serves them on the public listener (#1334)."""

from pathlib import Path
import unittest

import yaml

from test_hooks_two_factor_admin import load_hooks

ROOT = Path(__file__).resolve().parents[2]


class GatewayRoutes(unittest.TestCase):
    def test_they_are_not_excluded(self):
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        for operation in ("checkMailboxAddress", "deliverMessage"):
            self.assertNotIn(operation, config["exclude-operation-id"])
            self.assertFalse([e for e in config["surface"]["exclude"] if e.get("operation") == operation])

    def test_found_looks_up_the_fixture_admins_mailbox(self):
        module, _, registered = load_hooks()
        self.assertIn("look_up_a_hosted_mailbox", registered["checkMailboxAddress"])
        for path, expected in (
            ("/api/mailboxes?address=user%40example.com", "/api/mailboxes?address=admin%40restmail.test"),
            ("/api/mailboxes?address=user@example.com", "/api/mailboxes?address=admin%40restmail.test"),
            ("/api/mailboxes?address=", "/api/mailboxes?address="),
            ("/api/mailboxes", "/api/mailboxes"),
            ("/api/mailboxes?address=other%40example.com", "/api/mailboxes?address=other%40example.com"),
        ):
            with self.subTest(path=path):
                t = {"fullPath": path}
                module.look_up_a_hosted_mailbox(t)
                self.assertEqual(t["fullPath"], expected)

    def test_delivery_is_from_a_domain_without_a_dmarc_policy(self):
        import json
        module, hooks, registered = load_hooks()
        self.assertIn("deliver_from_a_domain_without_a_dmarc_policy", registered["deliverMessage"])
        for sender, expected in (("sender@example.net", "sender@contract-sender.test"),
                                 ("other@example.org", "other@example.org")):
            with self.subTest(sender=sender):
                t = {"request": {"body": json.dumps({"address": "admin@restmail.test", "sender": sender})}}
                module.deliver_from_a_domain_without_a_dmarc_policy(t)
                self.assertEqual(json.loads(t["request"]["body"])["sender"], expected)
        t = {"request": {"body": "[]"}}
        module.deliver_from_a_domain_without_a_dmarc_policy(t)
        self.assertEqual(t["request"]["body"], "[]")

if __name__ == "__main__":
    unittest.main()
