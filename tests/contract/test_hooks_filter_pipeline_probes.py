"""Coverage's custom filter and pipeline examples get a row of their own.

The transactions carry the names Vertrag 0.42.0 gives a before hook, which never
have a `coverage: ` prefix, and are loaded through the real hook entrypoint.
"""

import copy
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import runpy
import sys
import threading
import types
import unittest
import urllib.error
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]

FILTER = "/api/v1/admin/custom-filters/{id}"
PIPELINE = "/api/v1/admin/pipelines/{id}"
OPERATIONS = {
    "getCustomFilter": (FILTER + " > Get custom filter > 200 > application/json", "/api/v1/admin/custom-filters/%s"),
    "updateCustomFilter": (FILTER + " > Update custom filter > 200 > application/json", "/api/v1/admin/custom-filters/%s"),
    "deleteCustomFilter": (FILTER + " > Delete custom filter > 200 > application/json", "/api/v1/admin/custom-filters/%s"),
    "testCustomFilter": (FILTER + "/test > Test custom filter > 200 > application/json", "/api/v1/admin/custom-filters/%s/test"),
    "updatePipeline": (PIPELINE + " > Update pipeline > 200 > application/json", "/api/v1/admin/pipelines/%s"),
    "deletePipeline": (PIPELINE + " > Delete pipeline > 200 > application/json", "/api/v1/admin/pipelines/%s"),
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


class CoveragePlaceholderTests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.calls = []
        self.server.serial = 40
        self.server.failure = None
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.callbacks = {}
        stub = types.ModuleType("vertrag_hooks")
        stub.stash = {}
        stub.before = lambda name: lambda fn: self.callbacks.setdefault(name, []).append(fn) or fn
        stub.after = lambda name: lambda fn: fn
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

    def transaction(self, operation, target="1", query="?keep=yes"):
        name, path = OPERATIONS[operation]
        return {
            "name": name,
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_port),
            "fullPath": path % target + query,
            "request": {
                "headers": {"Authorization": "Bearer original-session"},
                "body": json.dumps({"name": "", "domain_id": -1}),
            },
        }

    def send(self, operation, transaction):
        # Only the hook under test: pacing hooks on the operation need no check.
        for callback in self.callbacks[operation]:
            if callback.__name__.endswith("_for_the_coverage_placeholder"):
                callback(transaction)

    def rows(self):
        return [call for call in self.server.calls if not call[0].endswith("/domains")]

    def test_each_operation_moves_only_its_path_to_a_row_of_its_own(self):
        for operation, (_, path) in OPERATIONS.items():
            with self.subTest(operation=operation):
                transaction = self.transaction(operation)
                before = copy.deepcopy(transaction)
                self.send(operation, transaction)
                # A changed body would end the probe uncounted (ErrChangedByHook).
                before["fullPath"] = path % self.server.serial + "?keep=yes"
                self.assertEqual(transaction, before)

    def test_every_request_gets_a_new_row(self):
        for operation in OPERATIONS:
            with self.subTest(operation=operation):
                first, second = self.transaction(operation), self.transaction(operation)
                self.send(operation, first)
                self.send(operation, second)
                self.assertNotEqual(first["fullPath"], second["fullPath"])

    def test_rows_stay_off_the_mail_path_on_one_disposable_domain(self):
        for operation in OPERATIONS:
            self.send(operation, self.transaction(operation))
        domains = [call for call in self.server.calls if call[0].endswith("/domains")]
        self.assertEqual(len(domains), 1)
        self.assertTrue(domains[0][2]["name"].startswith("contract-probe-"))
        domain_id = 41
        for path, session, row in self.rows():
            self.assertEqual(session, "Bearer setup-session")
            self.assertEqual(row["domain_id"], domain_id)
            if path.endswith("/pipelines"):
                self.assertIs(row["active"], False)
            else:
                self.assertIs(row["enabled"], False)
                # testCustomFilter refuses a config without a script.
                self.assertIn("function filter", row["config"]["script"])

    def test_other_targets_are_sent_as_generated(self):
        for operation in OPERATIONS:
            for target in ("0", "2", "x", "1.5", "vertrag-not-a-number", "2147483647"):
                with self.subTest(operation=operation, target=target):
                    transaction = self.transaction(operation, target)
                    before = copy.deepcopy(transaction)
                    self.send(operation, transaction)
                    self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_refusal_examples_keep_their_target(self):
        for operation in OPERATIONS:
            for status in (401, 403, 404, 422, 500):
                with self.subTest(operation=operation, status=status):
                    transaction = self.transaction(operation)
                    transaction["name"] = transaction["name"].replace(" > 200", " > %d" % status)
                    before = copy.deepcopy(transaction)
                    self.send(operation, transaction)
                    self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_other_phases_are_left_alone(self):
        with patch.dict(os.environ):
            del os.environ["CONTRACT_COVERAGE"]
            for operation in OPERATIONS:
                transaction = self.transaction(operation)
                before = copy.deepcopy(transaction)
                self.send(operation, transaction)
                self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_a_failed_setup_is_not_swallowed(self):
        self.server.failure = 500
        for operation in OPERATIONS:
            with self.subTest(operation=operation), self.assertRaises(urllib.error.HTTPError):
                self.send(operation, self.transaction(operation))


if __name__ == "__main__":
    unittest.main()
