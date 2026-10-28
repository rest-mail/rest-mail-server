"""Hooks that give named examples sharing one request the state each needs:
an array body left alone, invitations, blocked senders, bans and drafts."""

import base64
import importlib.util
import json
import sys
import threading
import types
import unittest
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.before = stub.after = lambda name: lambda fn: fn
    stub.get_json = lambda transaction, which="request": json.loads(
        transaction[which]["body"]
    )

    def set_json(transaction, value):
        transaction["request"]["body"] = json.dumps(value)

    stub.set_json = set_json
    spec = importlib.util.spec_from_file_location(
        "contract_example_state_hooks", ROOT / "tests/contract/hooks.py"
    )
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, stub


class FixtureAPI(BaseHTTPRequestHandler):
    """The admin endpoints the hooks call, recording each call."""

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
        return json.loads(
            self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"null"
        )

    def do_GET(self):
        state = self.server.state
        state["calls"].append("GET " + self.path)
        url = urllib.parse.urlsplit(self.path)
        if url.path == "/api/v1/accounts/1/contacts":
            return self.answer(200, {"data": state["contacts"]})
        if url.path == "/api/v1/admin/domains":
            return self.answer(200, {"data": state["domains"]})
        if url.path == "/api/v1/accounts/0/contacts":
            return self.answer(422, {"error": {"code": "validation_failed"}})
        if url.path == "/api/v1/admin/queue":
            query = urllib.parse.parse_qs(url.query)
            items = state["queue"].get(query.get("recipient", [""])[0], [])
            return self.answer(200, {"data": {"items": [{"id": i} for i in items]}})
        self.answer(404)

    def do_POST(self):
        state = self.server.state
        state["calls"].append("POST " + self.path)
        body = self.read_body()
        if self.path == "/api/v1/auth/login":
            return self.answer(200, {"data": {"access_token": "setup"}})
        if self.path == "/api/v1/accounts":
            return self.answer(201, {"data": {"id": 77}})
        if self.path == "/api/v1/admin/mailboxes/77/invitation":
            return self.answer(201, {"data": {"token": "fresh-invitation"}})
        if self.path == "/api/v1/messages/draft":
            state["drafts"].append(body)
            return self.answer(201, {"data": {"id": 88}})
        if self.path == "/api/v1/accounts/0/contacts" or body.get("ip") == "not-an-ip":
            return self.answer(422, {"error": {"code": "validation_failed"}})
        if self.path == "/api/v1/admin/domains":
            state["created"].append(body)
            return self.answer(201, {"data": {"id": 91, "name": body["name"]}})
        if self.path in ("/api/v1/accounts/1/contacts", "/api/v1/admin/bans"):
            state["created"].append(body)
            return self.answer(201, {"data": body})
        self.answer(404)

    def do_DELETE(self):
        state = self.server.state
        state["calls"].append("DELETE " + self.path)
        if self.path in (
            "/api/v1/accounts/1/contacts/5",
            "/api/v1/admin/domains/7",
            "/api/v1/accounts/4/sieve",
        ):
            return self.answer(204)
        self.answer(404, {"error": {"code": "not_found"}})


