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
        # The run's domain still stands unless a test says otherwise.
        self.module.fixture_call = lambda *args, **kwargs: {"id": 17}

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
        with patch.object(self.module, "probe_certificate") as certificate:
            for body in bodies:
                with self.subTest(body=body):
                    transaction = self.transaction(body)
                    before = copy.deepcopy(transaction)
                    self.module.a_self_signed_certificate(transaction)
                    self.assertEqual(transaction, before)
            certificate.assert_not_called()

    def test_upload_moves_to_its_own_domain_once_the_run_has_deleted_its_own(self):
        calls = []

        def fixture_call(transaction, method, path, *args, **kwargs):
            calls.append((method, path))
            if method == "GET":
                return None
            return {"id": 99, "name": args[0]["name"]}

        for _ in range(2):
            transaction = self.transaction(
                {"domain_id": 1, "cert_pem": "certificate", "key_pem": "key"}
            )
            with (
                patch.object(self.module, "fixture_call", side_effect=fixture_call),
                patch.object(self.module, "certificate_name_for", return_value="n.test"),
                patch.object(self.module, "probe_certificate", return_value=("fresh certificate", "fresh key")),
            ):
                self.module.a_self_signed_certificate(transaction)
            self.assertEqual(json.loads(transaction["request"]["body"])["domain_id"], 99)
        self.assertEqual(
            [call for call in calls if call[0] == "POST"],
            [("POST", "/api/v1/admin/domains")],
        )

    def test_valid_upload_gets_fresh_pem_and_the_created_domain(self):
        transaction = self.transaction(
            {"domain_id": 1, "cert_pem": "certificate", "key_pem": "key"}
        )

        with patch.object(
            self.module, "probe_certificate", return_value=("fresh certificate", "fresh key")
        ) as certificate:
            self.module.a_self_signed_certificate(transaction)
            certificate.assert_called_once_with("contract.example.org")
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
        # A domain the fixtures did not make is looked up on the server; one
        # that is not there (a 404 is None) keeps the placeholder name.
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
                with patch.object(
                    self.module, "probe_certificate", return_value=("fresh certificate", "fresh key")
                ) as certificate, patch.object(
                    self.module, "fixture_call", return_value=None
                ) as lookup:
                    self.module.a_self_signed_certificate(transaction)
                certificate.assert_called_once_with("example.org")
                lookup.assert_called_once()
                self.assertEqual(
                    lookup.call_args.args[1:],
                    ("GET", "/api/v1/admin/domains/2147483647"),
                )
                body = json.loads(transaction["request"]["body"])
                self.assertEqual(body["domain_id"], 2147483647)
                self.assertEqual(body["auto_renew"], policy)

    def test_invalid_material_is_not_repaired(self):
        with patch.object(self.module, "probe_certificate") as certificate:
            for field in ["cert_pem", "key_pem"]:
                for value in [None, "", False, 3, [], {}, "invalid material"]:
                    body = {"domain_id": 1, "cert_pem": "certificate", "key_pem": "key"}
                    body[field] = value
                    transaction = self.transaction(body)
                    before = copy.deepcopy(transaction)
                    self.module.a_self_signed_certificate(transaction)
                    self.assertEqual(transaction, before)
            certificate.assert_not_called()

    def test_success_probe_material_is_repaired(self):
        # Coverage's valid probes for the 201 carry schema-valid placeholder
        # material ("x") that no server accepts as a certificate; only real
        # material makes them the success they are named for.
        name = "/api/v1/admin/certificates > Upload a new TLS certificate > 201 > application/json"
        transaction = self.transaction(
            {"domain_id": 1, "cert_pem": "x", "key_pem": "x", "auto_renew": True}
        )
        transaction["name"] = name
        with patch.object(
            self.module, "probe_certificate", return_value=("fresh certificate", "fresh key")
        ):
            self.module.a_self_signed_certificate(transaction)
        self.assertEqual(
            json.loads(transaction["request"]["body"]),
            {
                "domain_id": 17,
                "cert_pem": "fresh certificate",
                "key_pem": "fresh key",
                "auto_renew": True,
            },
        )

    def test_refusal_probes_keep_their_material(self):
        with patch.object(self.module, "probe_certificate") as certificate:
            for name, body in [
                (
                    "/api/v1/admin/certificates > Upload a new TLS certificate > 422 > application/json",
                    {"domain_id": 1, "cert_pem": "x", "key_pem": "x"},
                ),
                (
                    "/api/v1/admin/certificates > Upload a new TLS certificate > 201 > application/json",
                    {"domain_id": 1, "cert_pem": "", "key_pem": "x"},
                ),
                (
                    "/api/v1/admin/certificates > Upload a new TLS certificate > 201 > application/json",
                    {"domain_id": 1, "cert_pem": "x", "key_pem": 3},
                ),
            ]:
                with self.subTest(name=name, body=body):
                    transaction = self.transaction(body)
                    transaction["name"] = name
                    before = copy.deepcopy(transaction)
                    self.module.a_self_signed_certificate(transaction)
                    self.assertEqual(transaction, before)
            certificate.assert_not_called()


if __name__ == "__main__":
    unittest.main()
