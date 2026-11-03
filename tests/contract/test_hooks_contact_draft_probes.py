"""Exercise the real hook entrypoint with Vertrag's unprefixed transaction names."""

import copy
import json
import os
import runpy
import sys
import threading
import types
import unittest
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
CONTACT = "/api/v1/accounts/{id}/contacts/{cid}"
DRAFT = "/api/v1/messages/draft/{id}"
FOLDER = "/api/v1/accounts/{id}/folders/{folder}"
OPERATIONS = {
    "updateContact": (CONTACT + " > Update a contact > 200 > application/json", "/api/v1/accounts/1/contacts/1"),
    "deleteContact": (CONTACT + " > Delete a contact > 204", "/api/v1/accounts/1/contacts/41"),
    "updateDraft": (DRAFT + " > Update an existing draft > 200 > application/json", "/api/v1/messages/draft/12"),
    "sendDraft": (DRAFT + "/send > Send a draft > 201 > application/json", "/api/v1/messages/draft/12/send"),
    "deleteFolder": (FOLDER + " > Delete a folder and its messages > 204", "/api/v1/accounts/1/folders/Work"),
}
FIXTURES = {
    "a_contact_for_the_coverage_placeholder",
    "a_draft_for_the_coverage_placeholder",
    "a_folder_for_the_coverage_placeholder",
    "send_a_draft_of_its_own",
}


class FixtureAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.calls.append((self.path, self.headers.get("Authorization"), body))
        if self.server.failure:
            self.send_response(self.server.failure)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        self.server.serial += 1
        payload = json.dumps({"data": dict(body, id=self.server.serial)}).encode()
        self.send_response(201)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


class ContactDraftCoverageHooksTests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.calls = []
        self.server.serial = 80
        self.server.failure = None
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.callbacks = {}
        stub = types.ModuleType("vertrag_hooks")
        stub.stash = {}
        stub.before = lambda name: lambda callback: self.callbacks.setdefault(name, []).append(callback) or callback
        stub.after = lambda name: lambda callback: callback
        stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
        stub.set_json = lambda transaction, body: transaction["request"].__setitem__("body", json.dumps(body))
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        with patch.dict(sys.modules, {"vertrag_hooks": stub}):
            runpy.run_path(str(ROOT / config["hookfiles"]))
        origin = "http://127.0.0.1:%d" % self.server.server_port
        stub.stash["setup_admin_tokens"] = {origin: "setup-session"}
        environment = patch.dict(os.environ, {"CONTRACT_COVERAGE": "1"})
        environment.start()
        self.addCleanup(environment.stop)

    def transaction(self, operation, path=None, body='{"name":"","from":null}'):
        name, original_path = OPERATIONS[operation]
        return {
            "name": name,
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_port),
            "fullPath": (path or original_path) + "?keep=yes",
            "request": {
                "headers": {"Authorization": "Bearer original-session"},
                "body": body,
            },
        }

    def prepare(self, operation, transaction):
        callbacks = [callback for callback in self.callbacks[operation] if callback.__name__ in FIXTURES]
        self.assertEqual(len(callbacks), 1)
        callbacks[0](transaction)

    def test_each_operation_changes_only_its_path(self):
        for operation in OPERATIONS:
            for body in ('{"name":"","from":null}', "[]", "null", "{"):
                with self.subTest(operation=operation, body=body):
                    transaction = self.transaction(operation, body=body)
                    before = copy.deepcopy(transaction)
                    self.prepare(operation, transaction)
                    self.assertNotEqual(transaction["fullPath"], before["fullPath"])
                    self.assertTrue(transaction["fullPath"].endswith("?keep=yes"))
                    before["fullPath"] = transaction["fullPath"]
                    self.assertEqual(transaction, before)

    def test_each_request_gets_a_fresh_row(self):
        for operation in OPERATIONS:
            with self.subTest(operation=operation):
                first, second = self.transaction(operation), self.transaction(operation)
                self.prepare(operation, first)
                self.prepare(operation, second)
                self.assertNotEqual(first["fullPath"], second["fullPath"])

    def test_minimum_id_probes_also_get_fresh_targets(self):
        for operation, path in (
            ("deleteContact", "/api/v1/accounts/1/contacts/1"),
            ("updateDraft", "/api/v1/messages/draft/1"),
            ("sendDraft", "/api/v1/messages/draft/1/send"),
        ):
            with self.subTest(operation=operation):
                transaction = self.transaction(operation, path)
                self.prepare(operation, transaction)
                self.assertNotEqual(transaction["fullPath"], path + "?keep=yes")

    def test_setup_uses_its_own_session_and_self_addressed_drafts(self):
        for operation in OPERATIONS:
            self.prepare(operation, self.transaction(operation))
        self.assertTrue(all(call[1] == "Bearer setup-session" for call in self.server.calls))
        drafts = [body for path, _, body in self.server.calls if path.endswith("/draft")]
        self.assertEqual(len(drafts), 2)
        for draft in drafts:
            self.assertEqual(draft["from"], "admin@restmail.test")
            self.assertEqual(draft["to"], ["admin@restmail.test"])

    def test_other_targets_and_malformed_ids_are_not_replaced(self):
        for operation in OPERATIONS:
            original = OPERATIONS[operation][1]
            for target in ("0", "vertrag-not-a-number", "1.5", "2147483647"):
                path = original.rsplit("/", 1)[0] + "/" + target
                if operation == "sendDraft":
                    if target == "2147483647":
                        continue
                    path = "/api/v1/messages/draft/" + target + "/send"
                with self.subTest(operation=operation, target=target):
                    transaction = self.transaction(operation, path)
                    before = copy.deepcopy(transaction)
                    self.prepare(operation, transaction)
                    self.assertEqual(transaction, before)
        for operation in ("updateContact", "deleteContact", "updateDraft", "deleteFolder"):
            transaction = self.transaction(operation, OPERATIONS[operation][1].rsplit("/", 1)[0] + "/2")
            before = copy.deepcopy(transaction)
            self.prepare(operation, transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_other_accounts_and_collections_are_untouched(self):
        for operation in ("updateContact", "deleteContact", "deleteFolder"):
            for path in (
                OPERATIONS[operation][1].replace("/accounts/1/", "/accounts/0/"),
                OPERATIONS[operation][1].replace("/accounts/1/", "/accounts/2/"),
                OPERATIONS[operation][1].replace("/accounts/", "/admin/"),
            ):
                transaction = self.transaction(operation, path)
                before = copy.deepcopy(transaction)
                self.prepare(operation, transaction)
                self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_refusal_examples_are_left_alone(self):
        for operation in ("updateContact", "deleteContact", "updateDraft", "deleteFolder"):
            for status in (401, 403, 404, 422, 500):
                transaction = self.transaction(operation)
                transaction["name"] = transaction["name"].replace(" > 200", " > %d" % status).replace(" > 204", " > %d" % status)
                before = copy.deepcopy(transaction)
                self.prepare(operation, transaction)
                self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_other_phases_do_not_get_the_new_fixtures(self):
        with patch.dict(os.environ):
            del os.environ["CONTRACT_COVERAGE"]
            for operation in ("updateContact", "deleteContact", "updateDraft", "deleteFolder"):
                transaction = self.transaction(operation)
                before = copy.deepcopy(transaction)
                self.prepare(operation, transaction)
                self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_setup_failures_propagate(self):
        self.server.failure = 500
        for operation in OPERATIONS:
            with self.subTest(operation=operation), self.assertRaises(urllib.error.HTTPError):
                self.prepare(operation, self.transaction(operation))


if __name__ == "__main__":
    unittest.main()
