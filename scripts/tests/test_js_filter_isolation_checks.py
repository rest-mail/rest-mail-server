"""Daemon-free regressions for the real-instance live check's evidence."""

import contextlib
import io
import subprocess
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

    def wait(self, states, probes, timeout=60):
        """Run the wait with scripted container states and probe results."""
        replies = [
            subprocess.CompletedProcess([], 0, stdout=state, stderr="")
            for state in states
        ]
        logs = subprocess.CompletedProcess([], 0, stdout="filter log line\n", stderr="")
        with (
            patch.object(live, "run", side_effect=replies + [logs]) as run,
            patch.object(self.check, "probe", side_effect=probes) as probe,
            patch.object(live, "FILTER_READY_TIMEOUT", timeout),
            patch.object(live.time, "sleep"),
        ):
            try:
                self.check.wait_for_filter(CONFIG)
            except AssertionError as failure:
                return str(failure), run, probe
        return None, run, probe

    def test_wait_lasts_until_the_filter_accepts_connections(self):
        failure, _, probe = self.wait(
            ["running restarts=0\n"] * 3,
            [(1, "ECONNREFUSED"), (1, "ECONNREFUSED"), (0, "connected")],
        )
        self.assertIsNone(failure)
        self.assertEqual(probe.call_count, 3)

    def test_filter_that_never_listens_fails_and_is_named(self):
        failure, _, probe = self.wait(
            ["running restarts=0\n"], [(1, "ECONNREFUSED")], timeout=0
        )
        self.assertIn("js-filter service isolation-a-js-filter", failure)
        self.assertIn("still refused connections", failure)
        self.assertIn("filter log line", failure)
        self.assertEqual(probe.call_count, 1)

    def test_crash_is_not_waited_out(self):
        for state in ("exited restarts=0\n", "running restarts=1\n"):
            with self.subTest(state):
                failure, _, probe = self.wait([state], [(0, "connected")])
                self.assertIn(state.strip(), failure)
                self.assertIn("isolation-a-js-filter", failure)
                self.assertEqual(probe.call_count, 0)

    def test_missing_probe_executable_is_not_waited_out(self):
        failure, _, probe = self.wait(["running restarts=0\n"], [(127, "node missing")])
        self.assertIn("node missing", failure)
        self.assertEqual(probe.call_count, 1)

    def test_api_check_waits_before_it_calls_the_filter(self):
        order = []
        with (
            patch.object(
                self.check,
                "wait_for_filter",
                side_effect=lambda _: order.append("wait"),
            ),
            patch.object(self.check, "filter_address", return_value="172.20.0.2"),
            patch.object(
                live,
                "run",
                return_value=subprocess.CompletedProcess(
                    [], 0, stdout="http://172.20.0.2:3100\n", stderr=""
                ),
            ),
            patch.object(
                self.check,
                "call_filter",
                side_effect=lambda *_, **__: order.append("call") or "refused",
            ),
            self.assertRaises(AssertionError),
        ):
            self.check.check_api_to_filter(CONFIG)
        self.assertEqual(order, ["wait", "call"])


if __name__ == "__main__":
    unittest.main()
