"""The updateDomain hook gives a domain a certificate before a request sets
active to true, unless the API lists a valid one, without touching the request.
In the own-data phase each exchange starts clean on a domain of its own (#1385)."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
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
    spec = importlib.util.spec_from_file_location("contract_update_domain_hooks", ROOT / "tests/contract/hooks.py")
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
        self.server.calls.append("GET " + self.path)
        if self.path == "/api/v1/admin/certificates":
            return self.answer(200, {"data": self.server.listed + [
                {"id": n + 1, "domain_id": c["domain_id"], "not_before": "2026-01-01T00:00:00Z",
                 "not_after": "2999-01-01T00:00:00Z"}
                for n, c in enumerate(self.server.certificates)
            ]})
        if self.path == "/api/v1/admin/domains/7":
            return self.answer(200, {"data": {"id": 7, "name": "example.org"}})
        self.answer(404, {"error": {"code": "domain_not_found"}})

    def do_POST(self):
        self.server.calls.append("POST " + self.path)
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        if self.path == "/api/v1/auth/login":
            return self.answer(200, {"data": {"access_token": "setup"}})
        if self.path == "/api/v1/admin/certificates":
            self.server.certificates.append(body)
            return self.answer(201, {"data": {"id": len(self.server.certificates)}})
        self.answer(404)


class UpdateDomainHookTests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.calls, self.server.certificates, self.server.listed = [], [], []
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.hooks, self.stub = load_hooks()

    def transaction(self, body, domain_id="7", status=200):
        return {
            "protocol": "http:", "host": "127.0.0.1", "port": str(self.server.server_address[1]),
            "fullPath": "/api/v1/admin/domains/" + domain_id,
            "name": f"/api/v1/admin/domains/{domain_id} > Update a domain > {status} > application/json",
            "request": {"body": json.dumps(body), "headers": {}},
        }

    def test_going_live_gets_one_certificate_and_the_request_is_untouched(self):
        for _ in range(2):
            transaction = self.transaction({"active": True, "default_quota_bytes": 0})
            before = json.dumps(transaction, sort_keys=True)
            self.hooks.a_certificate_before_going_live(transaction)
            self.assertEqual(json.dumps(transaction, sort_keys=True), before)
        self.assertEqual(len(self.server.certificates), 1)
        certificate = self.server.certificates[0]
        self.assertEqual(certificate["domain_id"], 7)
        self.assertTrue(certificate["cert_pem"].startswith("-----BEGIN CERTIFICATE-----"))
        self.assertIn("PRIVATE KEY-----", certificate["key_pem"])
        # Whether the domain has one is read from the API, not the stash.
        self.assertEqual(self.server.calls.count("GET /api/v1/admin/certificates"), 2)
        self.assertNotIn("certified_domains", self.stub.stash)

    def test_a_certificate_listed_valid_is_enough_and_an_expired_one_is_not(self):
        for listed, made in (
            ({"domain_id": 7, "not_before": "2026-01-01T00:00:00Z", "not_after": "2999-01-01T00:00:00Z"}, 0),
            ({"domain_id": 7, "not_before": "2020-01-01T00:00:00Z", "not_after": "2021-01-01T00:00:00Z"}, 1),
            ({"domain_id": 7, "not_before": "2999-01-01T00:00:00Z", "not_after": "2999-02-01T00:00:00Z"}, 1),
            ({"domain_id": 8, "not_before": "2026-01-01T00:00:00Z", "not_after": "2999-01-01T00:00:00Z"}, 1),
        ):
            with self.subTest(listed=listed):
                self.server.listed, self.server.certificates = [listed], []
                self.hooks.a_certificate_before_going_live(self.transaction({"active": True}))
                self.assertEqual(len(self.server.certificates), made)

    def test_other_updates_and_unknown_domains_are_left_alone(self):
        for body, domain_id in (({}, "7"), ({"active": False}, "7"), ({"active": "true"}, "7"),
                                ([], "7"), ({"active": True}, "0"), ({"active": True}, "x"),
                                ({"active": True}, "99")):
            with self.subTest(body=body, domain_id=domain_id):
                self.hooks.a_certificate_before_going_live(self.transaction(body, domain_id))
        self.assertEqual(self.server.certificates, [])


class CleanStart(unittest.TestCase):
    """After a reset each exchange is sent to a domain of its own."""

    def setUp(self):
        self.hooks, self.stub = load_hooks()
        self.start_clean = patch.object(self.hooks, "start_clean").start()
        self.domain = patch.object(self.hooks, "a_domain_of_its_own", return_value=9).start()
        self.certify = patch.object(self.hooks, "admin_token", side_effect=AssertionError("no lookup")).start()
        self.addCleanup(patch.stopall)

    def transaction(self, domain_id, status, body):
        return {
            "protocol": "http:", "host": "127.0.0.1", "port": "1",
            "fullPath": "/api/v1/admin/domains/" + domain_id + "?keep=yes",
            "name": f"/api/v1/admin/domains/{domain_id} > Update a domain > {status} > application/json",
            "request": {"body": json.dumps(body), "headers": {"Authorization": "Bearer two-factor"}},
        }

    def test_without_a_reset_nothing_moves(self):
        transaction = self.transaction("5", 200, {"sending_policy": "transactional"})
        before = json.dumps(transaction, sort_keys=True)
        self.hooks.a_certificate_before_going_live(transaction)
        self.assertEqual(json.dumps(transaction, sort_keys=True), before)
        self.start_clean.assert_not_called()
        self.domain.assert_not_called()

    def test_after_a_reset_every_exchange_starts_clean_and_links_get_their_own_domain(self):
        patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"}).start()
        for domain_id, status, sent in (
            ("5", 200, "9"),
            ("5", 422, "9"),
            ("1", 422, "9"),
            ("2147483647", 404, "2147483647"),
            ("0", 422, "0"),
            ("x", 422, "x"),
        ):
            with self.subTest(domain_id=domain_id, status=status):
                body = {"sending_policy": "newsletter"}
                transaction = self.transaction(domain_id, status, body)
                self.hooks.a_certificate_before_going_live(transaction)
                self.start_clean.assert_called_once_with(transaction)
                self.start_clean.reset_mock()
                self.assertEqual(transaction["fullPath"], f"/api/v1/admin/domains/{sent}?keep=yes")
                self.assertEqual(json.loads(transaction["request"]["body"]), body)
                self.assertEqual(transaction["request"]["headers"], {"Authorization": "Bearer two-factor"})
        self.assertEqual(self.domain.call_count, 3)


if __name__ == "__main__":
    unittest.main()
