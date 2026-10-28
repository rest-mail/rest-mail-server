"""The createAccount hook hosts the coverage probes' domain and lets each
repeated probe create its account afresh, without touching the request."""

import json
import sys
import threading
import types
import unittest
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import ClassVar
from unittest.mock import patch


def stub_vertrag_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    module = types.ModuleType("vertrag_hooks")
    module.stash = {}
    module.before = module.after = lambda name: lambda fn: fn
    module.get_json = lambda transaction, which="request": json.loads(
        transaction[which]["body"]
    )
    module.set_json = lambda transaction, body: transaction["request"].__setitem__(
        "body", json.dumps(body)
    )
    sys.modules["vertrag_hooks"] = module
    return module


vertrag_hooks = stub_vertrag_hooks()
import hooks


class SharedFixturePlan(unittest.TestCase):
    def test_preview_pacing_preserves_the_request_and_expected_response(self):
        transaction = {
            "fullPath": "/api/v1/admin/pipelines/test-filter",
            "request": {"body": "[]", "headers": {"Authorization": "Bearer original"}},
            "expected": {"statusCode": "400"},
        }
        before = json.dumps(transaction, sort_keys=True)
        with patch.object(hooks.time, "sleep") as sleep:
            hooks.pace_pipeline_previews(transaction)
        sleep.assert_called_once_with(0.55)
        self.assertEqual(json.dumps(transaction, sort_keys=True), before)

    def test_queue_hook_uses_the_named_declaration_and_preserves_credentials(self):
        transaction = {
            "fullPath": "/api/v1/admin/queue?status=pending",
            "request": {"headers": {"Authorization": "Bearer original"}},
        }
        with patch.dict(
            hooks.FIXTURE_PLAN["queue"],
            {"retry": {"recipient": "alternate@retry.invalid"}},
        ):
            hooks.list_the_sent_messages_queue_entry(transaction)
        parsed = urllib.parse.urlparse(transaction["fullPath"])
        self.assertEqual(
            urllib.parse.parse_qs(parsed.query),
            {"recipient": ["alternate@retry.invalid"]},
        )
        self.assertEqual(
            transaction["request"]["headers"], {"Authorization": "Bearer original"}
        )

    def test_queue_hook_recognises_the_pending_example_with_its_defaults(self):
        # Vertrag 0.31.1 sends the declared limit and offset defaults too.
        transaction = {
            "fullPath": "/api/v1/admin/queue?status=pending&limit=50&offset=0",
            "request": {"headers": {}},
        }
        hooks.list_the_sent_messages_queue_entry(transaction)
        parsed = urllib.parse.urlparse(transaction["fullPath"])
        self.assertEqual(parsed.path, "/api/v1/admin/queue")
        self.assertEqual(
            urllib.parse.parse_qs(parsed.query),
            {"recipient": [hooks.FIXTURE_PLAN["queue"]["retry"]["recipient"]]},
        )

    def test_queue_hook_recognises_the_pending_example_with_its_own_limit(self):
        # The available example pages by 20, not the declared default.
        transaction = {
            "fullPath": "/api/v1/admin/queue?status=pending&limit=20",
            "request": {"headers": {}},
        }
        hooks.list_the_sent_messages_queue_entry(transaction)
        parsed = urllib.parse.urlparse(transaction["fullPath"])
        self.assertEqual(
            urllib.parse.parse_qs(parsed.query),
            {"recipient": [hooks.FIXTURE_PLAN["queue"]["retry"]["recipient"]]},
        )

    def test_invalid_and_unrelated_queue_probes_remain_unchanged(self):
        for path in (
            "/api/v1/admin/queue?status=invalid",
            "/api/v1/admin/queue?status=deferred",
            "/api/v1/admin/queue",
            "/api/v1/admin/queue?status=pending&limit=0",
            "/api/v1/admin/queue?status=pending&limit=50&offset=-1",
            "/api/v1/admin/queue?status=pending&domain=x",
        ):
            transaction = {"fullPath": path}
            hooks.list_the_sent_messages_queue_entry(transaction)
            self.assertEqual(transaction, {"fullPath": path})


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
        return json.loads(
            self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"null"
        )

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
        if self.path == "/api/v1/accounts":
            body = self.read_body()
            address = body["address"]
            if any(account["address"] == address for account in state["accounts"]):
                return self.answer(409)
            domain = next(
                d for d in state["domains"] if d["name"] == address.split("@")[1]
            )
            account = {
                "id": 100 + len(state["accounts"]) + len(state["deleted"]),
                "domain_id": domain["id"],
                **body,
            }
            state["accounts"].append(account)
            return self.answer(201, {"data": account})
        if self.path == "/api/v1/accounts/link":
            body = self.read_body()
            account = next(
                (
                    account
                    for account in state["accounts"]
                    if account["address"] == body.get("address")
                ),
                None,
            )
            if (
                not account
                or not body.get("password")
                or account.get("password") != body["password"]
            ):
                return self.answer(403)
            if account["id"] in state["linked"]:
                return self.answer(409)
            state["linked"].add(account["id"])
            return self.answer(201, {"data": account})
        self.answer(404)

    def do_GET(self):
        state = self.server.state
        url = urllib.parse.urlparse(self.path)
        if url.path == "/api/v1/admin/domains":
            return self.answer(200, {"data": state["domains"]})
        if url.path == "/api/v1/admin/mailboxes":
            domain_id = urllib.parse.parse_qs(url.query).get("domain_id", [None])[0]
            return self.answer(
                200,
                {
                    "data": [
                        a
                        for a in state["accounts"]
                        if domain_id is None or a["domain_id"] == int(domain_id)
                    ]
                },
            )
        self.answer(404)

    def do_DELETE(self):
        state = self.server.state
        account_id = int(self.path.rsplit("/", 1)[1])
        if self.path.startswith("/api/v1/accounts/"):
            if account_id not in state["linked"]:
                return self.answer(404)
            state["linked"].remove(account_id)
            return self.answer(204)
        state["deleted"].append(account_id)
        state["accounts"] = [a for a in state["accounts"] if a["id"] != account_id]
        self.answer(204)


