import copy
import importlib.util
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch


BODY_BYTES = 32 * 1024 * 1024 + 1


class OversizedProofLifecycleTests(unittest.TestCase):
    def setUp(self):
        self.callbacks = {event: [] for event in (
            "before_each", "before_each_validation", "after_all",
        )}
        stub = types.ModuleType("vertrag_hooks")
        for event, callbacks in self.callbacks.items():
            def register(function, callbacks=callbacks):
                callbacks.append(function)
                return function
            setattr(stub, event, register)
        source = Path(__file__).with_name("live_requirement_hooks.py")
        spec = importlib.util.spec_from_file_location("oversized_lifecycle", source)
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": stub}):
            spec.loader.exec_module(module)

    def emit(self, event, value):
        for callback in self.callbacks[event]:
            callback(value)

    def transaction(self):
        return {
            "request": {"body": "{}", "headers": {"Content-Type": "application/json"}},
            "expected": {"statusCode": "413", "body": '{"error":"too large"}'},
            "real": {"statusCode": "", "body": ""},
            "fail": "unexpected EOF",
        }

    def prepare(self, transaction):
        with patch.dict(os.environ, {"CONTRACT_OVER_CAP_BYTES": str(BODY_BYTES)}):
            self.emit("before_each", transaction)

    def test_failed_exchange_releases_only_finished_request_body(self):
        transaction = self.transaction()
        self.prepare(transaction)
        self.assertEqual(transaction["request"]["body"], " " * BODY_BYTES)
        expected = copy.deepcopy(transaction)
        expected["request"]["body"] = ""
        self.emit("after_all", [transaction])
        self.assertEqual(transaction["request"]["body"], "")
        self.assertEqual(transaction, expected)

    def test_successful_exchange_keeps_all_bytes_until_validation(self):
        transaction = self.transaction()
        transaction["fail"] = ""
        self.prepare(transaction)
        self.assertEqual(len(transaction["request"]["body"].encode()), BODY_BYTES)
        self.assertEqual(transaction["request"]["headers"]["Content-Length"], str(BODY_BYTES))
        transaction["real"] = {"statusCode": "413", "body": '{"error":"too large"}'}
        self.emit("before_each_validation", transaction)
        expected = copy.deepcopy(transaction)
        self.emit("after_all", [transaction])
        self.assertEqual(transaction["request"]["body"], "")
        self.assertEqual(transaction, expected)

    def test_terminal_cleanup_handles_empty_and_already_cleared_runs(self):
        self.emit("after_all", [])
        transaction = self.transaction()
        transaction["request"]["body"] = ""
        expected = copy.deepcopy(transaction)
        self.emit("after_all", [transaction])
        self.emit("after_all", [transaction])
        self.assertEqual(transaction, expected)
