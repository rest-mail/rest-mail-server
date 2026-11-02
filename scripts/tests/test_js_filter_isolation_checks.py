"""Daemon-free regressions for the real-instance live check's evidence."""

import contextlib
import io
import unittest
from unittest.mock import patch

from scripts.tests import test_js_filter_isolation_live as live

CONFIG = {
    "RESTMAIL_PROJECT": "isolation-a",
    "RESTMAIL_HOSTNAME": "isolation-a.test",
    "RESTMAIL_TESTBED_DNS_IP": "10.99.0.10",
    "RESTMAIL_POSTGRES_IP": "10.99.0.50",
    "RESTMAIL_API_IP": "10.99.0.51",
}


class RealInstanceEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.check = live.JSFilterIsolationLiveTest()

    def test_calls_both_endpoints_through_the_go_api(self):
        replies = [
            {"data": {"access_token": "private-session-token"}},
            {"data": [{"id": 7, "name": "isolation-a.test"}]},
            {"data": {"id": 19}},
            {"valid": True},
            {"result": {"type": "action", "action": "continue"}},
        ]
        output = io.StringIO()
        with (
            patch.object(self.check, "api_request", side_effect=replies) as request,
            contextlib.redirect_stderr(output),
        ):
            self.check.check_go_api(CONFIG)
        calls = request.call_args_list
        self.assertEqual(calls[0].args[1], "/auth/login")
        self.assertEqual(calls[0].kwargs["body"]["client"], "native")
        self.assertEqual(calls[2].kwargs["body"]["domain_id"], 7)
        self.assertEqual(calls[3].args[1], "/admin/custom-filters/validate")
        self.assertEqual(calls[4].args[1], "/admin/custom-filters/19/test")
        for call in calls[1:]:
            self.assertEqual(call.kwargs["token"], "private-session-token")
        self.assertNotIn("private-session-token", output.getvalue())

    def test_go_api_validation_failure_is_not_a_pass(self):
        replies = [
            {"data": {"access_token": "private-session-token"}},
            {"data": [{"id": 7, "name": "isolation-a.test"}]},
            {"data": {"id": 19}},
            {"valid": False},
        ]
        with (
            patch.object(self.check, "api_request", side_effect=replies),
            self.assertRaises(AssertionError),
        ):
            self.check.check_go_api(CONFIG)

    def test_missing_probe_executable_is_not_denied_egress(self):
        with (
            patch.object(self.check, "filter_address", return_value="172.20.0.2"),
            patch.object(
                self.check,
                "probe",
                side_effect=[(0, "connected"), (127, "node missing")],
            ),
            patch.object(self.check, "positive_control"),
            self.assertRaises(AssertionError),
        ):
            self.check.check_egress(CONFIG, CONFIG)


if __name__ == "__main__":
    unittest.main()
