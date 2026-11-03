import copy
import io
from pathlib import Path
import runpy
import sys
import types
import unittest
from unittest.mock import patch


class DMARCReportFixtureTests(unittest.TestCase):
    def setUp(self):
        module = types.ModuleType("vertrag_hooks")
        module.before = lambda _: lambda function: function
        with patch.dict(sys.modules, {"vertrag_hooks": module}):
            loaded = runpy.run_path(str(Path(__file__).with_name("dmarc_report_hooks.py")))
        self.prepare = loaded["register_dmarc_report_hook"](lambda _: "setup-session")
        self.transaction = {
            "protocol": "http:", "host": "fixture.test", "port": "8080",
            "fullPath": "/api/v1/admin/dmarc-reports/1",
            "request": {"method": "GET", "headers": {"Authorization": "Bearer original"}},
            "expected": {"statusCode": "200", "body": "unchanged"},
        }

    def test_only_the_fixture_id_changes(self):
        original = copy.deepcopy(self.transaction)
        response = io.BytesIO(b'{"data":[{"id":42,"report_id":"daily-1"}]}')
        with patch("urllib.request.urlopen", return_value=response) as send:
            self.prepare(self.transaction)
        original["fullPath"] = "/api/v1/admin/dmarc-reports/42"
        self.assertEqual(self.transaction, original)
        self.assertEqual(send.call_args.args[0].get_header("Authorization"), "Bearer setup-session")

    def test_refusals_boundaries_and_principals_are_untouched(self):
        for kind, value in (("status", "404"), ("status", "403"), ("status", "422"),
                            ("id", "0"), ("id", "not-an-integer"), ("id", "4294967295"),
                            ("headers", {}), ("method", "POST")):
            transaction = copy.deepcopy(self.transaction)
            if kind == "status":
                transaction["expected"]["statusCode"] = value
            elif kind == "id":
                transaction["fullPath"] = "/api/v1/admin/dmarc-reports/" + value
            else:
                transaction["request"][kind] = value
            original = copy.deepcopy(transaction)
            with patch("urllib.request.urlopen") as send:
                self.prepare(transaction)
            send.assert_not_called()
            self.assertEqual(transaction, original)

    def test_missing_fixture_fails_instead_of_skipping(self):
        with patch("urllib.request.urlopen", return_value=io.BytesIO(b'{"data":[]}')):
            with self.assertRaisesRegex(RuntimeError, "fixture is missing"):
                self.prepare(self.transaction)
