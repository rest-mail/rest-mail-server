import json
import unittest
from unittest.mock import Mock, patch

from test_clean_start_batch_regressions import registered_hooks


class OwnDataAcceptance(unittest.TestCase):
    def setUp(self):
        self.callbacks, self.loaded, self.stub = registered_hooks()

    def test_dmarc_setup_delivers_with_the_smtp_gateway_identity(self):
        transaction = {"fullPath": "/api/v1/admin/dmarc-reports"}
        domain = {"id": 7, "name": self.loaded["FIXTURE_PLAN"]["domain"]}
        report = {"id": 9, "report_id": self.loaded["OWN_DMARC_REPORT_ID"]}
        fixture = Mock(side_effect=[[domain], None, [report]])
        context = Mock()
        ssl = Mock()
        ssl.create_default_context.return_value = context
        response = Mock()
        response.__enter__ = Mock(return_value=Mock(status=201))
        response.__exit__ = Mock(return_value=False)
        opener = Mock(return_value=response)
        callback = self.loaded["a_dmarc_report_of_its_own"]
        with patch.dict(callback.__globals__, {
            "fixture_call": fixture,
            "origin_of": Mock(return_value="https://public.invalid"),
            "INTERNAL_ORIGIN": "https://gateway.invalid:8443",
            "CONTRACT_MTLS": "/contract-mtls",
            "ssl": ssl,
        }), patch("urllib.request.urlopen", opener):
            self.assertEqual(callback(transaction), report)
        request = opener.call_args.args[0]
        self.assertEqual(request.full_url, "https://gateway.invalid:8443/api/v1/messages/deliver")
        self.assertEqual(request.method, "POST")
        self.assertNotIn("Authorization", request.headers)
        ssl.create_default_context.assert_called_once_with(cafile="/contract-mtls/internal-ca.crt")
        context.load_cert_chain.assert_called_once_with(
            "/contract-mtls/internal-smtp-delivery.crt", "/contract-mtls/internal-smtp-delivery.key"
        )
        self.assertIs(opener.call_args.kwargs["context"], context)
        self.assertIn("raw_message", json.loads(request.data))

    def test_clean_certificate_upload_uses_a_domain_created_after_reset(self):
        transaction = {
            "name": "/api/v1/admin/certificates > Upload > 201 > application/json",
            "fullPath": "/api/v1/admin/certificates",
            "request": {"body": json.dumps({"domain_id": 1, "cert_pem": "certificate", "key_pem": "key"})},
        }
        reset = Mock()
        make_domain = Mock(return_value=73)
        callback = self.loaded["domain_administration_after_a_reset"]
        with patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"}), patch.dict(callback.__globals__, {
            "start_clean": reset, "a_domain_of_its_own": make_domain,
        }):
            callback(transaction)
        reset.assert_called_once_with(transaction)
        make_domain.assert_called_once_with(transaction)
        self.assertEqual(json.loads(transaction["request"]["body"])["domain_id"], 73)

    def test_clean_certificate_refusals_keep_the_documented_domain(self):
        for status in ("404", "422"):
            transaction = {
                "name": f"/api/v1/admin/certificates > Upload > {status} > application/json",
                "fullPath": "/api/v1/admin/certificates",
                "request": {"body": json.dumps({"domain_id": 2147483647})},
            }
            callback = self.loaded["domain_administration_after_a_reset"]
            make_domain = Mock()
            with patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"}), patch.dict(callback.__globals__, {
                "start_clean": Mock(), "a_domain_of_its_own": make_domain,
            }):
                callback(transaction)
            make_domain.assert_not_called()
            self.assertEqual(json.loads(transaction["request"]["body"])["domain_id"], 2147483647)
