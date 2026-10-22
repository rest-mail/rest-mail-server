"""The sender rule and alias hooks let each repeated probe create its entry
afresh, without touching the request, and keep the linked entry alive.

The domain stays for the whole run now that deleteDomain refuses one with
accounts (#388), so nothing clears what an earlier probe made.
"""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import re
import sys
import threading
import types
import unittest
import urllib.parse


def stub_vertrag_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    module = sys.modules.get("vertrag_hooks") or types.ModuleType("vertrag_hooks")
    module.stash = getattr(module, "stash", {})
    module.before = module.after = lambda name: (lambda fn: fn)
    module.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    module.set_json = lambda transaction, body: transaction["request"].__setitem__("body", json.dumps(body))
    sys.modules["vertrag_hooks"] = module
    return module


vertrag_hooks = stub_vertrag_hooks()
import hooks  # noqa: E402

RULES = re.compile(r"^/api/v1/admin/domains/(\d+)/(allowlist|blocklist)(?:/(\d+))?$")
ALIAS = re.compile(r"^/api/v1/admin/aliases/(\d+)$")


class FixtureAPI(BaseHTTPRequestHandler):
    """The admin endpoints the hooks call, over an in-memory fixture."""

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

    def next_id(self):
        state = self.server.state
        state["serial"] += 1
        return state["serial"]

    def do_POST(self):
        state = self.server.state
        url = urllib.parse.urlparse(self.path)
        if url.path == "/api/v1/auth/login":
            return self.answer(200, {"data": {"access_token": "setup"}})
        if url.path == "/api/v1/admin/domains":
            name = self.read_body()["name"]
            if name.endswith(".invalid"):
                return self.answer(422, {"error": {"code": "validation_error"}})
            domain = {"id": self.next_id(), "name": name}
            state["domains"].append(domain)
            return self.answer(201, {"data": domain})
        if url.path == "/api/v1/admin/aliases":
            body = self.read_body()
            domain = next(d for d in state["domains"] if d["name"] == body["source_address"].split("@")[1])
            alias = dict(body, id=self.next_id(), domain_id=domain["id"])
            state["aliases"].append(alias)
            return self.answer(201, {"data": alias})
        match = RULES.match(url.path)
        if match:
            rule = dict(self.read_body(), id=self.next_id(), domain_id=int(match[1]), list=match[2])
            state["rules"].append(rule)
            return self.answer(201, {"data": rule})
        self.answer(404)

    def do_GET(self):
        state = self.server.state
        url = urllib.parse.urlparse(self.path)
        if url.path == "/api/v1/admin/domains":
            return self.answer(200, {"data": state["domains"]})
        if url.path == "/api/v1/admin/aliases":
            domain_id = int(urllib.parse.parse_qs(url.query)["domain_id"][0])
            return self.answer(200, {"data": [a for a in state["aliases"] if a["domain_id"] == domain_id]})
        match = RULES.match(url.path)
        if match and not match[3]:
            if not any(d["id"] == int(match[1]) for d in state["domains"]):
                return self.answer(404)
            return self.answer(200, {"data": [r for r in state["rules"]
                                              if r["domain_id"] == int(match[1]) and r["list"] == match[2]]})
        self.answer(404)

    def do_DELETE(self):
        state = self.server.state
        url = urllib.parse.urlparse(self.path)
        match = RULES.match(url.path) or ALIAS.match(url.path)
        entry_id = int(match.groups()[-1])
        state["deleted"].append(entry_id)
        state["rules"] = [r for r in state["rules"] if r["id"] != entry_id]
        state["aliases"] = [a for a in state["aliases"] if a["id"] != entry_id]
        self.answer(204)


class AdminAPITestCase(unittest.TestCase):
    def setUp(self):
        vertrag_hooks.stash.clear()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.state = {
            "domains": [{"id": 1, "name": "restmail.test"}],
            "rules": [{"id": 2, "domain_id": 1, "list": "allowlist", "pattern": "@seeded.example"}],
            "aliases": [{"id": 3, "domain_id": 1, "source_address": "postmaster@restmail.test"}],
            "deleted": [],
            "serial": 100,
        }
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def transaction(self, path, body=None):
        return {
            "protocol": "http:", "host": "127.0.0.1", "port": self.server.server_port,
            "fullPath": path, "request": {"body": json.dumps(body)},
        }

    def probe(self, hook, path, body):
        transaction = self.transaction(path, body)
        hook(transaction)
        self.assertEqual(transaction["request"]["body"], json.dumps(body), "the hook rewrote the request")
        self.assertEqual(transaction["fullPath"], path, "the hook rewrote the path")

    def created(self, kind, **entry):
        """What a successful probe leaves behind."""
        self.server.state["serial"] += 1
        entry["id"] = self.server.state["serial"]
        self.server.state[kind].append(entry)
        return entry


