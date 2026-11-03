"""The sender rule and alias hooks let each repeated probe create its entry
afresh, without touching the request, and keep the linked entry alive.

The domain stays for the whole run now that deleteDomain refuses one with
accounts (#388), so nothing clears what an earlier probe made.
"""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import sys
import threading
import types
import unittest
from unittest import mock
import urllib.parse
import urllib.request


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
        match = ALIAS.match(url.path)
        if match:
            alias = next((a for a in state["aliases"] if a["id"] == int(match[1])), None)
            return self.answer(200, {"data": alias}) if alias else self.answer(404)
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

    def removal(self, path, status=204):
        transaction = self.transaction(path)
        transaction["name"] = "%s/{eid} > Remove entry > %d" % (self.PATH, status)
        transaction["request"]["headers"] = {"Authorization": "Bearer original"}
        return transaction

    def test_an_entry_that_is_there_is_followed_unchanged(self):
        for path in (self.PATH + "/2", self.PATH + "/2?x=1"):
            transaction = self.removal(path)
            before = json.loads(json.dumps(transaction))
            hooks.on_a_sender_rule_of_its_own(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.server.state["serial"], 100)

    def test_an_entry_that_is_gone_gets_one_of_its_own(self):
        # The add's linked entry, removed by a probe repeating its pattern (which
        # added its own), by the documented remove, or never made (coverage's
        # placeholder 1). Each kind of list gets its own entry, made once.
        for kind in ("allowlist", "blocklist"):
            parent = "/api/v1/admin/domains/1/" + kind
            for entry_id in ("1", "101", "999"):
                with self.subTest(kind=kind, entry_id=entry_id):
                    transaction = self.removal(parent + "/" + entry_id + "?keep=yes")
                    before = json.loads(json.dumps(transaction))
                    hooks.on_a_sender_rule_of_its_own(transaction)
                    own = [r for r in self.server.state["rules"]
                           if r["list"] == kind and r["pattern"] == hooks.OWN_SENDER_RULE["pattern"]]
                    self.assertEqual(len(own), 1)
                    before["fullPath"] = parent + "/%d?keep=yes" % own[0]["id"]
                    self.assertEqual(transaction, before)
        self.assertEqual(self.server.state["deleted"], [])

    def test_refusals_and_other_targets_keep_their_ids(self):
        for path, status in (
            (self.PATH + "/1", 404),
            (self.PATH + "/0", 204),
            (self.PATH + "/x", 204),
            (self.PATH + "/2147483647", 204),
            ("/api/v1/admin/domains/0/allowlist/1", 204),
            ("/api/v1/admin/domains/x/allowlist/1", 204),
            ("/api/v1/admin/domains/999/allowlist/1", 204),
        ):
            with self.subTest(path=path, status=status):
                transaction = self.removal(path, status)
                before = json.loads(json.dumps(transaction))
                hooks.on_a_sender_rule_of_its_own(transaction)
                self.assertEqual(transaction, before)
        self.assertEqual(self.server.state["serial"], 100)

    def test_nothing_is_kept_between_exchanges(self):
        example = {"pattern": "@partner.example.com"}
        self.probe_rule(example)
        self.created("rules", domain_id=1, list="allowlist", **example)
        hooks.on_a_sender_rule_of_its_own(self.removal(self.PATH + "/1"))
        self.assertEqual(set(vertrag_hooks.stash) - {"setup_admin_tokens"}, {"created_sender_rule_probes"})


