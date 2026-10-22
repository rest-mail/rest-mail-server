"""Scheduling fixture setup can outwait probes spending the admin's send budget."""

import copy
import importlib.util
import io
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

import yaml

ROOT = Path(__file__).resolve().parents[2]


class Response(io.BytesIO):
    status = 202

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()


class ContractScheduledFixtureTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = hooks.after = lambda *args: lambda function: function
        hooks.stash = {"setup_admin_tokens": {"http://api:8080": "setup-session"}}
        spec = importlib.util.spec_from_file_location("contract_scheduled_hooks", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)
        self.transaction = {
            "protocol": "http:", "host": "api", "port": "8080",
            "fullPath": "/api/v1/messages/scheduled/1",
            "request": {"headers": {"Authorization": "Bearer probe-session"}},
        }
        self.sleep = patch.object(self.module.time, "sleep").start()
        self.addCleanup(patch.stopall)

    def limited(self, retry="60", status=429):
        return HTTPError("http://api:8080/api/v1/messages/send", status,
                         "refused", {"Retry-After": retry}, io.BytesIO(b"{}"))

    def test_worker_deadline_allows_the_fixture_retry(self):
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        # One 60-second wait, two 10-second requests and a 10-second login.
        self.assertGreaterEqual(config["hooks-worker-timeout"], 90_000)

    def test_cancel_fixture_waits_for_one_send_window_and_keeps_the_probe_principal(self):
        original = copy.deepcopy(self.transaction)
        limited = self.limited()
        with patch.object(self.module.urllib.request, "urlopen", side_effect=[
                limited, Response(b'{"data":{"id":42}}')]) as network:
            self.module.cancel_a_message_we_made(self.transaction)
        self.sleep.assert_called_once_with(60)
        self.assertEqual(network.call_count, 2)
        self.assertTrue(limited.fp.closed)
        first, second = [call.args[0] for call in network.call_args_list]
        self.assertIs(first, second)
        self.assertEqual(first.get_header("Authorization"), "Bearer setup-session")
        self.assertEqual(first.get_method(), "POST")
        self.assertEqual(json.loads(first.data)["from"], self.module.ADMIN)
        self.assertEqual(self.transaction["request"], original["request"])
        self.assertEqual(self.transaction["fullPath"], "/api/v1/messages/scheduled/42")

    def test_a_second_rate_limit_stops_setup_instead_of_waiting_again(self):
        original = copy.deepcopy(self.transaction)
        with patch.object(self.module.urllib.request, "urlopen", side_effect=[
                self.limited(), self.limited()]) as network:
            with self.assertRaises(HTTPError):
                self.module.cancel_a_message_we_made(self.transaction)
        self.sleep.assert_called_once_with(60)
        self.assertEqual(network.call_count, 2)
        self.assertEqual(self.transaction, original)

    def test_only_a_bounded_rate_limit_delay_is_retried(self):
        for status, delay in ((403, "60"), (500, "60"), (429, ""),
                              (429, "later"), (429, "0"), (429, "-1"), (429, "61")):
            with self.subTest(status=status, delay=delay):
                with patch.object(self.module.urllib.request, "urlopen",
                                  side_effect=self.limited(delay, status)) as network:
                    with self.assertRaises(HTTPError):
                        self.module.schedule_a_message(self.transaction)
                self.assertEqual(network.call_count, 1)
        self.sleep.assert_not_called()

    def test_no_wait_when_scheduling_succeeds(self):
        with patch.object(self.module.urllib.request, "urlopen",
                          return_value=Response(b'{"data":{"id":42}}')) as network:
            self.assertEqual(self.module.schedule_a_message(self.transaction), 42)
        self.assertEqual(network.call_count, 1)
        self.sleep.assert_not_called()

    def test_invalid_id_probes_need_no_fixture_and_keep_their_path(self):
        with patch.object(self.module.urllib.request, "urlopen") as network:
            for value in ("0", "-1", "not-an-id", "1.5"):
                transaction = copy.deepcopy(self.transaction)
                transaction["fullPath"] = "/api/v1/messages/scheduled/" + value
                original = copy.deepcopy(transaction)
                self.module.cancel_a_message_we_made(transaction)
                self.assertEqual(transaction, original)
        network.assert_not_called()
        self.sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main()
