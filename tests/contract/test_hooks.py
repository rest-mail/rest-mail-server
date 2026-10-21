"""The createAccount hook hosts the coverage probes' domain and lets each
repeated probe create its account afresh, without touching the request."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import sys
import threading
import types
import unittest
import urllib.parse


def stub_vertrag_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    module = types.ModuleType("vertrag_hooks")
    module.stash = {}
    module.before = module.after = lambda name: (lambda fn: fn)
    module.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    module.set_json = lambda transaction, body: transaction["request"].__setitem__("body", json.dumps(body))
    sys.modules["vertrag_hooks"] = module
    return module


vertrag_hooks = stub_vertrag_hooks()
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
        if self.path == "/api/v1/auth/login":
            return self.answer(200, {"data": {"access_token": "setup"}})
        if self.path == "/api/v1/admin/domains":
            name = self.read_body()["name"]
            if name.endswith(".invalid"):
                return self.answer(422, {"error": {"code": "validation_error"}})
            domain = {"id": len(state["domains"]) + 1, "name": name}
            state["domains"].append(domain)
            return self.answer(201, {"data": domain})
        self.answer(404)

    def do_GET(self):
        state = self.server.state
        url = urllib.parse.urlparse(self.path)
        if url.path == "/api/v1/admin/domains":
            return self.answer(200, {"data": state["domains"]})
        if url.path == "/api/v1/admin/mailboxes":
            domain_id = int(urllib.parse.parse_qs(url.query)["domain_id"][0])
            return self.answer(200, {"data": [a for a in state["accounts"] if a["domain_id"] == domain_id]})
        self.answer(404)

    def do_DELETE(self):
        state = self.server.state
        account_id = int(self.path.rsplit("/", 1)[1])
        state["deleted"].append(account_id)
        state["accounts"] = [a for a in state["accounts"] if a["id"] != account_id]
        self.answer(204)


class CreateAccountProbes(unittest.TestCase):
    def setUp(self):
        vertrag_hooks.stash.clear()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.state = {
            "domains": [{"id": 1, "name": "restmail.test"}],
            "accounts": [{"id": 1, "domain_id": 1, "address": "admin@restmail.test"}],
            "deleted": [],
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
        hooks.host_the_account_probe_domain(transaction)
        self.assertEqual(transaction["request"]["body"], before, "the hook rewrote the request")

    def create(self, address):
        """What a successful probe leaves behind."""
        state = self.server.state
        domain = next(d for d in state["domains"] if d["name"] == address.split("@")[1])
        state["accounts"].append({"id": 100 + len(state["accounts"]), "domain_id": domain["id"], "address": address})

    def test_hosts_the_domain_and_removes_only_its_own_repeated_probe(self):
        self.probe({"address": "someone@example.com"})
        self.assertIn("example.com", [d["name"] for d in self.server.state["domains"]])
        self.assertEqual(self.server.state["deleted"], [])
        self.create("someone@example.com")

        self.probe({"address": "someone@example.com", "display_name": ""})
        self.assertEqual(len(self.server.state["deleted"]), 1)
        self.assertEqual(len([d for d in self.server.state["domains"] if d["name"] == "example.com"]), 1)

    def test_leaves_an_account_it_did_not_create(self):
        self.probe({"address": "admin@restmail.test"})
        self.assertEqual(self.server.state["deleted"], [])
        self.assertEqual(len(self.server.state["accounts"]), 1)

    def test_leaves_malformed_and_unhostable_addresses_to_the_probe(self):
        for body in ({}, [], {"address": ""}, {"address": "not-an-address"}, {"address": "a@b"},
                     {"address": 7}, {"address": "someone@nowhere.invalid"}):
            self.probe(body)
        self.assertEqual([d["name"] for d in self.server.state["domains"]], ["restmail.test"])
        self.assertEqual(self.server.state["deleted"], [])


if __name__ == "__main__":
    unittest.main()
