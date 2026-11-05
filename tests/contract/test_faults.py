"""The fault run's verdicts: a fault counts as caught only in the phase it names."""

import http.client
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
import xml.etree.ElementTree as ET
from html import escape
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import faults

HERE = Path(__file__).resolve().parent


def setUpModule():
    # faults.run speaks to GitHub Actions on stdout. Here it judges made-up faults, and each
    # "::error::" it printed became an annotation on a passing Lint job, read as the job's
    # first evidence on every failure that job ever had (#1942).
    silence = patch.object(faults, "print", create=True)
    silence.start()
    unittest.addModuleCleanup(silence.stop)


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


class ShapeAndStatusFaults(unittest.TestCase):
    # What pinned Vertrag reported against each fault's real build, and the
    # transaction it named (tests/contract/README.md, "Fault catalogue").
    REPORTED = {
        "accounts_list_null": (
            "listAccounts",
            "/api/v1/accounts",
            "/api/v1/accounts > List the accounts this one reaches > 200 > application/json",
            "body: /data: got null, want array",
        ),
        "health_storage_ref_leaked": (
            "getHealth",
            "/api/health",
            "/api/health > Health check > 200 > application/json",
            "body: /data: additional properties 'storage_ref' not allowed",
        ),
        "discover_missing_status_ok": (
            "restmailDiscover",
            "/.well-known/restmail",
            "/.well-known/restmail > Discover RESTMAIL delivery for a hosted domain > 404 > application/json",
            "statusCode: Expected status code '404', but got '200'.",
        ),
    }

    def entries(self):
        catalogue = {fault["name"]: fault for fault in faults.load()}
        return [(catalogue[name], *reported) for name, reported in self.REPORTED.items()]

    def write(self, directory, name, message=None):
        problem = f'<failure message="{escape(message, quote=True)}"/>' if message else ""
        return report(directory, [f'<testcase name="{escape(name, quote=True)}">{problem}</testcase>'])

    def test_each_is_catalogued_for_the_examples_phase_with_its_reason(self):
        for fault, operation, path, _, reason in self.entries():
            with self.subTest(fault=fault["name"]):
                self.assertEqual((fault["operation"], fault["path"], fault["phase"]), (operation, path, "examples"))
                self.assertEqual(fault["expected_failure"], reason)
                self.assertNotIn("known_gap", fault)

    def test_vertrags_reported_failure_catches_each_and_its_green_baseline_does_not(self):
        for fault, _, path, transaction, reason in self.entries():
            with self.subTest(fault=fault["name"]), tempfile.TemporaryDirectory() as directory:
                red = self.write(directory, transaction, reason)
                passed, why = faults.judge(red, path, faulted=True, expected_failure=reason)
                self.assertTrue(passed, why)
                self.assertIn(reason, why)
                green = self.write(directory, transaction)
                self.assertTrue(faults.judge(green, path, faulted=False)[0])
                passed, why = faults.judge(green, path, faulted=True, expected_failure=reason)
                self.assertFalse(passed)
                self.assertIn("not caught", why)

    def test_a_failure_for_another_reason_does_not_catch_any_of_them(self):
        for fault, _, path, transaction, reason in self.entries():
            with self.subTest(fault=fault["name"]), tempfile.TemporaryDirectory() as directory:
                red = self.write(directory, transaction, "statusCode: Expected status code '200', but got '429'.")
                passed, why = faults.judge(red, path, faulted=True, expected_failure=reason)
                self.assertFalse(passed, why)
                self.assertIn("wanted the failure", why)

    def test_run_requires_the_catalogued_reason(self):
        fault, _, _, transaction, reason = self.entries()[0]
        for message, expected_code, verdict in [(reason, 0, "caught:"), ("something else", 1, "wanted the failure")]:
            with self.subTest(message=message), tempfile.TemporaryDirectory() as directory, patch.object(
                faults, "OUT", Path(directory)
            ), patch.object(faults, "load", return_value=[fault]), patch.object(
                faults, "restart_api"
            ) as restart, patch.object(faults, "fault_active", return_value=True):
                answers = iter([None, message])

                def run_phase(*_):
                    return self.write(directory, transaction, next(answers))

                with patch.object(faults, "run_phase", side_effect=run_phase):
                    self.assertEqual(faults.run("api"), expected_code)
                self.assertEqual([call.args for call in restart.call_args_list], [(fault["build_tags"],), ("",)])
                self.assertIn(verdict, (Path(directory) / "faults.md").read_text())


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


