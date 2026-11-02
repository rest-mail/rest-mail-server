"""The fault run's verdicts: a fault counts as caught only in the phase it names."""

import http.client
import json
import os
import tempfile
import threading
import unittest
import xml.etree.ElementTree as ET
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from unittest.mock import patch

import faults


def report(directory, cases):
    path = Path(directory) / "examples.xml"
    path.write_text("<testsuites><testsuite>" + "".join(cases) + "</testsuite></testsuites>")
    return path


class Catalogue(unittest.TestCase):
    def test_every_fault_names_a_judged_phase_and_its_tags(self):
        for fault in faults.load():
            self.assertIn(fault["phase"], faults.PHASES)
            self.assertEqual(fault["build_tags"], "faultinject,fault_" + fault["name"])
            self.assertTrue(fault["path"].startswith("/"), fault["name"])

    def test_tags_join_every_fault_once(self):
        tags = faults.all_tags(
            [{"build_tags": "faultinject,fault_a"}, {"build_tags": "faultinject,fault_b"}]
        )
        self.assertEqual(tags, "faultinject,fault_a,fault_b")

    def test_an_unknown_phase_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "faults.yml"
            path.write_text(
                "faults:\n  - {name: a, build_tags: 'faultinject,fault_a', operation: getHealth, path: /api/health, phase: fuzz}\n"
            )
            with self.assertRaises(ValueError):
                faults.load(path)

    def test_limit_phases_are_login_only(self):
        limits = [fault for fault in faults.load() if fault["phase"] in faults.LIMIT_PHASES]
        self.assertEqual({fault["phase"] for fault in limits}, faults.LIMIT_PHASES)
        for fault in limits:
            self.assertEqual((fault["operation"], fault["path"]), ("login", faults.LOGIN))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "faults.yml"
            path.write_text(
                "faults:\n  - {name: a, operation: getHealth, path: /api/health, phase: body_cap}\n"
            )
            with self.assertRaisesRegex(ValueError, "only exercise POST"):
                faults.load(path)


