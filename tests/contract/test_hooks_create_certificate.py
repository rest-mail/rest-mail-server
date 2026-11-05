"""The createCertificate hook makes the documented certificate for the domain
the request uploads it to, since the server refuses one that does not name
that domain (#1704)."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import threading
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.before = stub.after = lambda name: (lambda fn: fn)
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])

    def set_json(transaction, body):
        transaction["request"]["body"] = json.dumps(body)

    stub.set_json = set_json
    spec = importlib.util.spec_from_file_location("contract_create_certificate_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, stub


class FixtureAPI(BaseHTTPRequestHandler):
    """The admin endpoints the hook calls, over an in-memory fixture."""

    def log_message(self, *args):
        pass

    def answer(self, status, body=None):
        data = json.dumps(body).encode() if body is not None else b""
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/api/v1/admin/domains/7":
            return self.answer(200, {"data": {"id": 7, "name": "seven.example.org"}})
        if self.path == "/api/v1/admin/domains/9":
            return self.answer(200, {"data": {"id": 9, "name": "made.example.org"}})
        self.answer(404, {"error": {"code": "not_found"}})

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        if self.path == "/api/v1/auth/login":
            return self.answer(200, {"data": {"access_token": "setup"}})
        self.answer(404)


def names_in(cert_pem):
    out = subprocess.run(["openssl", "x509", "-noout", "-ext", "subjectAltName"],
                         input=cert_pem, capture_output=True, text=True, check=True).stdout
    return sorted(part.strip()[len("DNS:"):] for part in out.splitlines()[-1].split(",") if "DNS:" in part)


class CreateCertificateHookTests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.hooks, self.stub = load_hooks()

    def transaction(self, body, name="createCertificate > 201 > valid"):
        return {
            "name": name, "protocol": "http:", "host": "127.0.0.1", "port": str(self.server.server_address[1]),
            "fullPath": "/api/v1/admin/certificates",
            "request": {"body": json.dumps(body), "headers": {}},
        }

    def upload(self, domain_id):
        transaction = self.transaction({"domain_id": domain_id, "cert_pem": "certificate", "key_pem": "key"})
        self.hooks.a_self_signed_certificate(transaction)
        return json.loads(transaction["request"]["body"])

    def test_the_certificate_names_the_domain_it_is_uploaded_to(self):
        body = self.upload(7)
        self.assertEqual(body["domain_id"], 7)
        self.assertEqual(names_in(body["cert_pem"]), ["seven.example.org"])
        self.assertIn("PRIVATE KEY-----", body["key_pem"])

    def test_the_documented_domain_stands_for_the_one_the_run_made(self):
        self.stub.stash.update({"domain_id": 9, "domain": "made.example.org"})
        body = self.upload(1)
        self.assertEqual(body["domain_id"], 9)
        self.assertEqual(names_in(body["cert_pem"]), ["made.example.org"])

    def test_a_domain_the_run_has_deleted_gives_way_to_one_of_the_certificates_own(self):
        self.stub.stash.update({"domain_id": 404, "domain": "deleted.example.org"})
        made = {"id": 31, "name": "contract-certificate-1.test"}
        answers = {
            ("GET", "/api/v1/admin/domains/404"): None,
            ("POST", "/api/v1/admin/domains"): made,
            ("GET", "/api/v1/admin/domains/31"): made,
        }
        calls = []

        def fixture_call(_, method, path, *rest, **options):
            calls.append((method, path))
            return answers[(method, path)]

        with patch.object(self.hooks, "fixture_call", side_effect=fixture_call):
            first, second = self.upload(1), self.upload(1)
        self.assertEqual((first["domain_id"], second["domain_id"]), (31, 31))
        self.assertEqual(names_in(first["cert_pem"]), [made["name"]])
        # One domain, and what else reads the run's domain keeps its id.
        self.assertEqual(calls.count(("POST", "/api/v1/admin/domains")), 1)
        self.assertEqual(self.stub.stash["domain_id"], 404)

    def test_a_domain_that_does_not_answer_keeps_the_placeholder_name(self):
        body = self.upload(2147483647)
        self.assertEqual(names_in(body["cert_pem"]), ["example.org"])


if __name__ == "__main__":
    unittest.main()
