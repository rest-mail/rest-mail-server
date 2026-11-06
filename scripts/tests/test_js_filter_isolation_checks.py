"""Daemon-free regressions for the real-instance live check's evidence."""

import contextlib
import io
import re
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch

import yaml

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


class IsolationJobTests(unittest.TestCase):
    """The proof left the topology job (#1979); what it runs and fails must not move."""

    def setUp(self):
        workflows = Path(__file__).resolve().parents[2] / ".github/workflows"
        self.source = (workflows / "e2e-suite.yml").read_text()
        suite = yaml.safe_load(self.source)
        self.jobs = suite["jobs"]
        self.job = self.jobs["js-filter-isolation"]
        self.e2e = yaml.safe_load((workflows / "e2e.yml").read_text())["jobs"]

    def step(self, job, name):
        return next(s for s in job["steps"] if s.get("name") == name)

    def test_it_runs_with_the_gate_and_only_in_its_own_job(self):
        self.assertEqual(self.job["if"], "inputs.contract-phases == 'gate'")
        self.assertNotIn(
            "test_js_filter_isolation_live",
            yaml.safe_dump(self.jobs["suite"]),
        )

    def test_it_runs_the_whole_test_on_two_instances_and_a_skip_fails(self):
        step = next(
            s
            for s in self.job["steps"]
            if "test_js_filter_isolation_live" in s.get("run", "")
        )
        self.assertNotIn("continue-on-error", step)
        self.assertEqual(
            step["env"]["RESTMAIL_LIVE_JS_FILTER_CONFIGS"],
            "isolation-a.test isolation-b.test",
        )
        self.assertIn("python3 -m unittest scripts/tests/test_js_filter_isolation_live.py", step["run"])
        self.assertNotIn(" -k ", step["run"])
        self.assertIn("grep -q 'skipped'", step["run"])
        for task in ("instance:certs:issue", "instance:mtls:issue", "instance:dns:register"):
            self.assertIn(task, step["run"])

    def test_the_required_check_waits_for_it(self):
        # A reusable workflow's result covers every job in it, and the required
        # `E2E (full topology)` gate reads that result.
        self.assertIn("e2e-run", self.e2e["e2e"]["needs"])
        self.assertTrue(self.e2e["e2e-run"]["uses"].endswith("e2e-suite.yml"))
        self.assertIn("${{ needs.e2e-run.result }}", yaml.safe_dump(self.e2e["e2e"]))

    def test_its_setup_is_the_topology_jobs_setup(self):
        suite = self.jobs["suite"]
        for name in (
            "Checkout",
            "Retry transient image pull failures",
            "Set up Go",
            "Install chore (the newest release, attested)",
            "Populate Go module cache",
            "Route git@github over HTTPS",
        ):
            with self.subTest(step=name):
                self.assertEqual(self.step(self.job, name), self.step(suite, name))
        name = "Clone testbed + reference-mailserver substrate (pinned)"
        self.assertEqual(self.step(self.job, name)["run"], self.step(suite, name)["run"])
        self.assertNotIn("TESTBED_REF", self.source)


if __name__ == "__main__":
    unittest.main()
