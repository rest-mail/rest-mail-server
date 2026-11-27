"""testCustomFilter's valid fuzz probes reach a filter with a runnable script,
with the generated request unchanged (#2051)."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading
import unittest
from unittest.mock import patch

from test_hooks_delete_domain import load_hooks

VALID = 'fuzz: /api/v1/admin/custom-filters/{id}/test > Test custom filter > 200 > application/json · path parameter "id" · valid'
INVALID = 'fuzz: /api/v1/admin/custom-filters/{id}/test > Test custom filter > 200 > application/json · path parameter "id" · invalid'


class CustomFilterAPI(BaseHTTPRequestHandler):
    """The admin custom-filter update over an in-memory set of filters."""

    def log_message(self, *args):
        pass

    def answer(self, status, body=None):
        data = json.dumps(body).encode() if body is not None else b""
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_PATCH(self):
        state = self.server.state
        state["calls"].append(("PATCH", self.path, self.headers.get("Authorization")))
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        resource = self.path.rsplit("/", 1)[1]
        if resource not in state["filters"]:
            return self.answer(404, {"error": {"code": "not_found"}})
        state["filters"][resource].update(body)
        self.answer(200, {"data": {"id": int(resource), **state["filters"][resource]}})


class FuzzCustomFilterScript(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, registered = load_hooks()
        self.assertIn("testCustomFilter", registered)
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), CustomFilterAPI)
        # Filter 17 as run 45824626516 left it: an earlier generated
        # updateCustomFilter body replaced its config with one holding no script.
        self.server.state = {"calls": [], "filters": {"17": {"config": {}}}}
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        patch.object(self.module, "admin_token", lambda origin: "setup").start()
        self.addCleanup(patch.stopall)

    def fuzzing(self):
        patch.dict("os.environ", {"CONTRACT_FUZZ": "1"}).start()

    def transaction(self, resource="17", name=VALID, suffix="/test"):
        return {
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": self.server.server_address[1],
            "name": name,
            "fullPath": f"/api/v1/admin/custom-filters/{resource}{suffix}",
            "request": {"body": "{}", "headers": {"Authorization": "Bearer generated"}},
        }

    def test_the_valid_probe_gets_a_runnable_script_and_is_sent_unchanged(self):
        self.fuzzing()
        transaction = self.transaction()
        original = json.loads(json.dumps(transaction))
        self.module.a_runnable_script_on_a_generated_custom_filter(transaction)
        self.assertEqual(transaction, original)
        self.assertEqual(self.server.state["filters"]["17"]["config"], self.module.CUSTOM_FILTER["config"])
        self.assertIn("function filter", self.server.state["filters"]["17"]["config"]["script"])
        self.assertEqual(
            self.server.state["calls"],
            [("PATCH", "/api/v1/admin/custom-filters/17", "Bearer setup")],
        )

    def test_an_id_with_no_filter_keeps_its_404(self):
        self.fuzzing()
        transaction = self.transaction("4294967295")
        original = json.loads(json.dumps(transaction))
        self.module.a_runnable_script_on_a_generated_custom_filter(transaction)
        self.assertEqual(transaction, original)
        self.assertEqual(self.server.state["filters"], {"17": {"config": {}}})

    def test_other_probes_and_processes_are_left_alone(self):
        cases = [
            ("not fuzzing", self.transaction(), False),
            ("invalid probe", self.transaction("4294967296", INVALID), True),
            ("undecodable id", self.transaction("x"), True),
            ("not the test route", self.transaction(suffix=""), True),
            ("documented 404", self.transaction(name=VALID.replace(" > 200 > ", " > 404 > ")), True),
        ]
        for label, transaction, fuzz in cases:
            with self.subTest(label):
                with patch.dict("os.environ", {"CONTRACT_FUZZ": "1" if fuzz else "0"}):
                    original = json.loads(json.dumps(transaction))
                    self.module.a_runnable_script_on_a_generated_custom_filter(transaction)
                    self.assertEqual(transaction, original)
        self.assertEqual(self.server.state["calls"], [])
        self.assertEqual(self.server.state["filters"], {"17": {"config": {}}})


if __name__ == "__main__":
    unittest.main()