class ExampleStateHookTest(unittest.TestCase):
    def setUp(self):
        self.hooks, self.stub = load_hooks()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.state = {
            "calls": [],
            "contacts": [],
            "drafts": [],
            "created": [],
            "queue": {},
            "domains": [],
        }
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.hooks.pace_auth_requests = lambda transaction: None

    def transaction(self, name, path, body=None):
        return {
            "name": name,
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": self.server.server_address[1],
            "fullPath": path,
            "request": {"body": json.dumps(body) if body is not None else ""},
        }

    def test_an_array_body_has_no_password_to_confirm(self):
        transaction = self.transaction(
            "/api/v1/accounts/{id}/credentials > Make an app password > 400 > application/json",
            "/api/v1/accounts/1/credentials",
            [],
        )
        self.hooks.confirm_with_the_admin_password(transaction)
        self.assertEqual(transaction["request"]["body"], "[]")

    def test_each_invitation_example_gets_an_account_in_its_state(self):
        name = "/api/v1/admin/mailboxes/{id}/invitation > Invite an account to choose its first password > %s > application/json"
        paths = {}
        for status in ("201", "409", "404"):
            transaction = self.transaction(
                name % status, "/api/v1/admin/mailboxes/12/invitation"
            )
            self.hooks.invite_an_account_without_credentials(transaction)
            paths[status] = transaction["fullPath"]
        self.assertEqual(
            paths,
            {
                "201": "/api/v1/admin/mailboxes/77/invitation",
                "409": "/api/v1/admin/mailboxes/1/invitation",
                "404": "/api/v1/admin/mailboxes/2147483647/invitation",
            },
        )

    def test_a_new_sender_is_unblocked_first_and_an_existing_one_added(self):
        name = "/api/v1/accounts/{id}/contacts/block > Block a sender > %s > application/json"
        self.server.state["contacts"] = [{"id": 5, "email": "new@example.com"}]
        self.hooks.block_a_new_and_an_existing_sender(
            self.transaction(
                name % "201",
                "/api/v1/accounts/1/contacts/block",
                {"email": "new@example.com"},
            )
        )
        self.hooks.block_a_new_and_an_existing_sender(
            self.transaction(
                name % "200",
                "/api/v1/accounts/1/contacts/block",
                {"email": "existing@example.com"},
            )
        )
        self.assertIn(
            "DELETE /api/v1/accounts/1/contacts/5", self.server.state["calls"]
        )
        self.assertEqual(
            self.server.state["created"], [{"email": "existing@example.com"}]
        )

    def test_a_new_ban_is_cleared_first_and_a_replaced_one_made(self):
        name = "/api/v1/admin/bans > Create an IP ban > %s > application/json"
        self.hooks.ban_a_new_and_an_existing_ip(
            self.transaction(name % "201", "/api/v1/admin/bans", {"ip": "192.0.2.10"})
        )
        self.hooks.ban_a_new_and_an_existing_ip(
            self.transaction(name % "200", "/api/v1/admin/bans", {"ip": "192.0.2.20"})
        )
        self.assertIn(
            "DELETE /api/v1/admin/bans/ip/192.0.2.10", self.server.state["calls"]
        )
        self.assertEqual(
            [ban["ip"] for ban in self.server.state["created"]], ["192.0.2.20"]
        )

    def test_a_sent_draft_is_one_of_its_own_and_a_missing_one_is_absent(self):
        name = "/api/v1/messages/draft/{id}/send > Send a draft > %s > application/json"
        sent = self.transaction(name % "201", "/api/v1/messages/draft/12/send")
        missing = self.transaction(name % "404", "/api/v1/messages/draft/12/send")
        self.hooks.send_a_draft_of_its_own(sent)
        self.hooks.send_a_draft_of_its_own(missing)
        self.assertEqual(sent["fullPath"], "/api/v1/messages/draft/88/send")
        self.assertEqual(missing["fullPath"], "/api/v1/messages/draft/2147483647/send")
        self.assertEqual(len(self.server.state["drafts"]), 1)

    def test_a_sent_draft_with_a_linked_id_is_still_one_of_its_own(self):
        # named-values illustrate fills a success's id from the saveDraft link;
        # sending that draft would leave the next exchange nothing to send.
        name = (
            "/api/v1/messages/draft/{id}/send > Send a draft > 201 > application/json"
        )
        sent = self.transaction(name, "/api/v1/messages/draft/36/send")
        self.hooks.send_a_draft_of_its_own(sent)
        self.assertEqual(sent["fullPath"], "/api/v1/messages/draft/88/send")

    def test_a_deleted_domain_is_one_of_its_own(self):
        # The shared created domain outlives this delete, so the deletes of
        # what hangs off it (DKIM, sender rules, pipelines) still find it.
        name = "/api/v1/admin/domains/{id} > Delete a domain > 204"
        deleted = self.transaction(name, "/api/v1/admin/domains/18")
        self.stub.stash["domain_id"] = 18
        self.hooks.delete_only_the_created_domain(deleted)
        self.assertEqual(deleted["fullPath"], "/api/v1/admin/domains/91")
        self.assertNotIn("skip", deleted)
        self.assertEqual(self.stub.stash["domain_id"], 18)

    def test_a_bounce_takes_its_own_held_entry(self):
        # The retry example needs the retry entry still deferred.
        bounce = self.hooks.FIXTURE_PLAN["queue"]["bounce"]["recipient"]
        self.server.state["queue"] = {bounce: [55, 54]}
        name = "/api/v1/admin/queue/{id}/bounce > Bounce a single queue entry > 200 > application/json"
        transaction = self.transaction(name, "/api/v1/admin/queue/8/bounce")
        self.hooks.bounce_the_held_bounce_entry(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/queue/55/bounce")

    def test_removing_an_ips_bans_has_one_to_remove(self):
        name = "/api/v1/admin/bans/ip/{ip} > Remove all bans for a specific IP > 204"
        self.hooks.unban_an_ip_that_is_banned(
            self.transaction(name, "/api/v1/admin/bans/ip/192.0.2.10")
        )
        self.assertEqual(
            [ban["ip"] for ban in self.server.state["created"]], ["192.0.2.10"]
        )

    def test_coverage_probes_reach_the_handler_unchanged(self):
        # Probes reach hooks under the documented name; their own ids and
        # values mark them, and a setup refusal they provoke is not an error.
        bounce = "/api/v1/admin/queue/{id}/bounce > Bounce a single queue entry > 200 > application/json"
        for path in (
            "/api/v1/admin/queue/0/bounce",
            "/api/v1/admin/queue/vertrag-not-a-number/bounce",
        ):
            transaction = self.transaction(bounce, path)
            self.hooks.bounce_the_held_bounce_entry(transaction)
            self.assertEqual(transaction["fullPath"], path)
        sent = (
            "/api/v1/messages/draft/{id}/send > Send a draft > 201 > application/json"
        )
        probe = self.transaction(sent, "/api/v1/messages/draft/0/send")
        self.hooks.send_a_draft_of_its_own(probe)
        self.assertEqual(probe["fullPath"], "/api/v1/messages/draft/0/send")
        self.assertEqual(self.server.state["drafts"], [])
        block = "/api/v1/accounts/{id}/contacts/block > Block a sender > 200 > application/json"
        self.hooks.block_a_new_and_an_existing_sender(
            self.transaction(
                block,
                "/api/v1/accounts/0/contacts/block",
                {"email": "existing@example.com"},
            )
        )
        unban = "/api/v1/admin/bans/ip/{ip} > Remove all bans for a specific IP > 204"
        self.hooks.unban_an_ip_that_is_banned(
            self.transaction(unban, "/api/v1/admin/bans/ip/not-an-ip")
        )

    def test_a_repeated_create_finds_nothing_left_over(self):
        # The coverage process repeats the documented creates the examples
        # process already made.
        self.server.state["domains"] = [
            {"id": 7, "name": "example.org"},
            {"id": 8, "name": "other.test"},
        ]
        domain = "/api/v1/admin/domains > Create a new domain > 201 > application/json"
        self.hooks.create_a_domain_that_is_not_there(
            self.transaction(
                domain,
                "/api/v1/admin/domains",
                {"name": "example.org", "server_type": "restmail"},
            )
        )
        sieve = "/api/v1/accounts/{id}/sieve > Create or replace the Sieve filter script > 201 > application/json"
        self.hooks.install_a_sieve_script_where_there_is_none(
            self.transaction(sieve, "/api/v1/accounts/4/sieve", {"script": "keep;"})
        )
        self.hooks.install_a_sieve_script_where_there_is_none(
            self.transaction(sieve, "/api/v1/accounts/0/sieve", {"script": "keep;"})
        )
        deletes = [
            call for call in self.server.state["calls"] if call.startswith("DELETE")
        ]
        self.assertEqual(
            deletes,
            ["DELETE /api/v1/admin/domains/7", "DELETE /api/v1/accounts/4/sieve"],
        )

    def test_probe_values_no_schema_can_make_valid_are_replaced(self):
        # A probe's Sieve script and raw message are generator strings; it
        # sends usable ones and keeps the rest of its body.
        sieve = "/api/v1/accounts/{id}/sieve > Create or replace the Sieve filter script > 201 > application/json"
        probe = self.transaction(
            sieve, "/api/v1/accounts/4/sieve", {"script": "x", "active": True}
        )
        self.hooks.install_a_sieve_script_where_there_is_none(probe)
        self.assertEqual(
            json.loads(probe["request"]["body"]), {"script": "keep;", "active": True}
        )

        imported = "/api/v1/accounts/{id}/messages/import > Import one historical message > 200 > application/json"
        probe = self.transaction(
            imported,
            "/api/v1/accounts/4/messages/import",
            {
                "raw_message": "dmVydHJhZw==",
                "internal_date": "2020-01-02T03:04:05Z",
                "folder": "Archive",
            },
        )
        self.hooks.import_a_message_a_parser_can_read(probe)
        body = json.loads(probe["request"]["body"])
        raw = base64.b64decode(body["raw_message"])
        self.assertIn(b"\r\n\r\n", raw)
        self.assertIn(b"Message-ID: <contract-import-", raw)
        self.assertEqual(body["folder"], "Archive")

        documented = base64.b64encode(b"Subject: kept\r\n\r\nbody\r\n").decode()
        probe = self.transaction(
            imported, "/api/v1/accounts/4/messages/import", {"raw_message": documented}
        )
        self.hooks.import_a_message_a_parser_can_read(probe)
        self.assertEqual(
            json.loads(probe["request"]["body"])["raw_message"], documented
        )

    def test_an_invitation_probe_gets_a_token_and_a_password_it_can_redeem(self):
        # The token is optional in the schema and the 12-byte password rule is
        # the handler's, so a probe of the 200 is given both.
        accept = "/api/v1/auth/invitation > Choose your first password with an invitation > 200 > application/json"
        probe = self.transaction(
            accept, "/api/v1/auth/invitation", {"password": "x", "client": "native"}
        )
        self.hooks.redeem_a_fresh_invitation(probe)
        self.assertEqual(
            json.loads(probe["request"]["body"]),
            {
                "password": "the recipient chooses this password",
                "client": "native",
                "token": "fresh-invitation",
            },
        )


if __name__ == "__main__":
    unittest.main()
