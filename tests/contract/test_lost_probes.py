"""Probes a hook ends appear in no report, so the hook worker counts them."""

import io
import json
from pathlib import Path
import runpy
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
# The name and operationId Vertrag 0.42.0 gives a before hook in the coverage process.
NAME = "/api/v1/accounts/{id}/credentials > Make an app password > 201 > application/json"
OPERATION = "createAppPassword"


def worker():
    """The two entry points of hooks/worker/python.py the count registers with,
    run in the order handle() runs them: each-hooks, then named hooks by pattern."""
    module = types.ModuleType("vertrag_hooks")
    module.each, module.named = [], []
    module.before_each = lambda function: module.each.append(function) or function
    module.before = lambda pattern: lambda function: module.named.append(function) or function
    return module


class LostProbeCountTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.report = Path(directory.name) / "lost.txt"
        self.report.write_text("")
        self.hooks = worker()
        register = runpy.run_path(str(ROOT / "tests/contract/lost_probes.py"))["register_lost_probe_count"]
        with patch.dict(sys.modules, {"vertrag_hooks": self.hooks}):
            register({"CONTRACT_COVERAGE": "1", "CONTRACT_LOST_PROBES": str(self.report)})

    def send(self, body, change=None, name=NAME, skip=False):
        transaction = {"name": name, "operationId": OPERATION, "request": {"body": body}}
        for function in self.hooks.each:
            function(transaction)
        if change is not None:
            transaction["request"]["body"] = change
        if skip:
            transaction["skip"] = True
        for function in self.hooks.named:
            function(transaction)

    def documented(self):
        """The examples phase's request and the working check's, both rewritten."""
        for _ in range(2):
            self.send('{"name":"iPhone Mail"}', change='{"name":"iPhone Mail","password":"p"}')

    def counted(self):
        return self.report.read_text().split()

    def test_rewriting_the_documented_requests_loses_nothing(self):
        self.documented()
        self.assertEqual(self.counted(), [])

    def test_a_rewritten_probe_is_counted_under_its_operation(self):
        self.documented()
        self.send('{"name":"x"}', change='{"name":"x","password":"p"}')
        self.assertEqual(self.counted(), [OPERATION])

    def test_a_probe_left_alone_is_not_counted(self):
        self.documented()
        self.send('{"name":12345}')
        self.send(json.dumps({"name": "x"}))
        self.assertEqual(self.counted(), [])

    def test_a_probe_a_hook_skipped_is_counted(self):
        self.documented()
        self.send('{"name":"x"}', skip=True)
        self.assertEqual(self.counted(), [OPERATION])

    def test_quiet_hours_rewrites_are_counted_after_documented_requests(self):
        count = self.hooks.named[0]
        with patch.dict(sys.modules, {"vertrag_hooks": self.hooks}):
            register = runpy.run_path(str(ROOT / "tests/contract/quiet_hours_hooks.py"))[
                "register_quiet_hours_hooks"
            ]
            prepare = register(lambda origin: "setup-session")
        for _ in range(3):
            transaction = {
                "name": "/api/v1/accounts/{id}/quiet-hours > Set quiet hours > 200 > application/json",
                "operationId": "setQuietHours",
                "protocol": "http:",
                "host": "localhost",
                "port": "8080",
                "fullPath": "/api/v1/accounts/1/quiet-hours",
                "expected": {"statusCode": "200"},
                "request": {
                    "method": "PUT",
                    "headers": {"Authorization": "Bearer fixture-session"},
                    "body": '{"enabled":false,"start":"","end":"","time_zone":"x"}',
                },
            }
            for function in self.hooks.each:
                function(transaction)
            response = io.BytesIO(b'{"data":[{"id":42}]}')
            with patch("urllib.request.urlopen", return_value=response):
                prepare(transaction)
            count(transaction)
        self.assertEqual(self.counted(), ["setQuietHours"])

    def test_each_transaction_has_its_own_documented_requests(self):
        self.documented()
        other = NAME.replace("201", "202")
        self.send("{}", change='{"a":1}', name=other)
        self.send("{}", change='{"a":1}', name=other)
        self.assertEqual(self.counted(), [])
        self.send("{}", change='{"a":1}', name=other)
        self.assertEqual(self.counted(), [OPERATION])


class ScopeTests(unittest.TestCase):
    def test_only_the_coverage_process_with_a_report_counts(self):
        register = runpy.run_path(str(ROOT / "tests/contract/lost_probes.py"))["register_lost_probe_count"]
        for environment in ({}, {"CONTRACT_COVERAGE": "1"}, {"CONTRACT_LOST_PROBES": "/tmp/unused"}):
            with self.subTest(environment=environment):
                hooks = worker()
                with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
                    register(environment)
                self.assertEqual((hooks.each, hooks.named), ([], []))

    def test_the_count_is_registered_last(self):
        source = (ROOT / "tests/contract/entrypoint.py").read_text()
        self.assertEqual(source.rstrip().splitlines()[-1], 'lost["register_lost_probe_count"]()')


if __name__ == "__main__":
    unittest.main()
