"""Generated relay updates start without credentials from earlier probes (#2051)."""

import copy
import io
import json
import runpy
import sys
import types
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import patch


class FuzzRelayStateTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.stash = {"setup_admin_tokens": {"http://fixture:8080": "setup"}}
        self.before = []

        def before(operation):
            def register(function):
                if operation == "setDomainRelay":
                    self.before.append(function)
                return function

            return register

        hooks.before = before
        hooks.after = lambda _: lambda function: function
        environment = patch.dict("os.environ", {
            "CONTRACT_AUTHORIZATION": "0", "CONTRACT_RESET_SEED": "0", "CONTRACT_FUZZ": "1",
        })
        environment.start()
        self.addCleanup(environment.stop)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            runpy.run_path(str(Path(__file__).with_name("entrypoint.py")))
        self.transaction = {
            "protocol": "http:", "host": "fixture", "port": 8080,
            "fullPath": "/api/v1/admin/domains/17/relay?probe=1",
            "request": {
                "method": "PUT",
                "headers": {"Authorization": "Bearer generated"},
                "body": json.dumps({
                    "transport": "relay", "host": "smtp.example.net", "port": 587,
                    "tls_mode": "starttls", "username": "",
                }),
            },
        }

    def run_before_hooks(self):
        original = copy.deepcopy(self.transaction)
        for before in self.before:
            before(self.transaction)
        self.assertEqual(self.transaction, original)

    def test_each_probe_clears_credentials_with_a_separate_request(self):
        responses = [io.BytesIO(b'{"data":{"password_set":false}}') for _ in range(2)]
        with patch("urllib.request.urlopen", side_effect=responses) as send:
            self.run_before_hooks()
            self.transaction["fullPath"] = "/api/v1/admin/domains/000000000017/relay"
            self.run_before_hooks()
        requests = [call.args[0] for call in send.call_args_list]
        self.assertEqual([request.full_url for request in requests], [
            "http://fixture:8080/api/v1/admin/domains/17/relay",
            "http://fixture:8080/api/v1/admin/domains/000000000017/relay",
        ])
        for request in requests:
            self.assertEqual(request.method, "PUT")
            self.assertEqual(request.get_header("Authorization"), "Bearer setup")
            self.assertEqual(json.loads(request.data), {"transport": "direct", "password": ""})

    def test_missing_domain_still_gets_its_probe_and_other_setup_errors_raise(self):
        missing = urllib.error.HTTPError("fixture", 404, "not found", {}, None)
        failure = urllib.error.HTTPError("fixture", 500, "failed", {}, None)
        with patch("urllib.request.urlopen", side_effect=[missing, failure]) as send:
            self.run_before_hooks()
            with self.assertRaises(urllib.error.HTTPError) as raised:
                self.run_before_hooks()
        self.assertIs(raised.exception, failure)
        self.assertEqual(send.call_count, 2)

    def test_other_phases_and_invalid_domain_ids_need_no_setup(self):
        with (
            patch.dict("os.environ", {"CONTRACT_FUZZ": "0"}),
            patch("urllib.request.urlopen") as send,
        ):
            self.run_before_hooks()
            send.assert_not_called()
        with patch("urllib.request.urlopen") as send:
            for domain_id in ("0", "-1", "bad", "4294967296"):
                with self.subTest(domain_id=domain_id):
                    self.transaction["fullPath"] = f"/api/v1/admin/domains/{domain_id}/relay"
                    self.run_before_hooks()
            send.assert_not_called()


if __name__ == "__main__":
    unittest.main()
