"""The sendMessage hook gives a probe's sender a hosted, signing domain and a
mailbox, once, without touching the request."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import runpy
import sys
import threading
import types
import unittest
from unittest.mock import Mock, patch
import urllib.parse

ROOT = Path(__file__).resolve().parents[2]
REAL_NAME = "/api/v1/messages/send > Send a message > 201 > application/json"


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.registered = []
    stub.before = lambda name: lambda fn: stub.registered.append((name, fn)) or fn
    stub.after = lambda name: lambda fn: fn
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        loaded = runpy.run_path(str(ROOT / "tests/contract/entrypoint.py"))
    return types.SimpleNamespace(**loaded["shared"]), stub


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
            mailbox = {"id": 100 + len(state["mailboxes"]), "domain_id": domain["id"], "address": address}
            state["mailboxes"].append(mailbox)
            return self.answer(201, {"data": mailbox})
        self.answer(404)

    def do_PUT(self):
        self.server.state["calls"].append("PUT " + self.path)
        body = self.read_body()
        if not body.get("private_key", "").startswith("-----BEGIN"):
            return self.answer(400)
        self.server.state["signing"].add(int(self.path.rsplit("/", 1)[1]))
        self.answer(200, {"data": {"selector": body["selector"]}})

    def do_GET(self):
        state = self.server.state
        state["calls"].append("GET " + self.path)
        url = urllib.parse.urlparse(self.path)
        if url.path == "/api/v1/admin/domains":
            return self.answer(200, {"data": state["domains"]})
        if url.path == "/api/v1/admin/dkim":
            return self.answer(200, {"data": [{"domain_id": d["id"], "domain": d["name"], "has_key": True}
                                              for d in state["domains"] if d["id"] in state["signing"]]})
        if url.path == "/api/v1/admin/mailboxes":
            domain_id = int(urllib.parse.parse_qs(url.query)["domain_id"][0])
            return self.answer(200, {"data": [m for m in state["mailboxes"] if m["domain_id"] == domain_id]})
        self.answer(404)


class SendMessageProbes(unittest.TestCase):
    def setUp(self):
        self.hooks, self.stub = load_hooks()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.state = {"calls": [], "domains": [{"id": 1, "name": "restmail.test"}], "signing": {1},
                             "mailboxes": [{"id": 7, "domain_id": 1, "address": "admin@restmail.test"}]}
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.policy = Mock()
        patcher = patch.dict(
            self.hooks.a_sending_policy_for_the_send.__globals__,
            {"set_the_admin_domains_policy": self.policy},
        )
        patcher.start()
        self.addCleanup(patcher.stop)

    def send(self, body, name=REAL_NAME):
        transaction = {"name": name, "fullPath": "/api/v1/messages/send",
                       "protocol": "http:", "host": "127.0.0.1", "port": str(self.server.server_port),
                       "request": {"body": json.dumps(body), "headers": {"Authorization": "Bearer probe"}}}
        before = json.loads(json.dumps(transaction))
        self.hooks.a_sending_policy_for_the_send(transaction)
        self.assertEqual(transaction, before, "the request must stay as generated")

    def test_entrypoint_registers_the_operation_hook(self):
        self.assertIn(
            ("sendMessage", self.hooks.a_sending_policy_for_the_send),
            self.stub.registered,
        )

    def test_real_transaction_names_select_the_sending_policy(self):
        body = {"from": "admin@restmail.test", "to": ["admin@restmail.test"]}
        self.send(body)
        self.assertEqual(self.policy.call_args.args[1], "")
        self.send(body, REAL_NAME.replace(" > 201 > ", " > 422 > "))
        self.assertEqual(self.policy.call_args.args[1], "transactional")

    def test_the_documented_foreign_sender_gets_no_fixture(self):
        self.send({"from": "someone@other.test", "to": ["admin@restmail.test"]},
                  REAL_NAME.replace(" > 201 > ", " > 403 > "))
        self.assertEqual(self.server.state["calls"], [])

    def test_an_unknown_sender_gets_a_signing_domain_and_a_mailbox_once(self):
        self.send({"from": "someone@example.com", "to": ["someone@example.com"]})
        self.send({"from": "someone@example.com", "to": ["someone@example.com"], "subject": ""})
        state = self.server.state
        self.assertEqual([d["name"] for d in state["domains"]], ["restmail.test", "example.com"])
        self.assertEqual([m["address"] for m in state["mailboxes"]], ["admin@restmail.test", "someone@example.com"])
        self.assertEqual(state["calls"].count("PUT /api/v1/admin/dkim/2"), 1)
        self.assertEqual(state["calls"].count("POST /api/v1/accounts"), 1)

    def test_a_domain_hosted_without_a_key_gets_one(self):
        # The createAccount hook hosts probe domains too, without a key.
        self.server.state["domains"].append({"id": 2, "name": "example.com"})
        self.send({"from": "someone@example.com", "to": ["someone@example.com"]})
        state = self.server.state
        self.assertNotIn("POST /api/v1/admin/domains", state["calls"])
        self.assertEqual(state["calls"].count("PUT /api/v1/admin/dkim/2"), 1)
        self.assertEqual([m["address"] for m in state["mailboxes"]], ["admin@restmail.test", "someone@example.com"])

    def test_a_hosted_sender_is_left_alone(self):
        self.send({"from": "admin@restmail.test", "to": ["someone@example.com"]})
        calls = self.server.state["calls"]
        self.assertNotIn("POST /api/v1/admin/domains", calls)
        self.assertNotIn("POST /api/v1/accounts", calls)
        self.assertFalse([c for c in calls if c.startswith("PUT ")])

    def test_malformed_or_unhostable_senders_are_left_to_the_probe(self):
        for sender in ("", "no-at-sign", "a@b@c", "someone@localhost", 42, "someone@example.invalid"):
            with self.subTest(sender=sender):
                self.send({"from": sender, "to": ["someone@example.com"]})
        self.send({"to": ["someone@example.com"]})
        self.send(["not", "an", "object"])
        self.assertNotIn("POST /api/v1/accounts", self.server.state["calls"])
        self.assertEqual(len(self.server.state["domains"]), 1)


if __name__ == "__main__":
    unittest.main()