class SenderRuleCleanStart(AdminAPITestCase):
    """With CONTRACT_RESET_SEED=1 (the own-data phase), each sender rule
    exchange resets first and is sent to a domain of its own; a remove gets an
    entry of its own there (#1385)."""

    LINKED = "/api/v1/admin/domains/7/"

    def setUp(self):
        super().setUp()
        self.enterContext(mock.patch.dict(os.environ, {"CONTRACT_RESET_SEED": "1"}))
        self.start_clean = self.enterContext(mock.patch.object(hooks, "start_clean"))

    def sent(self, hook, path, name, body=None):
        transaction = self.transaction(path, body)
        transaction["name"] = name
        hook(transaction)
        self.start_clean.assert_called_once_with(transaction)
        self.start_clean.reset_mock()
        return transaction

    def own_domain(self):
        return next(d for d in self.server.state["domains"] if d["name"] == hooks.OWN_DOMAIN)

    def forget(self, domain):
        # The reset (mocked here) removes what the exchange made.
        self.server.state["domains"].remove(domain)
        self.server.state["rules"] = [r for r in self.server.state["rules"] if r["domain_id"] != domain["id"]]

    def test_a_list_reads_a_domain_of_its_own(self):
        for kind in ("allowlist", "blocklist"):
            with self.subTest(kind=kind):
                transaction = self.sent(hooks.list_sender_rules_from_a_clean_start,
                                        self.LINKED + kind + "?limit=5", "x > 200")
                domain = self.own_domain()
                self.assertEqual(transaction["fullPath"],
                                 "/api/v1/admin/domains/%d/%s?limit=5" % (domain["id"], kind))
                self.forget(domain)

    def test_an_add_is_sent_as_documented_to_a_domain_of_its_own(self):
        for kind in ("allowlist", "blocklist"):
            for body, name in (({"pattern": "@partner.example.com", "reason": "A partner"}, "x > 201"),
                               ({"reason": "No pattern"}, "x > 422")):
                with self.subTest(kind=kind, name=name):
                    transaction = self.sent(hooks.remove_a_previous_sender_rule_probe,
                                            self.LINKED + kind, name, body)
                    domain = self.own_domain()
                    self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/%d/%s" % (domain["id"], kind))
                    self.assertEqual(json.loads(transaction["request"]["body"]), body)
                    self.assertEqual(self.server.state["deleted"], [])
                    self.forget(domain)

    def test_a_remove_reaches_an_entry_of_its_own_on_a_domain_of_its_own(self):
        for kind in ("allowlist", "blocklist"):
            with self.subTest(kind=kind):
                transaction = self.sent(hooks.on_a_sender_rule_of_its_own,
                                        self.LINKED + kind + "/2?keep=yes", "x > 204")
                domain = self.own_domain()
                own = self.server.state["rules"][-1]
                self.assertEqual((own["domain_id"], own["list"]), (domain["id"], kind))
                self.assertEqual(own["pattern"], hooks.OWN_SENDER_RULE["pattern"])
                self.assertEqual(transaction["fullPath"],
                                 "/api/v1/admin/domains/%d/%s/%d?keep=yes" % (domain["id"], kind, own["id"]))
                self.forget(domain)

    def test_refusals_reset_and_keep_their_ids(self):
        # The entry is refused on a domain that exists; missing's domain and a
        # malformed domain id stay as they are.
        for path, name in ((self.LINKED + "allowlist/2147483647", "x > 404"),
                           (self.LINKED + "blocklist/2", "x > 404"),
                           (self.LINKED + "allowlist/0", "x > 422")):
            with self.subTest(path=path):
                transaction = self.sent(hooks.on_a_sender_rule_of_its_own, path, name)
                domain = self.own_domain()
                self.assertEqual(transaction["fullPath"],
                                 path.replace("/7/", "/%d/" % domain["id"]))
                self.forget(domain)
        for path in ("/api/v1/admin/domains/2147483647/allowlist/1", "/api/v1/admin/domains/0/allowlist/1",
                     "/api/v1/admin/domains/x/blocklist"):
            with self.subTest(path=path):
                hook = hooks.on_a_sender_rule_of_its_own if path.endswith("/1") else hooks.list_sender_rules_from_a_clean_start
                transaction = self.sent(hook, path, "x > 404")
                self.assertEqual(transaction["fullPath"], path)
        self.assertEqual(len(self.server.state["rules"]), 1)

    def test_without_a_reset_none_is_asked_for(self):
        os.environ.pop("CONTRACT_RESET_SEED")
        hooks.list_sender_rules_from_a_clean_start(self.transaction("/api/v1/admin/domains/1/allowlist"))
        hooks.remove_a_previous_sender_rule_probe(self.transaction("/api/v1/admin/domains/1/allowlist", []))
        transaction = self.transaction("/api/v1/admin/domains/1/allowlist/2")
        transaction["name"] = "x > 204"
        hooks.on_a_sender_rule_of_its_own(transaction)
        self.start_clean.assert_not_called()
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/1/allowlist/2")


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

    def create_over_the_seeded_alias(self, name, leftovers):
        """Create the alias the fixture already holds, in a run that does or
        does not clear leftovers."""
        body = {"source_address": "postmaster@restmail.test", "destination_address": "a@b.example"}
        transaction = self.transaction(self.PATH, body)
        transaction["name"] = name
        with mock.patch.dict(os.environ):
            os.environ.pop("CONTRACT_REMOVE_LEFTOVERS", None)
            if leftovers:
                os.environ["CONTRACT_REMOVE_LEFTOVERS"] = "1"
            hooks.remove_a_previous_alias_probe(transaction)

    def test_an_ownership_run_clears_a_leftover_for_a_documented_create(self):
        self.create_over_the_seeded_alias(self.PATH + " > Create an alias > 201 > application/json", True)
        self.assertEqual(self.server.state["deleted"], [3])

    def test_an_ownership_run_leaves_the_object_a_conflict_example_names(self):
        self.create_over_the_seeded_alias(self.PATH + " > Create an alias > 409 > application/json", True)
        self.assertEqual(self.server.state["deleted"], [])

    def test_other_runs_leave_a_leftover_alone(self):
        self.create_over_the_seeded_alias(self.PATH + " > Create an alias > 201 > application/json", False)
        self.assertEqual(self.server.state["deleted"], [])

    def test_only_the_ownership_phase_turns_on_clearing_leftovers(self):
        script = (Path(__file__).resolve().parents[2] / "tests/contract/run.sh").read_text()
        self.assertEqual(script.count("export CONTRACT_REMOVE_LEFTOVERS=1"), 1)
        ownership = script.index("\nownership)\n")
        next_phase = re.compile(r"^[a-z|* ]+\)$", re.M).search(script, ownership + len("\nownership)\n"))
        self.assertLess(script.index("export CONTRACT_REMOVE_LEFTOVERS=1"), next_phase.start())
        self.assertGreater(script.index("export CONTRACT_REMOVE_LEFTOVERS=1"), ownership)

    def test_leaves_malformed_and_unhostable_addresses_to_the_probe(self):
        for body in ({}, [], {"source_address": ""}, {"source_address": "not-an-email"},
                     {"source_address": "a@b"}, {"source_address": 7},
                     {"source_address": "someone@nowhere.invalid"}):
            self.probe_alias(body)
        self.assertEqual([d["name"] for d in self.server.state["domains"]], ["restmail.test"])
        self.assertEqual(self.server.state["deleted"], [])

    OWN = "contract-own-alias@restmail.test"

    def aliased(self, path, name="x > 200"):
        transaction = self.transaction(path)
        transaction["name"] = name
        hooks.on_an_alias_of_its_own(transaction)
        return transaction["fullPath"]

    def test_a_linked_alias_that_exists_is_followed(self):
        alias = self.created("aliases", domain_id=1, source_address="sales@restmail.test")
        self.assertEqual(self.aliased("%s/%d" % (self.PATH, alias["id"])), "%s/%d" % (self.PATH, alias["id"]))
        self.assertEqual(self.server.state["serial"], alias["id"])

    def test_a_removed_alias_is_replaced_by_one_of_its_own_and_reused(self):
        first = self.aliased(self.PATH + "/101?keep=yes")
        own = next(a for a in self.server.state["aliases"] if a["source_address"] == self.OWN)
        self.assertEqual(first, "%s/%d?keep=yes" % (self.PATH, own["id"]))
        self.assertEqual(own["destination_address"], hooks.ADMIN)
        # The next exchange finds it at its source and makes no other.
        self.assertEqual(self.aliased(self.PATH + "/102"), "%s/%d" % (self.PATH, own["id"]))
        self.assertEqual(len([a for a in self.server.state["aliases"] if a["source_address"] == self.OWN]), 1)
        self.assertNotIn("created_alias", vertrag_hooks.stash)

    def test_other_alias_ids_are_left_alone(self):
        self.assertEqual(self.aliased("%s/3" % self.PATH), "%s/3" % self.PATH)

    def test_coverage_placeholder_gets_an_alias_that_exists(self):
        self.enterContext(mock.patch.dict(os.environ, {"CONTRACT_COVERAGE": "1"}))
        for method in ("GET", "PATCH", "DELETE"):
            with self.subTest(method=method):
                transaction = self.transaction(self.PATH + "/1?keep=yes")
                transaction["request"]["method"] = method
                transaction["name"] = self.PATH + "/{id} > Alias > 200"
                hooks.on_an_alias_of_its_own(transaction)
                own = next(a for a in self.server.state["aliases"] if a["source_address"] == self.OWN)
                self.assertEqual(transaction["fullPath"], self.PATH + "/%d?keep=yes" % own["id"])
                self.server.state["aliases"] = []

    def test_refusals_keep_their_ids_and_bodies(self):
        for target, name in (("0", "x > 422"), ("-1", "x > 422"), ("x", "x > 422"),
                             ("2147483647", "x > 404"), ("3", "x > 200"), ("55", "x > 404")):
            with self.subTest(target=target):
                transaction = self.transaction(self.PATH + "/" + target, {"active": "not-a-boolean"})
                transaction["name"] = name
                before = json.loads(json.dumps(transaction))
                hooks.on_an_alias_of_its_own(transaction)
                self.assertEqual(transaction, before)
        self.assertEqual(self.server.state["serial"], 100)