class SecretScan(unittest.TestCase):
    PATH = "/api/v1/auth/password"
    # The password a refusal echoes, and a value Vertrag redacts: it cannot be judged.
    SENT = "not-the-password"

    def entry(self, answer, path=None, sent=None):
        sent = {"current_password": self.SENT} if sent is None else sent
        return {
            "request": {
                "method": "POST",
                "url": "http://api.test" + (path or self.PATH),
                "postData": {"text": json.dumps(sent)},
                "headers": [],
            },
            "response": {"status": 403, "headers": [], "content": {"text": json.dumps(answer)}},
            "comment": "a transaction",
        }

    def scan(self, directory, entries, keep_as="scan.xml"):
        har = Path(directory) / "examples.har"
        if entries is not None:
            har.write_text(json.dumps({"log": {"entries": entries}}))
        with patch.object(faults, "OUT", Path(directory)):
            return faults.scan_recording(har, "changeOwnPassword", self.PATH, keep_as)

    def judged(self, entries, **options):
        with tempfile.TemporaryDirectory() as directory:
            report_path = self.scan(directory, entries)
            return (*faults.judge(report_path, self.PATH, phase="secret_scan", **options), report_path.read_text())

    def refusal(self, message="Current password is incorrect"):
        return self.entry({"error": {"code": "invalid_password", "message": message}})

    def test_the_catalogue_entry_is_judged_by_the_scan_for_its_reason(self):
        fault = next(fault for fault in faults.load() if fault["phase"] == "secret_scan")
        self.assertEqual((fault["operation"], fault["path"]), ("changeOwnPassword", self.PATH))
        self.assertEqual(fault["expected_failure"], "answers the current_password sent in")
        self.assertNotIn("known_gap", fault)

    def test_an_echo_in_a_free_string_catches_the_fault_without_naming_the_secret(self):
        expected = "answers the current_password sent in"
        passed, why, written = self.judged(
            [self.refusal(f"Current password is incorrect: {self.SENT}")], faulted=True, expected_failure=expected
        )
        self.assertTrue(passed, why)
        self.assertIn("at $.error.message", why)
        self.assertNotIn(self.SENT, why)
        self.assertNotIn(self.SENT, written)

    def test_a_clean_recording_is_green_on_the_baseline_and_catches_nothing(self):
        entries = [self.refusal()]
        self.assertTrue(self.judged(entries, faulted=False)[0])
        passed, why, _ = self.judged(entries, faulted=True)
        self.assertFalse(passed)
        self.assertIn("not caught", why)

    def test_an_echo_on_the_baseline_is_red(self):
        passed, why, _ = self.judged([self.refusal(self.SENT)], faulted=False)
        self.assertFalse(passed)
        self.assertIn("red without a fault", why)

    def test_a_finding_for_another_secret_does_not_catch_the_fault(self):
        issued = self.entry({"data": {"secret": "a-secret-issued-once"}}, sent={"current_password": self.SENT})
        issued["response"]["status"] = 200
        echoed = self.refusal("a-secret-issued-once")
        passed, why, _ = self.judged(
            [issued, echoed], faulted=True, expected_failure="answers the current_password sent in"
        )
        self.assertFalse(passed)
        self.assertIn("wanted the failure", why)

    def test_a_known_gap_fails_once_the_scan_catches_it(self):
        passed, why, _ = self.judged([self.refusal(self.SENT)], faulted=True, known_gap="#658")
        self.assertFalse(passed)
        self.assertIn("now caught, so remove known_gap #658", why)

    def test_a_recording_that_cannot_be_judged_neither_catches_nor_clears(self):
        elsewhere = self.entry({"error": {"code": "e", "message": self.SENT}}, path="/api/v1/other")
        scenarios = {
            "no recording": None,
            "not sent to the route": [elsewhere | {"request": {**elsewhere["request"], "postData": {"text": "{}"}}}],
            "only a redacted password sent": [self.entry({"error": {"code": "e", "message": "m"}}, sent={"current_password": "<redacted>"})],
            "the route absent": [],
            "a finding on another route": [self.refusal(), elsewhere],
        }
        for name, entries in scenarios.items():
            for faulted in (False, True):
                for known_gap in (None, "#658"):
                    with self.subTest(name, faulted=faulted, known_gap=known_gap):
                        passed, why, _ = self.judged(entries, faulted=faulted, known_gap=known_gap)
                        self.assertFalse(passed, why)
                        self.assertIn("invalid scan proof", why)

    def test_a_finding_names_its_route_only_as_the_answering_one(self):
        self.assertTrue(faults.answers_route(f"POST http://api.test{self.PATH} [t] answers the password", self.PATH))
        self.assertFalse(faults.answers_route(f"POST http://api.test/other [t] answers the password sent in POST http://api.test{self.PATH} [t]", self.PATH))
        self.assertFalse(faults.answers_route(f"the audit log's {self.PATH} entry holds it", self.PATH))

    def test_a_case_that_is_not_the_scans_own_is_refused(self):
        for contents in [
            [],
            ['<testcase name="/api/health &gt; secret_scan"/>'],
            ['<testcase name="/api/v1/auth/password &gt; secret_scan"><failure type="body_cap"/></testcase>'],
            ['<testcase name="/api/v1/auth/password &gt; secret_scan"><skipped/></testcase>'],
        ]:
            with self.subTest(contents=contents), tempfile.TemporaryDirectory() as directory:
                passed, why = faults.judge(report(directory, contents), self.PATH, faulted=True, phase="secret_scan")
                self.assertFalse(passed, why)
                self.assertIn("invalid scan proof", why)

    def test_run_scans_each_run_of_the_gate_and_restores_the_api(self):
        fault = next(fault for fault in faults.load() if fault["phase"] == "secret_scan")
        for answers, expected_code, verdict in [
            (["Current password is incorrect", self.SENT], 0, "caught:"),
            (["Current password is incorrect", "Current password is incorrect"], 1, "not caught"),
            ([self.SENT, self.SENT], 1, "red without a fault"),
        ]:
            with self.subTest(answers=answers), tempfile.TemporaryDirectory() as directory, patch.object(
                faults, "OUT", Path(directory)
            ), patch.object(faults, "load", return_value=[fault]), patch.object(
                faults, "restart_api"
            ) as restart, patch.object(faults, "fault_active", return_value=True):
                recordings = iter(answers)

                def gate(command, **_):
                    self.assertEqual(command, ["tests/contract/run.sh", "gate", "--operation-id", "changeOwnPassword"])
                    har = {"log": {"entries": [self.refusal(next(recordings))]}}
                    (Path(directory) / "examples.har").write_text(json.dumps(har))
                    return SimpleNamespace(returncode=0)

                with patch.object(faults.subprocess, "run", side_effect=gate):
                    self.assertEqual(faults.run("api"), expected_code)
                self.assertIn(verdict, (Path(directory) / "faults.md").read_text())
                if verdict != "red without a fault":
                    self.assertEqual([call.args for call in restart.call_args_list], [(fault["build_tags"],), ("",)])
                else:
                    restart.assert_not_called()


