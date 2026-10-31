"""The internal mTLS listener's operations are sent there, with the gateway's
certificate, by a gate of their own (#1334)."""

from pathlib import Path
import tempfile
import unittest

import yaml

import internal
from test_hooks_two_factor_admin import load_hooks

ROOT = Path(__file__).resolve().parents[2]


class Internal(unittest.TestCase):
    def test_the_run_is_narrowed_and_pointed_at_the_internal_listener(self):
        config = {"exclude-operation-id": ["checkMailboxAddress", "deliverMessage", "resetTestData"],
                  "surface": {}, "auth": {"login": {}}, "endpoint": "http://10.99.0.20:8080",
                  "transport": {"timeout": "30s"}}
        out = internal.configure(config, "https://10.99.0.20:8443", "/certs")
        self.assertEqual(out["exclude-operation-id"], ["resetTestData"])
        self.assertEqual(out["operation-id"], ["checkMailboxAddress", "deliverMessage"])
        self.assertNotIn("surface", out)
        self.assertNotIn("auth", out)
        self.assertEqual(out["endpoint"], "https://10.99.0.20:8443")
        self.assertEqual(out["transport"], {"timeout": "30s", "ca-cert": "/certs/internal-ca.crt",
                                            "cert": "/certs/internal-client.crt",
                                            "cert-key": "/certs/internal-client.key"})
        self.assertEqual(out["unproven"], ".cache/contract/unproven-internal.json")

    def test_missing_certificates_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / "vertrag-internal.yml"
            config.write_text("endpoint: x\n")
            with self.assertRaises(SystemExit) as refused:
                internal.main([str(config), "https://10.99.0.20:8443", directory])
            self.assertIn("internal-ca.crt", str(refused.exception))

    def test_the_primary_run_leaves_them_for_the_internal_pass(self):
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        for operation in internal.OPERATIONS:
            with self.subTest(operation=operation):
                self.assertIn(operation, config["exclude-operation-id"])
                reasons = [item["reason"] for item in config["surface"]["exclude"]
                           if item.get("operation") == operation and "status" not in item]
                self.assertEqual(len(reasons), 1)
                self.assertIn("tests/contract/internal.py", reasons[0])

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


if __name__ == "__main__":
    unittest.main()