class AliasCleanStart(AdminAPITestCase):
    """With CONTRACT_RESET_SEED=1 (the own-data phase), each alias exchange
    resets first, and a read, update or delete gets an alias on a domain of
    its own (#1385)."""

    PATH = "/api/v1/admin/aliases"

    def setUp(self):
        super().setUp()
        self.enterContext(mock.patch.dict(os.environ, {"CONTRACT_RESET_SEED": "1"}))
        self.start_clean = self.enterContext(mock.patch.object(hooks, "start_clean"))

    def sent(self, hook, path, name, body=None):
        transaction = self.transaction(path, body)
        transaction["name"] = name
        hook(transaction)
        self.start_clean.assert_called_once_with(transaction)
        self.start_clean.reset_mock()
        return transaction

    def test_get_update_and_delete_reach_an_alias_on_a_domain_of_their_own(self):
        for name in ("x > 200", "x > 204", "x > 422"):
            with self.subTest(name=name):
                transaction = self.sent(hooks.on_an_alias_of_its_own, self.PATH + "/3", name)
                own = self.server.state["aliases"][-1]
                domain = next(d for d in self.server.state["domains"] if d["id"] == own["domain_id"])
                self.assertEqual(domain["name"], hooks.OWN_DOMAIN)
                self.assertEqual(own["source_address"], "contract-own-alias@" + hooks.OWN_DOMAIN)
                self.assertEqual(transaction["fullPath"], "%s/%d" % (self.PATH, own["id"]))
                # The reset (mocked here) removes what it made.
                self.server.state["domains"].remove(domain)
                self.server.state["aliases"].remove(own)

    def test_refusals_reset_and_keep_their_ids(self):
        for target, name in (("2147483647", "x > 404"), ("0", "x > 422"), ("abc", "x > 422")):
            with self.subTest(target=target):
                transaction = self.sent(hooks.on_an_alias_of_its_own, self.PATH + "/" + target, name)
                self.assertEqual(transaction["fullPath"], self.PATH + "/" + target)
        self.assertEqual(self.server.state["serial"], 100)

    def test_a_create_resets_and_is_sent_as_documented(self):
        for body in ({"source_address": "sales@restmail.test", "destination_address": "admin@restmail.test"}, []):
            with self.subTest(body=body):
                transaction = self.sent(hooks.remove_a_previous_alias_probe, self.PATH, "x > 201", body)
                self.assertEqual(json.loads(transaction["request"]["body"]), body)
                self.assertEqual(transaction["fullPath"], self.PATH)

    def test_without_a_reset_none_is_asked_for(self):
        os.environ.pop("CONTRACT_RESET_SEED")
        transaction = self.transaction(self.PATH + "/3")
        transaction["name"] = "x > 200"
        hooks.on_an_alias_of_its_own(transaction)
        hooks.remove_a_previous_alias_probe(self.transaction(self.PATH, []))
        self.start_clean.assert_not_called()

if __name__ == "__main__":
    unittest.main()
