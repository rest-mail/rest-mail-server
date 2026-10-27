"""Fail-closed audit retrieval and credential handling for #1165/#658/#673."""

import contextlib
import io
import json
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

import secret_scan

ACCESS = "fixture-audit-access-token"
REFRESH = "fixture-audit-refresh-token"
PASSWORD = "fixture-audit-password"


class AuditReadTest(unittest.TestCase):
    def setUp(self):
        self.requests, self.foreign = [], []
        self.login = {"data": {"access_token": ACCESS, "refresh_token": REFRESH}}
        self.pages = [{"data": [], "pagination": {"has_more": False}}]
        self.redirect, self.login_redirect = None, False
        self.cookies = []
        self.http_status = 200
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_POST(self):
                self.do_GET()

            def do_GET(self):
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                if self.server.foreign:
                    owner.foreign.append(self.headers.get("Authorization"))
                    value = owner.login if self.path == "/login" else owner.pages[0]
                else:
                    owner.requests.append(
                        (self.path, self.headers.get("Authorization"))
                    )
                    login = self.path == "/login"
                    if owner.redirect and login == owner.login_redirect:
                        self.send_response(302)
                        self.send_header(
                            "Location",
                            owner.redirect + ("/login" if login else "/audit"),
                        )
                        self.end_headers()
                        return
                    value = owner.login if login else owner.pages.pop(0)
                body = value if isinstance(value, bytes) else json.dumps(value).encode()
                self.send_response(owner.http_status)
                if self.path == "/login":
                    for cookie in owner.cookies:
                        self.send_header("Set-Cookie", cookie)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        self.servers = []
        for foreign in [False, True]:
            server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            server.foreign = foreign
            thread = threading.Thread(
                target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True
            )
            thread.start()
            self.servers.append((server, thread))
        self.temp = tempfile.TemporaryDirectory()
        self.config = Path(self.temp.name) / "vertrag.yml"
        self.config.write_text(
            json.dumps(
                {
                    "endpoint": f"http://127.0.0.1:{self.servers[0][0].server_port}",
                    "auth": {
                        "login": {"path": "/login", "body": {"password": PASSWORD}}
                    },
                }
            )
        )
        self.har = Path(self.temp.name) / "run.har"
        self.har.write_text('{"log":{"entries":[]}}')

    def tearDown(self):
        for server, thread in self.servers:
            server.shutdown()
            thread.join(timeout=2)
            server.server_close()
        self.temp.cleanup()

    def run_cli(self):
        stdout, stderr = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            result = secret_scan.main([str(self.har), "--audit-from", str(self.config)])
        return result, stdout.getvalue(), stderr.getvalue()

    def test_complete_empty_audit_at_the_page_limit_is_clean(self):
        with patch.object(secret_scan, "MAX_AUDIT_PAGES", 1):
            result, out, err = self.run_cli()
        self.assertEqual(result, 0)
        self.assertIn("0 audit entries, 0 findings", out)
        self.assertEqual(err, "")

    def test_a_bad_later_page_never_reports_a_partial_audit_clean(self):
        self.pages = [
            {
                "data": [{"action": "first"}],
                "pagination": {"has_more": True, "cursor": "12"},
            },
            ("invalid JSON " + ACCESS).encode(),
        ]
        result, out, err = self.run_cli()
        self.assertEqual(result, 2)
        self.assertNotIn("0 findings", out)
        self.assertNotIn(ACCESS, out + err)

    def test_incomplete_pagination_cannot_report_a_clean_scan(self):
        self.pages = [{"data": [], "pagination": {"has_more": True}}]
        result, out, err = self.run_cli()
        self.assertEqual(result, 2)
        self.assertNotIn("0 findings", out)
        self.assertIn("could not be verified", err)

    def test_audit_redirect_never_forwards_authorization(self):
        self.redirect = f"http://127.0.0.1:{self.servers[1][0].server_port}"
        result, out, err = self.run_cli()
        self.assertEqual(result, 2)
        self.assertEqual(self.foreign, [])
        self.assertNotIn(ACCESS, out + err)

    def test_login_redirect_is_refused(self):
        self.redirect = f"http://127.0.0.1:{self.servers[1][0].server_port}"
        self.login_redirect = True
        result, out, err = self.run_cli()
        self.assertEqual(result, 2)
        self.assertEqual(self.foreign, [])
        self.assertNotIn(PASSWORD, out + err)

    def test_the_audit_readers_own_login_tokens_are_scanned_and_redacted(self):
        self.pages = [
            {
                "data": [{"action": "login", "note": ACCESS + REFRESH}],
                "pagination": {"has_more": False},
            }
        ]
        result, out, err = self.run_cli()
        self.assertEqual(result, 1)
        self.assertIn("access_token", out)
        self.assertIn("refresh_token", out)
        self.assertNotIn(ACCESS, out + err)
        self.assertNotIn(REFRESH, out + err)

    def test_login_session_cookies_are_also_known_secrets(self):
        value = "fixture-cookie-only-refresh"
        self.cookies = ["restmail_refresh=" + value + "; HttpOnly"]
        self.pages = [
            {
                "data": [{"action": "login", "note": value}],
                "pagination": {"has_more": False},
            }
        ]
        result, out, err = self.run_cli()
        self.assertEqual(result, 1)
        self.assertIn("refresh_token", out)
        self.assertNotIn(value, out + err)

    def test_http_error_and_total_limit_fail_closed(self):
        self.http_status = 503
        self.login = {"note": ACCESS + PASSWORD}
        result, out, err = self.run_cli()
        self.assertEqual(result, 2)
        for secret in [ACCESS, PASSWORD]:
            self.assertNotIn(secret, out + err)
        self.http_status = 200
        self.login = {"data": {"access_token": ACCESS}}
        self.pages = [
            {"data": [{"action": "login"}], "pagination": {"has_more": False}}
        ]
        with patch.object(secret_scan, "MAX_AUDIT_TOTAL_BYTES", 1):
            result, out, _ = self.run_cli()
        self.assertEqual(result, 2)
        self.assertNotIn("0 findings", out)

    def test_complete_pages_and_cursor_query_encoding(self):
        cursor = "12&action=fixture+雪"
        self.pages = [
            {
                "data": [{"action": "first"}],
                "pagination": {"has_more": True, "cursor": cursor},
            },
            {"data": [{"action": "last"}], "pagination": {"has_more": False}},
        ]
        records = secret_scan.read_audit(self.config)
        self.assertEqual([json.loads(r)["action"] for r in records], ["first", "last"])
        path, authorization = self.requests[-1]
        self.assertEqual(
            parse_qs(urlsplit(path).query), {"limit": ["200"], "before_seq": [cursor]}
        )
        self.assertEqual(authorization, "Bearer " + ACCESS)

    def test_invalid_pages_fail_closed_without_secret_output(self):
        invalid = [
            {"data": []},
            {"data": [], "pagination": {"has_more": True, "cursor": "12"}},
            None,
            [],
            {},
            {"data": {}},
            {"data": [ACCESS], "pagination": {"has_more": False}},
            {"data": [], "pagination": None},
            {"data": [], "pagination": {"has_more": 0}},
            {"data": [], "pagination": {"has_more": "false"}},
            {"data": [{"action": "x"}], "pagination": {"has_more": True, "cursor": ""}},
            {"data": [{"action": "x"}], "pagination": {"has_more": True, "cursor": 12}},
            ("not json " + ACCESS).encode(),
        ]
        for page in invalid:
            with self.subTest(page=page):
                self.pages = [page]
                result, out, err = self.run_cli()
                self.assertEqual(result, 2)
                self.assertNotIn("0 findings", out)
                self.assertNotIn(ACCESS, out + err)

    def test_cycles_and_page_limit_cannot_report_clean(self):
        for cursors in [["1", "1"], ["1", "2"], ["1", "2", "1"]]:
            with self.subTest(cursors=cursors):
                self.pages = [
                    {
                        "data": [{"action": "x"}],
                        "pagination": {"has_more": True, "cursor": c},
                    }
                    for c in cursors
                ]
                with patch.object(secret_scan, "MAX_AUDIT_PAGES", len(cursors)):
                    result, out, _ = self.run_cli()
                self.assertEqual(result, 2)
                self.assertNotIn("0 findings", out)

    def test_bad_login_and_oversized_answers_fail_closed(self):
        for login in [
            {},
            {"data": {}},
            {"data": {"access_token": 1}},
            {"data": {"access_token": ""}},
            {"data": {"access_token": "short"}},
            {"data": {"access_token": "<redacted>"}},
            b"not json",
        ]:
            with self.subTest(login=login):
                self.login = login
                result, out, err = self.run_cli()
                self.assertEqual(result, 2)
                self.assertNotIn("0 findings", out)
                self.assertNotIn(PASSWORD, out + err)
        self.login = {"data": {"access_token": ACCESS}}
        with patch.object(secret_scan, "MAX_AUDIT_RESPONSE_BYTES", 8):
            result, _, _ = self.run_cli()
        self.assertEqual(result, 2)

    def test_network_errors_and_bad_configuration_are_sanitized(self):
        for failure in [OSError(ACCESS + PASSWORD), ValueError(REFRESH)]:
            with patch.object(
                secret_scan.urllib.request.OpenerDirector, "open", side_effect=failure
            ):
                result, out, err = self.run_cli()
            self.assertEqual(result, 2)
            for secret in [ACCESS, REFRESH, PASSWORD]:
                self.assertNotIn(secret, out + err)
        self.config.write_text("auth: [broken")
        result, out, err = self.run_cli()
        self.assertEqual(result, 2)
        self.assertNotIn("Traceback", err)
        self.assertNotIn("0 findings", out)


if __name__ == "__main__":
    unittest.main()