class AdminAPITestCase(unittest.TestCase):
    def setUp(self):
        vertrag_hooks.stash.clear()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.state = {
            "domains": [{"id": 1, "name": "restmail.test"}],
            "accounts": [{"id": 1, "domain_id": 1, "address": "admin@restmail.test"}],
            "deleted": [],
            "linked": set(),
        }
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def probe(self, body):
        transaction = {
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": self.server.server_port,
            "request": {"body": json.dumps(body)},
        }
        before = transaction["request"]["body"]
        hooks.host_the_account_probe_domain(transaction)
        self.assertEqual(
            transaction["request"]["body"], before, "the hook rewrote the request"
        )

    def create(self, address):
        """What a successful probe leaves behind."""
        state = self.server.state
        domain = next(d for d in state["domains"] if d["name"] == address.split("@")[1])
        state["accounts"].append(
            {
                "id": 100 + len(state["accounts"]) + len(state["deleted"]),
                "domain_id": domain["id"],
                "address": address,
            }
        )


class CreateAccountProbes(AdminAPITestCase):
    def test_hosts_the_domain_and_removes_only_its_own_repeated_probe(self):
        self.probe({"address": "someone@example.com"})
        self.assertIn("example.com", [d["name"] for d in self.server.state["domains"]])
        self.assertEqual(self.server.state["deleted"], [])
        self.create("someone@example.com")

        self.probe({"address": "someone@example.com", "display_name": ""})
        self.assertEqual(len(self.server.state["deleted"]), 1)
        self.assertEqual(
            len(
                [d for d in self.server.state["domains"] if d["name"] == "example.com"]
            ),
            1,
        )

    def test_skips_an_owned_account_the_fixture_admin_cannot_make(self):
        # Owned accounts need a two-factor session (#341); the fixture admin
        # has none, so the probe is skipped before it touches any fixture.
        transaction = {
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": self.server.server_port,
            "request": {
                "body": json.dumps({"address": "someone@example.com", "owned": True})
            },
        }
        hooks.host_the_account_probe_domain(transaction)
        self.assertTrue(transaction.get("skip"))
        self.assertEqual(
            [d["name"] for d in self.server.state["domains"]], ["restmail.test"]
        )

    def test_leaves_an_account_it_did_not_create(self):
        self.probe({"address": "admin@restmail.test"})
        self.assertEqual(self.server.state["deleted"], [])
        self.assertEqual(len(self.server.state["accounts"]), 1)

    def test_leaves_malformed_and_unhostable_addresses_to_the_probe(self):
        for body in (
            {},
            [],
            {"address": ""},
            {"address": "not-an-address"},
            {"address": "a@b"},
            {"address": 7},
            {"address": "someone@nowhere.invalid"},
        ):
            self.probe(body)
        self.assertEqual(
            [d["name"] for d in self.server.state["domains"]], ["restmail.test"]
        )
        self.assertEqual(self.server.state["deleted"], [])


