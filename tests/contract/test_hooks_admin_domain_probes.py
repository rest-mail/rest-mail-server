"""Coverage probes that would change restmail.test's signing key or settings
go to a disposable domain instead (#1167)."""

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
    registered = []
    stub.before = lambda name: (lambda fn: registered.append((name, fn.__name__)) or fn)
    stub.after = lambda name: (lambda fn: fn)
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    spec = importlib.util.spec_from_file_location("contract_admin_domain_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, stub, registered


class FixtureAPI(BaseHTTPRequestHandler):
    """The admin domain endpoints the hook calls, over an in-memory fixture."""

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
        if self.path == "/api/v1/admin/domains":
            return self.answer(200, {"data": [{"id": 1, "name": "restmail.test"}, {"id": 18, "name": "example.org"}]})
        self.answer(404)

    def do_POST(self):
        self.server.calls.append("POST " + self.path)
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        if self.path == "/api/v1/admin/domains":
            self.server.created.append(body["name"])
            return self.answer(201, {"data": {"id": 30, "name": body["name"]}})
        self.answer(404)


class AdminDomainProbeHookTests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.calls, self.server.created = [], []
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.hooks, self.stub, self.registered = load_hooks()
        self.origin = "http://127.0.0.1:" + str(self.server.server_address[1])
        self.stub.stash["setup_admin_tokens"] = {self.origin: "setup"}

    def transaction(self, path, name, body=None):
        return {
            "name": name,
            "protocol": "http:", "host": "127.0.0.1", "port": str(self.server.server_address[1]),
            "fullPath": path,
            "request": {"body": json.dumps(body if body is not None else {}), "headers": {}},
        }

    def test_registered_before_each_mutating_dkim_operation(self):
        for operation in ("setDKIMKey", "deleteDKIMKey", "setDKIMRotation"):
            with self.subTest(operation=operation):
                self.assertIn((operation, "off_the_admin_domain"), self.registered)
        # setDKIMKey's key substitution only reads the body, so either order holds.
        self.assertIn(("updateDomain", "a_certificate_before_going_live"), self.registered)

    def test_coverage_probes_on_restmail_test_move_to_one_disposable_domain(self):
        for path, moved in (
            ("/api/v1/admin/dkim/1", "/api/v1/admin/dkim/30"),
            ("/api/v1/admin/dkim/1/rotation", "/api/v1/admin/dkim/30/rotation"),
            ("/api/v1/admin/domains/1", "/api/v1/admin/domains/30"),
            ("/api/v1/admin/domains/1?x=1", "/api/v1/admin/domains/30?x=1"),
        ):
            with self.subTest(path=path):
                transaction = self.transaction(path, "coverage: " + path + " > 200 · body · valid")
                self.hooks.off_the_admin_domain(transaction)
                self.assertEqual(transaction["fullPath"], moved)
        self.assertEqual(len(self.server.created), 1)
        self.assertTrue(self.server.created[0].startswith("contract-probe-"))

    def test_dmarc_reject_probe_does_not_patch_restmail_test(self):
        transaction = self.transaction(
            "/api/v1/admin/domains/1",
            'coverage: /api/v1/admin/domains/{id} > Update a domain > 200 · body · "dmarc_policy" at enum member "reject"',
            {"dmarc_policy": "reject"},
        )
        self.hooks.a_certificate_before_going_live(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/30")

    def test_other_targets_and_transactions_are_left_alone(self):
        for path, name in (
            ("/api/v1/admin/dkim/18", "coverage: probe"),
            ("/api/v1/admin/dkim/0", "coverage: probe"),
            ("/api/v1/admin/dkim/1.5", "coverage: probe"),
            ("/api/v1/admin/dkim/abc", "coverage: probe"),
            ("/api/v1/admin/dkim/1", "/api/v1/admin/dkim/{id} > Set or replace DKIM key for a domain > 200"),
            ("/api/v1/admin/domains/1", "ownership: PATCH /api/v1/admin/domains/{id} as domain-admin-b"),
            ("/api/v1/admin/domains/1", "authorization: PATCH /api/v1/admin/domains/{id} as admin"),
        ):
            with self.subTest(path=path, name=name):
                transaction = self.transaction(path, name)
                self.hooks.off_the_admin_domain(transaction)
                self.assertEqual(transaction["fullPath"], path)
        self.assertEqual(self.server.created, [])


if __name__ == "__main__":
    unittest.main()
