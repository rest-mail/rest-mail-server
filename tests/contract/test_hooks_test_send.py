"""The sendTestEmail hook gives the coverage probes' addresses a hosted domain
and a mailbox, once, without touching the request."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import sys
import threading
import types
import unittest
import unittest.mock
import urllib.parse


def stub_vertrag_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    module = types.ModuleType("vertrag_hooks")
    module.stash = {}
    module.before = module.after = lambda name: (lambda fn: fn)
    module.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    module.set_json = lambda transaction, body: transaction["request"].__setitem__("body", json.dumps(body))
    sys.modules["vertrag_hooks"] = module


if "vertrag_hooks" not in sys.modules:
    stub_vertrag_hooks()
import hooks  # noqa: E402


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

    def read_body(self):
        return json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"null")

    def do_POST(self):
        state = self.server.state
        state["calls"].append("POST " + self.path)
        if self.path == "/api/v1/auth/login":
            return self.answer(200, {"data": {"access_token": "setup"}})
        if self.path == "/api/v1/admin/domains":
            name = self.read_body()["name"]
            if name.endswith(".invalid"):
                return self.answer(422, {"error": {"code": "validation_error"}})
            domain = {"id": len(state["domains"]) + 1, "name": name}
            state["domains"].append(domain)
            return self.answer(201, {"data": domain})
        if self.path == "/api/v1/accounts":
            address = self.read_body()["address"]
            domain = next(d for d in state["domains"] if d["name"] == address.split("@")[1])
            account = {"id": 100 + len(state["accounts"]), "domain_id": domain["id"], "address": address}
            state["accounts"].append(account)
            return self.answer(201, {"data": account})
        self.answer(404)

    def do_GET(self):
        state = self.server.state
        state["calls"].append("GET " + self.path)
        url = urllib.parse.urlparse(self.path)
        if url.path == "/api/v1/admin/domains":
            return self.answer(200, {"data": state["domains"]})
        if url.path == "/api/v1/admin/mailboxes":
            domain_id = int(urllib.parse.parse_qs(url.query)["domain_id"][0])
            return self.answer(200, {"data": [a for a in state["accounts"] if a["domain_id"] == domain_id]})
        self.answer(404)


class SendTestEmailProbes(unittest.TestCase):
    def setUp(self):
        hooks.hooks.stash.clear()
        # Making a mailbox takes a second factor: the two-factor administrator,
        # which this fixture API answers as it does the admin.
        two_factor = unittest.mock.patch.object(hooks, "mfa_admin_token", return_value="setup")
        two_factor.start()
        self.addCleanup(two_factor.stop)
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.state = {
            "domains": [{"id": 1, "name": "restmail.test"}],
            "accounts": [
                {"id": 1, "domain_id": 1, "address": "admin@restmail.test"},
                {"id": 2, "domain_id": 1, "address": "postmaster@restmail.test"},
            ],
            "calls": [],
        }
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def probe(self, body):
        transaction = {
            "protocol": "http:", "host": "127.0.0.1", "port": self.server.server_port,
            "request": {"body": json.dumps(body)},
        }
        before = transaction["request"]["body"]
        hooks.a_mailbox_at_each_test_send_address(transaction)
        self.assertEqual(transaction["request"]["body"], before, "the hook rewrote the request")

    def addresses(self):
        return sorted(a["address"] for a in self.server.state["accounts"])

    def test_hosts_the_domain_and_creates_the_mailbox_once(self):
        self.probe({"from": "someone@example.com", "to": "someone@example.com"})
        self.assertEqual([d["name"] for d in self.server.state["domains"]], ["restmail.test", "example.com"])
        self.assertIn("someone@example.com", self.addresses())

        calls = len(self.server.state["calls"])
        self.probe({"body": "", "from": "someone@example.com", "to": "someone@example.com"})
        self.assertEqual(len(self.server.state["calls"]), calls, "a repeated probe looked the mailbox up again")
        self.assertEqual(self.addresses().count("someone@example.com"), 1)

    def test_leaves_existing_mailboxes_alone(self):
        self.probe({"from": "postmaster@restmail.test", "to": "admin@restmail.test"})
        self.assertEqual(len(self.server.state["accounts"]), 2)
        self.assertNotIn("POST /api/v1/accounts", self.server.state["calls"])

    def test_leaves_malformed_and_unhostable_addresses_to_the_probe(self):
        for body in ({}, [], {"from": ""}, {"to": "not-an-address"}, {"from": "a@b"},
                     {"from": 7, "to": None}, {"from": "someone@nowhere.invalid"}):
            self.probe(body)
        self.assertEqual([d["name"] for d in self.server.state["domains"]], ["restmail.test"])
        self.assertEqual(len(self.server.state["accounts"]), 2)


if __name__ == "__main__":
    unittest.main()
