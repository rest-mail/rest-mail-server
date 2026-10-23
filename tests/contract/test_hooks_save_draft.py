"""The saveDraft hook gives a probe's sender a hosted domain and a mailbox,
once, without touching the request."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import copy
import importlib.util
import json
from pathlib import Path
import sys
import threading
import types
import unittest
from unittest.mock import patch
import urllib.parse

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.before = stub.after = lambda name: (lambda fn: fn)
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    spec = importlib.util.spec_from_file_location("contract_save_draft_hooks", ROOT / "tests/contract/hooks.py")
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

    def read_body(self):
        return json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"null")

    def do_GET(self):
        state = self.server.state
        state["calls"].append("GET " + self.path)
        url = urllib.parse.urlsplit(self.path)
        if url.path == "/api/v1/admin/domains":
            return self.answer(200, {"data": state["domains"]})
        if url.path == "/api/v1/admin/mailboxes":
            domain_id = int(urllib.parse.parse_qs(url.query)["domain_id"][0])
            return self.answer(200, {"data": [m for m in state["mailboxes"] if m["domain_id"] == domain_id]})
        self.answer(404)

    def do_POST(self):
        state = self.server.state
        state["calls"].append("POST " + self.path)
        if self.path == "/api/v1/auth/login":
            return self.answer(200, {"data": {"access_token": "setup"}})
        body = self.read_body()
        if self.path == "/api/v1/admin/domains":
            if body["name"] in state["unhostable"]:
                return self.answer(422, {"error": {"code": "invalid", "message": "no"}})
            domain = {"id": len(state["domains"]) + 1, "name": body["name"]}
            state["domains"].append(domain)
            return self.answer(201, {"data": domain})
        if self.path == "/api/v1/accounts":
            domain = body["address"].split("@")[1]
            domain_id = next(d["id"] for d in state["domains"] if d["name"] == domain)
            state["mailboxes"].append({"address": body["address"], "domain_id": domain_id})
            return self.answer(201, {"data": {"address": body["address"]}})
        self.answer(404)


class SaveDraftHookTest(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.state = {
            "calls": [],
            "domains": [{"id": 1, "name": "restmail.test"}],
            "mailboxes": [{"address": "admin@restmail.test", "domain_id": 1}],
            "unhostable": {"localhost.localdomain"},
        }
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.hooks, self.stub = load_hooks()

    def transaction(self, body):
        return {
            "name": "/api/v1/messages/draft > Save a new draft > 201 > application/json",
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_address[1]),
            "request": {"body": json.dumps(body), "headers": {"Authorization": "Bearer probe"}},
        }

    def test_a_generated_sender_gets_a_domain_and_a_mailbox_once(self):
        first = self.transaction({"from": "someone@example.com", "cc": []})
        untouched = copy.deepcopy(first)
        self.hooks.a_mailbox_for_the_draft(first)
        self.assertEqual(first, untouched)
        state = self.server.state
        self.assertIn({"id": 2, "name": "example.com"}, state["domains"])
        self.assertIn({"address": "someone@example.com", "domain_id": 2}, state["mailboxes"])

        calls = len(state["calls"])
        self.hooks.a_mailbox_for_the_draft(self.transaction({"from": "Someone@Example.com"}))
        self.assertEqual(len(state["calls"]), calls, "a sender already given a mailbox is not set up again")

    def test_an_existing_mailbox_is_left_as_it_is(self):
        self.hooks.a_mailbox_for_the_draft(self.transaction({"from": "admin@restmail.test"}))
        self.assertNotIn("POST /api/v1/accounts", self.server.state["calls"])
        self.assertNotIn("POST /api/v1/admin/domains", self.server.state["calls"])

    def test_malformed_and_missing_senders_are_left_to_the_probe(self):
        for body in ({}, {"from": "not-an-email"}, {"from": 7}, {"from": "a@b@c.test"}, {"from": "@example.com"}):
            self.hooks.a_mailbox_for_the_draft(self.transaction(body))
        self.assertEqual(self.server.state["calls"], [])

    def test_a_domain_the_server_will_not_host_is_left_to_the_probe(self):
        self.hooks.a_mailbox_for_the_draft(self.transaction({"from": "x@localhost.localdomain"}))
        self.assertNotIn("POST /api/v1/accounts", self.server.state["calls"])
        self.assertEqual(len(self.server.state["mailboxes"]), 1)


if __name__ == "__main__":
    unittest.main()