class SenderRuleProbes(AdminAPITestCase):
    PATH = "/api/v1/admin/domains/1/allowlist"

    def probe_rule(self, body, path=PATH):
        self.probe(hooks.remove_a_previous_sender_rule_probe, path, body)

    def test_removes_only_its_own_repeated_probe(self):
        self.probe_rule({"pattern": "x"})
        self.assertEqual(self.server.state["deleted"], [])
        rule = self.created("rules", domain_id=1, list="allowlist", pattern="x")

        self.probe_rule({"pattern": "x", "reason": ""})
        self.assertEqual(self.server.state["deleted"], [rule["id"]])

    def test_the_blocklist_is_a_list_of_its_own(self):
        self.probe_rule({"pattern": "x"})
        self.created("rules", domain_id=1, list="allowlist", pattern="x")
        self.probe_rule({"pattern": "x"}, "/api/v1/admin/domains/1/blocklist")
        self.assertEqual(self.server.state["deleted"], [])

    def test_leaves_an_entry_it_did_not_add(self):
        self.probe_rule({"pattern": "@seeded.example"})
        self.assertEqual(self.server.state["deleted"], [])

    def test_leaves_malformed_requests_to_the_probe(self):
        for body in ({}, [], {"pattern": ""}, {"pattern": 7}):
            self.probe_rule(body)
        for path in ("/api/v1/admin/domains/0/allowlist", "/api/v1/admin/domains/x/allowlist",
                     "/api/v1/admin/domains/999/allowlist"):
            self.probe_rule({"pattern": "x"}, path)
        self.assertEqual(self.server.state["deleted"], [])

    def test_the_linked_entry_follows_a_probe_that_replaced_it(self):
        example = {"pattern": "@partner.example.com", "reason": "A trusted partner"}
        self.probe_rule(example)
        rule = self.created("rules", domain_id=1, list="allowlist", **example)
        hooks.keep_the_added_sender_rule(dict(self.transaction(self.PATH, example),
                                              real={"body": json.dumps({"data": rule})}))
        # A probe repeating the example's pattern removes it, then adds its own.
        self.probe_rule(example)
        replacement = self.created("rules", domain_id=1, list="allowlist", **example)

        transaction = self.transaction("%s/%d" % (self.PATH, rule["id"]))
        hooks.on_the_added_sender_rule(transaction)
        self.assertEqual(transaction["fullPath"], "%s/%d" % (self.PATH, replacement["id"]))

    def test_the_linked_entry_is_added_again_when_a_probe_left_none(self):
        example = {"pattern": "@partner.example.com"}
        rule = self.created("rules", domain_id=1, list="allowlist", **example)
        hooks.keep_the_added_sender_rule(dict(self.transaction(self.PATH, example),
                                              real={"body": json.dumps({"data": rule})}))
        self.server.state["rules"] = [r for r in self.server.state["rules"] if r["id"] != rule["id"]]

        transaction = self.transaction("%s/%d?x=1" % (self.PATH, rule["id"]))
        hooks.on_the_added_sender_rule(transaction)
        added = next(r for r in self.server.state["rules"] if r["pattern"] == example["pattern"])
        self.assertEqual(transaction["fullPath"], "%s/%d?x=1" % (self.PATH, added["id"]))

    def test_other_entry_ids_are_left_alone(self):
        for path in ("%s/1" % self.PATH, "%s/999" % self.PATH):
            transaction = self.transaction(path)
            hooks.on_the_added_sender_rule(transaction)
            self.assertEqual(transaction["fullPath"], path)


class AliasProbes(AdminAPITestCase):
    PATH = "/api/v1/admin/aliases"

    def probe_alias(self, body):
        self.probe(hooks.remove_a_previous_alias_probe, self.PATH, body)

    def test_hosts_the_domain_and_removes_only_its_own_repeated_probe(self):
        body = {"source_address": "someone@example.com", "destination_address": "someone@example.com"}
        self.probe_alias(body)
        domain = next(d for d in self.server.state["domains"] if d["name"] == "example.com")
        self.assertEqual(self.server.state["deleted"], [])
        alias = self.created("aliases", domain_id=domain["id"], **body)

        self.probe_alias(body)
        self.assertEqual(self.server.state["deleted"], [alias["id"]])
        self.assertEqual(len([d for d in self.server.state["domains"] if d["name"] == "example.com"]), 1)

    def test_leaves_an_alias_it_did_not_create(self):
        self.probe_alias({"source_address": "postmaster@restmail.test", "destination_address": "a@b.c"})
        self.assertEqual(self.server.state["deleted"], [])

    def test_leaves_malformed_and_unhostable_addresses_to_the_probe(self):
        for body in ({}, [], {"source_address": ""}, {"source_address": "not-an-email"},
                     {"source_address": "a@b"}, {"source_address": 7},
                     {"source_address": "someone@nowhere.invalid"}):
            self.probe_alias(body)
        self.assertEqual([d["name"] for d in self.server.state["domains"]], ["restmail.test"])
        self.assertEqual(self.server.state["deleted"], [])

    def test_the_linked_alias_follows_or_is_recreated(self):
        example = {"source_address": "sales@restmail.test", "destination_address": "admin@restmail.test"}
        self.probe_alias(example)
        alias = self.created("aliases", domain_id=1, **example)
        hooks.keep_the_created_alias(dict(self.transaction(self.PATH, example),
                                          real={"body": json.dumps({"data": alias})}))
        self.probe_alias(example)
        replacement = self.created("aliases", domain_id=1, **example)

        transaction = self.transaction("%s/%d" % (self.PATH, alias["id"]))
        hooks.on_the_created_alias(transaction)
        self.assertEqual(transaction["fullPath"], "%s/%d" % (self.PATH, replacement["id"]))

        self.server.state["aliases"] = [a for a in self.server.state["aliases"] if a["id"] != replacement["id"]]
        transaction = self.transaction("%s/%d" % (self.PATH, alias["id"]))
        hooks.on_the_created_alias(transaction)
        recreated = next(a for a in self.server.state["aliases"] if a["source_address"] == example["source_address"])
        self.assertEqual(transaction["fullPath"], "%s/%d" % (self.PATH, recreated["id"]))

    def test_other_alias_ids_are_left_alone(self):
        transaction = self.transaction("%s/3" % self.PATH)
        hooks.on_the_created_alias(transaction)
        self.assertEqual(transaction["fullPath"], "%s/3" % self.PATH)


if __name__ == "__main__":
    unittest.main()
