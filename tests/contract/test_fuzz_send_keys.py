import copy
import json
import os
import subprocess
import unittest
from unittest.mock import patch

from test_hooks_send_message import load_hooks


class FuzzSendKeyTests(unittest.TestCase):
    def setUp(self):
        self.module, _ = load_hooks()
        self.transaction = {
            "name": "/api/v1/messages/draft/{id}/send > Send a draft > 201 > application/json",
            "protocol": "http:",
            "host": "api.test",
            "port": 8080,
            "fullPath": "/api/v1/messages/draft/12/send",
            "request": {"headers": {"Authorization": "Bearer caller", "Idempotency-Key": "d"}, "body": "{}"},
        }
        fuzz = patch.dict(os.environ, CONTRACT_FUZZ="1")
        fuzz.start()
        self.addCleanup(fuzz.stop)

    def test_repeated_short_keys_get_fresh_fixture_state_and_reach_the_handler_unchanged(self):
        for path in ("/api/v1/messages/send", "/api/v1/messages/draft/12/send", "/api/v1/messages/12/forward"):
            with self.subTest(path=path):
                transaction = copy.deepcopy(self.transaction)
                transaction["fullPath"] = path
                before = copy.deepcopy(transaction)
                with (
                    patch.object(self.module, "session_address", return_value=self.module.ADMIN),
                    patch.object(self.module.time, "time_ns", return_value=1000),
                    patch.object(self.module.subprocess, "run") as prepare,
                ):
                    self.module.a_used_send_idempotency_key(transaction)
                    self.module.a_used_send_idempotency_key(transaction)
                self.assertEqual(transaction, before)
                self.assertEqual(prepare.call_count, 2)
                for call in prepare.call_args_list:
                    self.assertEqual(call.args[0], ["chore", "test:contract-send-key-fixture"])
                    self.assertEqual(
                        json.loads(call.kwargs["env"]["CONTRACT_SEND_KEY_REQUEST"]),
                        {"address": self.module.ADMIN, "key": "d"},
                    )
                    self.assertTrue(call.kwargs["check"])

    def test_invalid_keys_and_other_principals_are_left_to_the_handler(self):
        for address, key in ((self.module.ADMIN, ""), (self.module.ADMIN, "é" * 128), ("other@restmail.test", "d")):
            with self.subTest(address=address, key=key):
                self.transaction["request"]["headers"]["Idempotency-Key"] = key
                before = copy.deepcopy(self.transaction)
                with (
                    patch.object(self.module, "session_address", return_value=address),
                    patch.object(self.module.subprocess, "run") as prepare,
                ):
                    self.module.a_used_send_idempotency_key(self.transaction)
                prepare.assert_not_called()
                self.assertEqual(self.transaction, before)

    def test_failed_fixture_preparation_is_reported(self):
        before = copy.deepcopy(self.transaction)
        with (
            patch.object(self.module, "session_address", return_value=self.module.ADMIN),
            patch.object(self.module.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "chore")),
        ):
            with self.assertRaises(subprocess.CalledProcessError):
                self.module.a_used_send_idempotency_key(self.transaction)
        self.assertEqual(self.transaction, before)