class BrowserSession(unittest.TestCase):
    OPERATION = "createAccount"
    PATH = "/api/v1/accounts"
    REFUSED = [{"operation": "createAccount", "principal": variant, "status": 403} for variant in faults.CSRF_VARIANTS]
    BEFORE = [{"operation": "login", "principal": "cookie", "status": 200}]

    def verdict(self, directory, output, receipt):
        receipt_path = Path(directory) / "browser-sessions.json"
        if receipt is not None:
            receipt_path.write_text(json.dumps(receipt))
        with patch.object(faults, "OUT", Path(directory)):
            return faults.session_verdict(output, receipt_path, self.OPERATION, self.PATH, "sessions.xml")

    def judged(self, output, receipt, **options):
        with tempfile.TemporaryDirectory() as directory:
            report_path = self.verdict(directory, output, receipt)
            return faults.judge(report_path, self.PATH, phase="browser_session", **options)

    def passing(self):
        checks = self.BEFORE + self.REFUSED
        return f"browser session contract: {len(checks)} checks passed\n", {"passed": True, "checks": checks}

    def skipped(self, variant="cookie-without-csrf", status=400, operation=OPERATION):
        line = f"browser session contract: {operation} ({variant}): expected 403 csrf_failed, got status {status} or a different error code"
        checks = self.BEFORE + [{"operation": operation, "principal": variant, "status": status}]
        return "go: downloading\n" + line + "\n", {"passed": False, "checks": checks}

    def test_the_catalogue_entry_is_judged_by_the_checker_for_its_reason(self):
        fault = next(fault for fault in faults.load() if fault["phase"] == "browser_session")
        self.assertEqual((fault["operation"], fault["path"]), (self.OPERATION, self.PATH))
        self.assertEqual(fault["expected_failure"], "expected 403 csrf_failed")
        self.assertNotIn("known_gap", fault)
        self.assertEqual(faults.PHASES["browser_session"], ("gate", "browser-sessions.json"))

    def test_a_csrf_refusal_the_operation_skipped_catches_the_fault(self):
        for variant in faults.CSRF_VARIANTS:
            with self.subTest(variant=variant):
                passed, why = self.judged(*self.skipped(variant), faulted=True, expected_failure="expected 403 csrf_failed")
                self.assertTrue(passed, why)
                self.assertIn(f"caught: {self.OPERATION} ({variant}): expected 403 csrf_failed, got status 400", why)

    def test_a_passing_checker_is_green_on_the_baseline_and_catches_nothing(self):
        self.assertEqual(self.judged(*self.passing(), faulted=False), (True, "green"))
        passed, why = self.judged(*self.passing(), faulted=True)
        self.assertFalse(passed)
        self.assertIn("not caught", why)

    def test_a_skipped_csrf_refusal_on_the_baseline_is_red(self):
        passed, why = self.judged(*self.skipped(), faulted=False)
        self.assertFalse(passed)
        self.assertIn("red without a fault", why)

    def test_a_known_gap_fails_once_the_checker_catches_it(self):
        passed, why = self.judged(*self.skipped(), faulted=True, known_gap="#654")
        self.assertFalse(passed)
        self.assertIn("now caught, so remove known_gap #654", why)

    def test_a_checker_failing_for_another_reason_neither_catches_nor_clears(self):
        output, receipt = self.skipped()
        scenarios = {
            "another operation's CSRF refusal": self.skipped(operation="saveDraft"),
            "another assertion": (
                "browser session contract: refresh (cookie-with-csrf): expected 200 , got status 401 or a different error code\n",
                {"passed": False, "checks": self.BEFORE + [{"operation": "refresh", "principal": "cookie-with-csrf", "status": 401}]},
            ),
            "a safe read": (
                f"browser session contract: {self.OPERATION} (cookie-without-csrf): safe response error code differs from control (status 404)\n",
                receipt,
            ),
            "a valid token refused": self.skipped(variant="cookie-with-csrf"),
            "a run that could not start": ("browser session contract: open .cache/contract/openapi.json: no such file\n", {"passed": False, "checks": []}),
            "no checker verdict": ("run.sh: description.py failed\n", None),
            "no receipt": (output, None),
            "a receipt from another check": (output, {"passed": False, "checks": self.REFUSED}),
            "two verdicts": (output + output, receipt),
            "a pass without the operation's refusals": ("browser session contract: 1 checks passed\n", {"passed": True, "checks": self.BEFORE}),
            "a pass the output contradicts": (output, {"passed": True, "checks": self.BEFORE + self.REFUSED}),
        }
        for name, (out, rec) in scenarios.items():
            for faulted in (False, True):
                for known_gap in (None, "#654"):
                    with self.subTest(name, faulted=faulted, known_gap=known_gap):
                        passed, why = self.judged(out, rec, faulted=faulted, known_gap=known_gap)
                        self.assertFalse(passed, why)
                        self.assertIn("invalid session proof", why)

    def test_run_judges_the_checker_of_each_gate_run_and_restores_the_api(self):
        fault = next(fault for fault in faults.load() if fault["phase"] == "browser_session")
        for runs, expected_code, verdict in [
            ([self.passing(), self.skipped()], 0, "caught:"),
            ([self.passing(), self.passing()], 1, "not caught"),
            ([self.skipped(), self.skipped()], 1, "red without a fault"),
        ]:
            with self.subTest(verdict=verdict), tempfile.TemporaryDirectory() as directory, patch.object(
                faults, "OUT", Path(directory)
            ), patch.object(faults, "load", return_value=[fault]), patch.object(
                faults, "restart_api"
            ) as restart, patch.object(faults, "fault_active", return_value=True):
                answers = iter(runs)

                def gate(command, **options):
                    self.assertEqual(command, ["tests/contract/run.sh", "gate", "--operation-id", self.OPERATION])
                    self.assertEqual(options.get("stderr"), faults.subprocess.STDOUT)
                    output, receipt = next(answers)
                    (Path(directory) / "browser-sessions.json").write_text(json.dumps(receipt))
                    return SimpleNamespace(returncode=0 if receipt["passed"] else 1, stdout=output)

                with patch.object(faults.subprocess, "run", side_effect=gate), patch("builtins.print"):
                    self.assertEqual(faults.run("api"), expected_code)
                self.assertIn(verdict, (Path(directory) / "faults.md").read_text())
                if verdict != "red without a fault":
                    self.assertEqual([call.args for call in restart.call_args_list], [(fault["build_tags"],), ("",)])
                else:
                    restart.assert_not_called()


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


class Output(unittest.TestCase):
    def test_these_tests_send_github_actions_no_command(self):
        others = [
            f"test_faults.{name}"
            for name, value in globals().items()
            if isinstance(value, type) and issubclass(value, unittest.TestCase) and value is not Output
        ]
        result = subprocess.run(
            [sys.executable, "-B", "-m", "unittest", *others],
            cwd=HERE,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        commands = [line for line in result.stdout.splitlines() if line.startswith("::")]
        self.assertEqual(commands, [])


if __name__ == "__main__":
    unittest.main()
