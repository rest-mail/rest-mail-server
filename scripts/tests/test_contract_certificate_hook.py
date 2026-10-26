"""Certificate setup keeps the refused examples and coverage boundaries intact."""

import copy
import importlib.util
import json
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


class ContractCertificateHookTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = hooks.after = lambda *args: lambda function: function
        hooks.stash = {"domain_id": 17, "domain": "contract.example.org"}
        hooks.get_json = lambda transaction: json.loads(transaction["request"]["body"])
        hooks.set_json = lambda transaction, body: transaction["request"].update(
            body=json.dumps(body)
        )
        spec = importlib.util.spec_from_file_location(
            "contract_certificate_hooks", ROOT / "tests/contract/hooks.py"
        )
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)

    def transaction(self, body):
        return {
            "request": {
                "body": json.dumps(body),
                "headers": {"Authorization": "Bearer caller"},
            }
        }

    def test_refused_requests_are_sent_unchanged_without_generating_keys(self):
        complete = {"domain_id": 1, "cert_pem": "certificate", "key_pem": "key"}
        bodies = [[], {}, None, "certificate", 42]
        for field in complete:
            missing = dict(complete)
            del missing[field]
            bodies.append(missing)
            empty = dict(complete)
            empty[field] = 0 if field == "domain_id" else ""
            bodies.append(empty)
        with patch.object(self.module, "openssl") as openssl:
            for body in bodies:
                with self.subTest(body=body):
                    transaction = self.transaction(body)
                    before = copy.deepcopy(transaction)
                    self.module.a_self_signed_certificate(transaction)
                    self.assertEqual(transaction, before)
            openssl.assert_not_called()

    def test_valid_upload_gets_fresh_pem_and_the_created_domain(self):
        transaction = self.transaction(
            {"domain_id": 1, "cert_pem": "certificate", "key_pem": "key"}
        )

        def generate(*args):
            self.assertIn("/CN=contract.example.org", args)
            self.assertIn("subjectAltName=DNS:contract.example.org", args)
            Path(args[args.index("-keyout") + 1]).write_text("fresh key")
            Path(args[args.index("-out") + 1]).write_text("fresh certificate")

        with patch.object(self.module, "openssl", side_effect=generate) as openssl:
            self.module.a_self_signed_certificate(transaction)
            openssl.assert_called_once()
        self.assertEqual(
            json.loads(transaction["request"]["body"]),
            {
                "domain_id": 17,
                "cert_pem": "fresh certificate",
                "key_pem": "fresh key",
            },
        )
        self.assertEqual(
            transaction["request"]["headers"], {"Authorization": "Bearer caller"}
        )

    def test_renewal_policy_and_nonfixture_domain_are_preserved(self):
        def generate(*args):
            Path(args[args.index("-keyout") + 1]).write_text("fresh key")
            Path(args[args.index("-out") + 1]).write_text("fresh certificate")

        for policy in [False, True, None, "invalid"]:
            with self.subTest(policy=policy):
                transaction = self.transaction(
                    {
                        "domain_id": 2147483647,
                        "cert_pem": "certificate",
                        "key_pem": "key",
                        "auto_renew": policy,
                    }
                )
                with patch.object(self.module, "openssl", side_effect=generate):
                    self.module.a_self_signed_certificate(transaction)
                body = json.loads(transaction["request"]["body"])
                self.assertEqual(body["domain_id"], 2147483647)
                self.assertEqual(body["auto_renew"], policy)

    def test_invalid_material_is_not_repaired(self):
        with patch.object(self.module, "openssl") as openssl:
            for field in ["cert_pem", "key_pem"]:
                for value in [None, "", False, 3, [], {}, "invalid material"]:
                    body = {"domain_id": 1, "cert_pem": "certificate", "key_pem": "key"}
                    body[field] = value
                    transaction = self.transaction(body)
                    before = copy.deepcopy(transaction)
                    self.module.a_self_signed_certificate(transaction)
                    self.assertEqual(transaction, before)
            openssl.assert_not_called()


if __name__ == "__main__":
    unittest.main()
