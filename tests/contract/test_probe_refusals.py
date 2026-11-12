"""Business-state exceptions send the original request and verify the real refusal."""

import json
import os
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import probe_refusals
from jsonschema import ValidationError


class RefusalServer(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        self.server.received.append(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        answer = json.dumps(self.server.answer).encode()
        self.send_response(self.server.status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(answer)))
        self.end_headers()
        self.wfile.write(answer)


class RefusalProbes(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.directory = Path(directory.name)
        self.evidence = self.directory / "replays.jsonl"
        error = {
            "type": "object", "required": ["error"],
            "properties": {"error": {
                "type": "object", "required": ["code", "message"],
                "properties": {"code": {"type": "string"}, "message": {"type": "string"}},
            }},
        }
        report = {
            "type": "object", "required": ["organization-name", "policies"],
            "properties": {
                "organization-name": {"type": "string", "minLength": 1},
                "policies": {"type": "array", "minItems": 1, "items": {"type": "object"}},
            },
        }
        spec = {"components": {"schemas": {"Report": report, "Error": error}}, "paths": {
            "/.well-known/smtp-tlsrpt": {"post": {
                "operationId": "receiveTlsReport",
                "requestBody": {"content": {"application/tlsrpt+json": {"schema": {"$ref": "#/components/schemas/Report"}}}},
                "responses": {"422": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}},
            }},
            "/api/v1/admin/mailboxes/{id}/invitation": {"post": {
                "operationId": "inviteAccount",
                "responses": {"409": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}},
            }},
        }}
        spec_path = self.directory / "openapi.json"
        spec_path.write_text(json.dumps(spec))
        self.environment = patch.dict(os.environ, {"CONTRACT_SPEC": str(spec_path), "CONTRACT_PROBE_REPLAYS": str(self.evidence)})
        self.environment.start()
        self.addCleanup(self.environment.stop)
        probe_refusals.description.cache_clear()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), RefusalServer)
        self.server.received = []
        self.server.status = 422
        self.server.answer = {"error": {"code": "no_matching_domains", "message": "No policy domain is hosted"}}
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def transaction(self, body):
        return {
            "operationId": "receiveTlsReport", "name": "report > 201 > application/json",
            "protocol": "http:", "host": "127.0.0.1", "port": str(self.server.server_address[1]),
            "fullPath": "/.well-known/smtp-tlsrpt",
            "request": {"method": "POST", "headers": {"Content-Type": "application/tlsrpt+json"}, "body": body},
        }

    def test_a_domainless_report_is_sent_byte_for_byte_and_recorded(self):
        body = '{ "organization-name": "reporter", "policies": [{}] }'
        transaction = self.transaction(body)
        probe_refusals.tls_report_refusal(transaction)
        self.assertEqual(self.server.received, [body.encode()])
        self.assertEqual(transaction["request"]["body"], body)
        self.assertTrue(transaction["skip"])
        evidence = json.loads(self.evidence.read_text())
        self.assertEqual((evidence["status"], evidence["code"]), (422, "no_matching_domains"))
        self.assertIn("External reports", evidence["reason"])

    def test_invalid_shape_requires_validation_failed_not_a_business_refusal(self):
        transaction = self.transaction('{"policies": [{}]}')
        with self.assertRaises(RuntimeError):
            probe_refusals.tls_report_refusal(transaction)
        self.assertNotIn("skip", transaction)
        self.assertFalse(self.evidence.exists())
        self.server.answer["error"]["code"] = "validation_failed"
        probe_refusals.tls_report_refusal(transaction)
        self.assertTrue(transaction["skip"])

    def test_wrong_status_code_or_response_schema_never_becomes_an_exception(self):
        for status, answer in (
            (201, {"data": {"token": "token-that-must-not-be-logged"}}),
            (500, {"error": {"code": "internal_error", "message": "failed"}}),
            (422, {"error": {"code": "validation_failed", "message": "wrong refusal"}}),
            (422, {"error": {"code": "no_matching_domains"}}),
        ):
            with self.subTest(status=status, answer=answer):
                self.server.status, self.server.answer = status, answer
                transaction = self.transaction('{"organization-name":"reporter","policies":[{}]}')
                with self.assertRaises((RuntimeError, ValidationError)) as failure:
                    probe_refusals.tls_report_refusal(transaction)
                self.assertNotIn("token-that-must-not-be-logged", str(failure.exception))
                self.assertNotIn("skip", transaction)
                self.assertFalse(self.evidence.exists())

    def test_the_invitation_minimum_id_is_actually_sent_for_its_409(self):
        transaction = self.transaction("")
        transaction.update(operationId="inviteAccount", fullPath="/api/v1/admin/mailboxes/1/invitation")
        self.server.status = 409
        self.server.answer = {"error": {"code": "account_not_invitable", "message": "Already holds credentials"}}
        probe_refusals.send_refusal_probe(transaction, 409, "account_not_invitable", "The fixture administrator holds credentials")
        self.assertEqual(self.server.received, [b""])
        self.assertTrue(transaction["skip"])

    def test_the_gate_requires_a_receipt_for_every_ended_business_probe(self):
        transaction = self.transaction('{"organization-name":"reporter","policies":[{}]}')
        probe_refusals.tls_report_refusal(transaction)
        lost = self.directory / "lost.txt"
        lost.write_text("receiveTlsReport\ncreatePipeline\n")
        self.assertEqual(probe_refusals.check_replays(lost, self.evidence), 0)
        lost.write_text("receiveTlsReport\nreceiveTlsReport\n")
        with self.assertRaises(ValueError):
            probe_refusals.check_replays(lost, self.evidence)
        lost.write_text("")
        with self.assertRaises(ValueError):
            probe_refusals.check_replays(lost, self.evidence)

    def test_another_status_cannot_be_added_to_the_exception_receipts(self):
        receipt = {"operation": "receiveTlsReport", "status": 500, "code": "internal_error", "reason": "invalid", "request_sha256": "0" * 64}
        self.evidence.write_text(json.dumps(receipt) + "\n")
        lost = self.directory / "lost.txt"
        lost.write_text("receiveTlsReport\n")
        with self.assertRaises(ValueError):
            probe_refusals.check_replays(lost, self.evidence)


if __name__ == "__main__":
    unittest.main()
