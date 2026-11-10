"""Hooks look an object up through every page of a list, not the first alone.

The server's lists answer one page: 50 rows unless asked, 200 at most. A hook
that read the first page called an object past it absent, then made it again
(409) or failed to find its fixture (#1166). find_in_list and list_rows follow
the pages to the last; the helpers built on them find a row past page one.
"""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
import sys
import threading
import types
import unittest
import urllib.parse
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.before = stub.after = lambda name: (lambda fn: fn)
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    spec = importlib.util.spec_from_file_location("contract_paged_lookup_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module


class ListAPI(BaseHTTPRequestHandler):
    """Lists as the server pages them: by limit and offset, 50 rows unless
    asked and 200 at most, with has_more. A list in `unpaged` is answered
    whole with no pagination, as the sender rule and folder lists are."""

    def log_message(self, *args):
        pass

    def answer(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path, _, query = self.path.partition("?")
        asked = urllib.parse.parse_qs(query)
        self.server.calls.append((path, asked))
        if path in self.server.unpaged:
            return self.answer(200, {"data": self.server.unpaged[path]})
        if path not in self.server.lists:
            return self.answer(404, {"error": {"code": "not_found"}})
        rows = self.server.lists[path]
        domain_id = asked.get("domain_id", [None])[0]
        if domain_id is not None:
            rows = [r for r in rows if r.get("domain_id") == int(domain_id)]
        limit = min(int(asked.get("limit", ["50"])[0]), 200)
        offset = int(asked.get("offset", ["0"])[0])
        self.answer(200, {
            "data": rows[offset:offset + limit],
            "pagination": {"total": len(rows), "has_more": offset + limit < len(rows)},
        })

    def do_POST(self):
        self.server.posts.append(self.path)
        self.answer(201, {"data": {"id": 999}})


class PagedLookups(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), ListAPI)
        self.server.lists, self.server.unpaged = {}, {}
        self.server.calls, self.server.posts = [], []
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.origin = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.hooks = load_hooks()
        patch.object(self.hooks, "admin_token", return_value="admin-token").start()
        self.addCleanup(patch.stopall)

    def mailboxes(self, count, domain_id=1):
        return [
            {"id": n + 1, "domain_id": domain_id, "address": f"user-{n:03d}@restmail.test"}
            for n in range(count)
        ]

    def pages_read(self, path):
        return [asked for p, asked in self.server.calls if p == path]

    def test_a_row_on_page_two_is_found(self):
        self.server.lists["/api/v1/admin/mailboxes"] = self.mailboxes(250)
        found = self.hooks.find_in_list(
            self.origin, "/api/v1/admin/mailboxes", {},
            lambda a: a["address"] == "user-230@restmail.test",
        )
        self.assertEqual(found["id"], 231)
        offsets = [asked["offset"] for asked in self.pages_read("/api/v1/admin/mailboxes")]
        self.assertEqual(offsets, [["0"], ["200"]])

    def test_a_row_absent_from_every_page_is_absent_and_the_paging_stops_at_the_last(self):
        self.server.lists["/api/v1/admin/mailboxes"] = self.mailboxes(450)
        found = self.hooks.find_in_list(
            self.origin, "/api/v1/admin/mailboxes", {},
            lambda a: a["address"] == "absent@restmail.test",
        )
        self.assertIsNone(found)
        offsets = [asked["offset"][0] for asked in self.pages_read("/api/v1/admin/mailboxes")]
        self.assertEqual(offsets, ["0", "200", "400"])

    def test_a_row_on_page_one_reads_no_further(self):
        self.server.lists["/api/v1/admin/mailboxes"] = self.mailboxes(450)
        found = self.hooks.find_in_list(
            self.origin, "/api/v1/admin/mailboxes", {}, lambda a: a["id"] == 3
        )
        self.assertEqual(found["address"], "user-002@restmail.test")
        self.assertEqual(len(self.pages_read("/api/v1/admin/mailboxes")), 1)

    def test_an_empty_list_is_one_request(self):
        self.server.lists["/api/v1/admin/domains"] = []
        self.assertEqual(list(self.hooks.list_rows(self.origin, "/api/v1/admin/domains", {})), [])
        self.assertEqual(len(self.pages_read("/api/v1/admin/domains")), 1)

    def test_an_unpaged_list_is_read_once(self):
        self.server.unpaged["/api/v1/admin/domains/1/allowlist"] = [{"id": 1}, {"id": 2}]
        rows = list(self.hooks.list_rows(self.origin, "/api/v1/admin/domains/1/allowlist", {}))
        self.assertEqual([r["id"] for r in rows], [1, 2])
        self.assertEqual(len(self.pages_read("/api/v1/admin/domains/1/allowlist")), 1)

    def test_a_path_with_a_query_keeps_it_on_every_page(self):
        self.server.lists["/api/v1/admin/mailboxes"] = self.mailboxes(250, domain_id=4) + self.mailboxes(5, domain_id=9)
        rows = list(self.hooks.list_rows(self.origin, "/api/v1/admin/mailboxes?domain_id=4", {}))
        self.assertEqual(len(rows), 250)
        for asked in self.pages_read("/api/v1/admin/mailboxes"):
            self.assertEqual(asked["domain_id"], ["4"])
            self.assertEqual(asked["limit"], ["200"])

    # The helpers built on the pager.

    def test_mailbox_id_and_address_find_an_account_past_the_first_page(self):
        self.server.lists["/api/v1/admin/mailboxes"] = self.mailboxes(300)
        self.assertEqual(self.hooks.mailbox_id(self.origin, "user-299@restmail.test"), 300)
        self.assertEqual(self.hooks.mailbox_address(self.origin, 300), "user-299@restmail.test")
        self.assertIsNone(self.hooks.mailbox_address(self.origin, 2147483647))
        with self.assertRaises(RuntimeError):
            self.hooks.mailbox_id(self.origin, "absent@restmail.test")

    def test_alias_on_finds_an_alias_past_the_first_page(self):
        self.server.lists["/api/v1/admin/aliases"] = [
            {"id": n + 1, "domain_id": 1, "source_address": f"alias-{n:03d}@restmail.test"}
            for n in range(260)
        ]
        found = self.hooks.alias_on(self.origin, {}, 1, "ALIAS-255@restmail.test")
        self.assertEqual(found["id"], 256)
        self.assertIsNone(self.hooks.alias_on(self.origin, {}, 1, "absent@restmail.test"))

    def test_a_hosted_domain_past_the_first_page_is_not_made_again(self):
        self.server.lists["/api/v1/admin/domains"] = [
            {"id": n + 1, "name": f"d{n:03d}.test"} for n in range(220)
        ]
        self.assertEqual(self.hooks.hosted_domain_id(self.origin, {}, "d210.test"), 211)
        self.assertEqual(self.server.posts, [])

    def test_the_newest_held_message_is_taken_from_every_page(self):
        patch.object(self.hooks, "mailbox_id", return_value=6).start()
        patch.object(self.hooks, "session_address", return_value=self.hooks.ADMIN).start()
        held = [{"id": n + 1, "released": False} for n in range(210)]
        self.server.lists["/api/v1/accounts/6/quarantine"] = held[::-1][5:] + held[-5:][::-1]
        transaction = {
            "protocol": "http:", "host": "127.0.0.1", "port": str(self.server.server_address[1]),
            "fullPath": "/api/v1/accounts/1/quarantine/41",
            "request": {"headers": {"Authorization": "Bearer admin"}},
        }
        self.hooks.delete_the_fixture_quarantined_message(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/accounts/6/quarantine/210")


if __name__ == "__main__":
    unittest.main()