class Judge(unittest.TestCase):
    def test_a_failed_transaction_of_the_operation_catches_the_fault(self):
        with tempfile.TemporaryDirectory() as directory:
            path = report(
                directory,
                ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"><failure message="body: data is not an object"/></testcase>'],
            )
            passed, why = faults.judge(path, "/api/health", faulted=True)
            self.assertTrue(passed, why)

    def test_a_failure_of_another_operation_does_not(self):
        with tempfile.TemporaryDirectory() as directory:
            path = report(
                directory,
                [
                    '<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"/>',
                    '<testcase name="/api/v1/auth/login &gt; Sign in &gt; 200 &gt; application/json"><failure message="x"/></testcase>',
                ],
            )
            passed, why = faults.judge(path, "/api/health", faulted=True)
            self.assertFalse(passed)
            self.assertIn("not caught", why)

    def test_a_phase_that_never_ran_catches_nothing(self):
        with tempfile.TemporaryDirectory() as directory:
            passed, why = faults.judge(Path(directory) / "examples.xml", "/api/health", faulted=True)
            self.assertFalse(passed)
            self.assertIn("did not run", why)

    def test_the_baseline_must_be_green_for_the_operation(self):
        with tempfile.TemporaryDirectory() as directory:
            green = report(directory, ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"/>'])
            self.assertTrue(faults.judge(green, "/api/health", faulted=False)[0])
            red = report(directory, ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"><error message="e"/></testcase>'])
            passed, why = faults.judge(red, "/api/health", faulted=False)
            self.assertFalse(passed)
            self.assertIn("red without a fault", why)

    def test_a_baseline_that_sent_nothing_is_not_green(self):
        with tempfile.TemporaryDirectory() as directory:
            passed, why = faults.judge(report(directory, []), "/api/health", faulted=False)
            self.assertFalse(passed)
            self.assertIn("sent no transaction", why)

    def test_a_known_gap_passes_uncaught_and_fails_once_caught(self):
        with tempfile.TemporaryDirectory() as directory:
            green = report(directory, ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"/>'])
            passed, why = faults.judge(green, "/api/health", faulted=True, known_gap="#1")
            self.assertTrue(passed, why)
            red = report(directory, ['<testcase name="/api/health &gt; Health check &gt; 200 &gt; application/json"><failure/></testcase>'])
            passed, why = faults.judge(red, "/api/health", faulted=True, known_gap="#1")
            self.assertFalse(passed)
            self.assertIn("remove known_gap", why)


class LimitProbes(unittest.TestCase):
    def run_probe(self, directory, phase, responses):
        with patch.object(faults, "OUT", Path(directory)), patch.dict(
            os.environ, {"CONTRACT_ENDPOINT": "http://limit.test:8080"}
        ), patch.object(faults, "login_response", side_effect=responses) as request:
            path = faults.run_phase(phase, "login", f"{phase}.xml")
        return path, request

    def test_healthy_baselines_exercise_each_real_limit(self):
        for phase, responses in [
            ("body_cap", [(413, "request_too_large", "")]),
            ("auth_throttle", [(401, "unauthorized", ""), (429, "rate_limited", "1")]),
        ]:
            with self.subTest(phase=phase), tempfile.TemporaryDirectory() as directory:
                path, request = self.run_probe(directory, phase, responses)
                passed, why = faults.judge(path, faults.LOGIN, faulted=False, phase=phase)
                self.assertTrue(passed, why)
                case = ET.parse(path).find("testcase")
                self.assertIn("POST " + faults.LOGIN, case.findtext("system-out"))
                self.assertEqual(request.call_count, len(responses))
                if phase == "body_cap":
                    self.assertEqual(request.call_args.kwargs, {"body_bytes": faults.BODY_BYTES})
                else:
                    self.assertEqual(request.call_args.args[1]["password"], "not the password")

    def test_only_the_expected_missing_guard_catches_each_fault(self):
        for phase, responses in [
            ("body_cap", [(422, "validation_failed", "")]),
            ("auth_throttle", [(401, "unauthorized", "")] * faults.THROTTLE_ATTEMPTS),
        ]:
            with self.subTest(phase=phase), tempfile.TemporaryDirectory() as directory:
                path, request = self.run_probe(directory, phase, responses)
                passed, why = faults.judge(path, faults.LOGIN, faulted=True, phase=phase)
                self.assertTrue(passed, why)
                self.assertIn("caught:", why)
                self.assertFalse(faults.judge(path, faults.LOGIN, faulted=False, phase=phase)[0])
                passed, why = faults.judge(path, faults.LOGIN, faulted=True, phase=phase, known_gap="#1493")
                self.assertFalse(passed)
                self.assertIn("now caught, so remove known_gap #1493", why)
                self.assertEqual(request.call_count, len(responses))

    def test_wrong_reasons_are_errors_not_caught_faults_or_known_gaps(self):
        scenarios = [
            ("body_cap", [(500, "internal_error", "")]),
            ("body_cap", [(429, "rate_limited", "1")]),
            ("body_cap", [(422, "unrelated", "")]),
            ("body_cap", [TimeoutError("no response")]),
            ("body_cap", [http.client.RemoteDisconnected("connection lost")]),
            ("body_cap", [ValueError("not JSON")]),
            ("auth_throttle", [(500, "internal_error", "")]),
            ("auth_throttle", [(401, "bootstrap_expired", "")]),
            ("auth_throttle", [(200, "ok", "")]),
            ("auth_throttle", [(429, "rate_limited", "1")]),
            ("auth_throttle", [(401, "unauthorized", ""), (429, "other_limit", "1")]),
        ]
        for retry in ["", "0", "-1", "later", "1.5", "١"]:
            scenarios.append(("auth_throttle", [(401, "unauthorized", ""), (429, "rate_limited", retry)]))
        for phase, responses in scenarios:
            with self.subTest(phase=phase, responses=responses), tempfile.TemporaryDirectory() as directory:
                path, _ = self.run_probe(directory, phase, responses)
                for faulted in (False, True):
                    for known_gap in (None, "#1493"):
                        passed, why = faults.judge(path, faults.LOGIN, faulted=faulted, phase=phase, known_gap=known_gap)
                        self.assertFalse(passed, why)
                        self.assertIn("invalid limit proof", why)

    def test_a_healthy_limit_does_not_catch_a_fault(self):
        with tempfile.TemporaryDirectory() as directory:
            path, _ = self.run_probe(directory, "body_cap", [(413, "request_too_large", "")])
            passed, why = faults.judge(path, faults.LOGIN, faulted=True, phase="body_cap")
            self.assertFalse(passed)
            self.assertIn("not caught", why)

    def test_wrong_operation_reason_skips_and_empty_reports_are_rejected(self):
        cases = [
            [],
            ['<testcase classname="login" name="/api/health &gt; body_cap"><failure type="body_cap"/></testcase>'],
            ['<testcase classname="other" name="/api/v1/auth/login &gt; body_cap"><failure type="body_cap"/></testcase>'],
            ['<testcase classname="login" name="/api/v1/auth/login &gt; auth_throttle"><failure type="auth_throttle"/></testcase>'],
            ['<testcase classname="login" name="/api/v1/auth/login &gt; body_cap"><failure type="auth_throttle"/></testcase>'],
            ['<testcase classname="login" name="/api/v1/auth/login &gt; body_cap"><skipped/></testcase>'],
            ['<testcase classname="login" name="/api/v1/auth/login &gt; body_cap"><error/><failure type="body_cap"/></testcase>'],
        ]
        for contents in cases:
            with self.subTest(contents=contents), tempfile.TemporaryDirectory() as directory:
                path = report(directory, contents)
                for faulted in (False, True):
                    passed, why = faults.judge(path, faults.LOGIN, faulted=faulted, phase="body_cap")
                    self.assertFalse(passed, why)

    def test_transport_streams_the_declared_body_and_reads_early_rejections(self):
        observed = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers["Content-Length"])
                received = self.rfile.read(length) if self.server.accept_body else b""
                observed.append((self.path, length, received))
                status, code = (422, "validation_failed") if self.server.accept_body else (413, "request_too_large")
                body = json.dumps({"error": {"code": code}}).encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        with HTTPServer(("127.0.0.1", 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                endpoint = f"http://127.0.0.1:{server.server_port}"
                for accept in (False, True):
                    server.accept_body = accept
                    status, _, _ = faults.login_response(endpoint, {}, body_bytes=2 << 20)
                    self.assertEqual(status, 422 if accept else 413)
                self.assertEqual(observed[0], (faults.LOGIN, 2 << 20, b""))
                self.assertEqual(observed[1][:2], (faults.LOGIN, 2 << 20))
                self.assertEqual(len(observed[1][2]), 2 << 20)
                self.assertEqual(json.loads(observed[1][2]), {})
            finally:
                server.shutdown()
                thread.join()


class LimitRun(unittest.TestCase):
    def test_baseline_is_required_before_any_fault_build(self):
        fault = next(fault for fault in faults.load() if fault["phase"] == "body_cap")
        with tempfile.TemporaryDirectory() as directory, patch.object(faults, "OUT", Path(directory)), patch.object(
            faults, "load", return_value=[fault]
        ), patch.object(faults, "restart_api") as restart, patch.object(
            faults, "fault_active"
        ) as active, patch.object(faults, "login_response", return_value=(500, "internal_error", "")):
            self.assertEqual(faults.run("api"), 1)
            restart.assert_called_once_with("")
            active.assert_not_called()

    def test_run_judges_the_limit_phase_after_fresh_baseline_and_restores_api(self):
        for known_gap, response, expected_code, verdict in [
            (None, (422, "validation_failed", ""), 0, "caught:"),
            ("#1493", (422, "validation_failed", ""), 1, "now caught"),
            ("#1493", (500, "internal_error", ""), 1, "invalid limit proof"),
        ]:
            fault = dict(next(fault for fault in faults.load() if fault["phase"] == "body_cap"))
            fault["known_gap"] = known_gap
            with self.subTest(known_gap=known_gap, response=response), tempfile.TemporaryDirectory() as directory, patch.object(
                faults, "OUT", Path(directory)
            ), patch.object(faults, "load", return_value=[fault]), patch.object(
                faults, "restart_api"
            ) as restart, patch.object(faults, "fault_active", return_value=True), patch.object(
                faults, "login_response", side_effect=[(413, "request_too_large", ""), response]
            ):
                self.assertEqual(faults.run("api"), expected_code)
                self.assertEqual([call.args for call in restart.call_args_list], [("",), (fault["build_tags"],), ("",)])
                self.assertIn(verdict, (Path(directory) / "faults.md").read_text())


if __name__ == "__main__":
    unittest.main()