class LinkedAccount(AdminAPITestCase):
    """The example's account, which getMailbox and friends are linked to."""

    EXAMPLE: ClassVar[dict] = {
        "address": "new-account@restmail.test",
        "password": "correct-horse-battery",
    }

    def setUp(self):
        super().setUp()
        self.probe(self.EXAMPLE)
        self.create(self.EXAMPLE["address"])
        hooks.keep_the_created_account(
            {
                "request": {"body": json.dumps(self.EXAMPLE)},
                "real": {"body": json.dumps({"data": {"id": 101}})},
            }
        )

    def linked(self, account_id):
        transaction = {
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": self.server.server_port,
            "fullPath": f"/api/v1/admin/mailboxes/{account_id}",
        }
        hooks.on_the_created_account(transaction)
        return transaction["fullPath"]

    def test_keeps_the_link_while_the_account_stands(self):
        self.assertEqual(self.linked(101), "/api/v1/admin/mailboxes/101")

    def test_follows_the_account_a_repeated_probe_replaced(self):
        self.probe(dict(self.EXAMPLE, display_name=""))
        self.assertEqual(self.server.state["deleted"], [101])
        self.create(self.EXAMPLE["address"])
        self.assertEqual(self.linked(101), "/api/v1/admin/mailboxes/102")

    def test_recreates_the_account_a_refused_probe_removed(self):
        self.probe(dict(self.EXAMPLE, password="short"))
        self.assertEqual(self.linked(101), "/api/v1/admin/mailboxes/102")
        self.assertEqual(self.server.state["deleted"], [101])
        self.assertEqual(
            [a["address"] for a in self.server.state["accounts"]],
            ["admin@restmail.test", "new-account@restmail.test"],
        )

    def test_leaves_other_ids_alone(self):
        self.probe(dict(self.EXAMPLE, display_name=""))
        for account_id in ("1", "0", "999999", "abc"):
            self.assertEqual(
                self.linked(account_id), "/api/v1/admin/mailboxes/" + account_id
            )
        hooks.hooks.stash.pop("created_account")
        self.assertEqual(self.linked(101), "/api/v1/admin/mailboxes/101")


class CreateAndLinkAccountProbes(AdminAPITestCase):
    def test_both_operations_repeat_without_consuming_each_others_fixture(self):
        origin = f"http://127.0.0.1:{self.server.server_port}"
        proof = {"address": "someone@example.com", "password": "x"}
        setup_calls = []

        def prepare(*args, **kwargs):
            setup_calls.append(args)
            account = next(
                account
                for account in self.server.state["accounts"]
                if account["address"] == proof["address"]
            )
            account["password"] = proof["password"]

        def send(path, body):
            request = urllib.request.Request(
                origin + path,
                data=json.dumps(body).encode(),
                headers={
                    "Content-Type": "application/json",
                    "Authorization": "Bearer caller",
                },
            )
            with urllib.request.urlopen(request, timeout=10) as response:
                self.assertEqual(response.status, 201)

        with patch.object(hooks.subprocess, "run", side_effect=prepare):
            for cycle in range(2):
                for body in (
                    {"address": proof["address"]},
                    {"address": proof["address"], "password": "xxxxxxxxxxxx"},
                ):
                    self.probe(body)
                    send("/api/v1/accounts", body)
                for _ in range(2):
                    transaction = {
                        "protocol": "http:",
                        "host": "127.0.0.1",
                        "port": self.server.server_port,
                        "request": {
                            "body": json.dumps(proof),
                            "headers": {"Authorization": "Bearer caller"},
                        },
                    }
                    before = json.dumps(transaction, sort_keys=True)
                    hooks.link_a_seeded_account(transaction)
                    self.assertEqual(json.dumps(transaction, sort_keys=True), before)
                    send("/api/v1/accounts/link", proof)
                self.assertEqual(len(setup_calls), cycle + 1)


if __name__ == "__main__":
    unittest.main()
