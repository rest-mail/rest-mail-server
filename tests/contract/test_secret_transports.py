"""Credential transport and safe-reporting controls for #1165/#658/#673."""

import base64
import contextlib
import io
import json
import tempfile
import unittest
from http.cookies import SimpleCookie
from pathlib import Path
from urllib.parse import quote

import secret_scan
from test_secret_scan import exchange, har


class SecretTransportTest(unittest.TestCase):
    def test_malformed_har_fails_without_printing_credential_text(self):
        value = "fixture-malformed-har-password"
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "run.har"
            path.write_text("not json " + value)
            stdout, stderr = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                result = secret_scan.main([str(path)])
        self.assertEqual(result, 2)
        self.assertNotIn(value, stdout.getvalue() + stderr.getvalue())
        self.assertNotIn("0 findings", stdout.getvalue())
        self.assertNotIn("Traceback", stderr.getvalue())

    def test_refresh_token_sent_without_an_issuance_is_collected(self):
        value = "fixture-refresh-only-token"
        sent = exchange("POST", "/api/v1/auth/refresh", {"refresh_token": value})
        echoed = exchange("GET", "/api/v1/accounts/7", response={"note": value})
        findings = secret_scan.scan(har(sent, echoed))
        self.assertEqual(len(findings), 1)
        self.assertIn("refresh_token sent in", findings[0])

    def test_request_authentication_transports_are_collected(self):
        value = "fixture-header-password"
        basic = base64.b64encode(("fixture@example.test:" + value).encode()).decode()
        for name, transport in [
            (
                "bearer",
                {"headers": [{"name": "aUtHoRiZaTiOn", "value": "Bearer " + value}]},
            ),
            (
                "basic",
                {"headers": [{"name": "Authorization", "value": "Basic " + basic}]},
            ),
            (
                "cookie-header",
                {"headers": [{"name": "Cookie", "value": "restmail_access=" + value}]},
            ),
            ("har-cookie", {"cookies": [{"name": "restmail_refresh", "value": value}]}),
        ]:
            with self.subTest(transport=name):
                sent = exchange("GET", "/api/v1/accounts/7")
                sent["request"].update(transport)
                echoed = exchange("GET", "/api/v1/accounts/7", response={"note": value})
                audit = [json.dumps({"action": "test", "details": {"copied": value}})]
                findings = secret_scan.scan(har(sent, echoed), audit)
                self.assertEqual(len(findings), 2)
                self.assertIn("answers the", findings[0])
                self.assertIn("audit log", findings[1])

    def test_every_response_transport_is_scanned(self):
        value = 'fixture-"quoted"-password-雪'
        sent = exchange("POST", "/api/v1/accounts", {"password": value})
        for name, transport in [
            ("header", {"headers": [{"name": "X-Debug", "value": value}]}),
            (
                "location",
                {
                    "headers": [
                        {"name": "Location", "value": "/next?leaked=" + quote(value)}
                    ]
                },
            ),
            ("cookie", {"cookies": [{"name": "debug", "value": value}]}),
            (
                "set-cookie",
                {
                    "headers": [
                        {
                            "name": "Set-Cookie",
                            "value": SimpleCookie({"debug": value})
                            .output(header="")
                            .strip()
                            + "; Path=/",
                        }
                    ]
                },
            ),
        ]:
            with self.subTest(transport=name):
                echoed = exchange("GET", "/api/v1/accounts/7")
                echoed["response"].update(transport)
                findings = secret_scan.scan(har(sent, echoed))
                self.assertTrue(findings)
                self.assertIn(name.split("-")[-1], " ".join(findings).lower())
                self.assertNotIn(value, " ".join(findings))

    def test_response_basic_credentials_are_decoded_before_matching(self):
        value = "fixture-basic-echo-password"
        sent = exchange("POST", "/api/v1/accounts", {"password": value})
        encoded = base64.b64encode(("fixture:" + value).encode()).decode()
        echoed = exchange("GET", "/api/v1/accounts/7")
        echoed["response"]["headers"] = [
            {"name": "Authorization", "value": "Basic " + encoded}
        ]
        findings = secret_scan.scan(har(sent, echoed))
        self.assertEqual(len(findings), 1)

    def test_cookie_only_session_issuance_keeps_its_creation_exception(self):
        value = "fixture-issued-session-token"
        for cookie in ["restmail_access", "restmail_refresh"]:
            for transport in [
                {"cookies": [{"name": cookie, "value": value}]},
                {
                    "headers": [
                        {
                            "name": "Set-Cookie",
                            "value": cookie + "=" + value + "; HttpOnly; Secure",
                        }
                    ]
                },
            ]:
                with self.subTest(cookie=cookie, transport=transport):
                    issued = exchange(
                        "POST", "/api/v1/auth/login", response={"data": {"id": 7}}
                    )
                    issued["response"].update(transport)
                    self.assertEqual(secret_scan.scan(har(issued)), [])
                    echoed = exchange(
                        "GET", "/api/v1/accounts/7", response={"note": value}
                    )
                    findings = secret_scan.scan(har(issued, echoed))
                    self.assertEqual(len(findings), 1)
                    self.assertIn("issued by", findings[0])

    def test_push_device_tokens_are_not_mistaken_for_issued_credentials(self):
        value = "ab" * 32
        registered = exchange(
            "POST",
            "/api/v1/push/devices",
            {"token": value},
            {"data": {"id": "1", "token": value}},
        )
        listed = exchange(
            "GET",
            "/api/v1/push/devices",
            response={"data": [{"id": "1", "token": value}]},
        )
        self.assertEqual(secret_scan.scan(har(registered, listed)), [])
        # Excluding device-token collection must not exempt push answers from
        # matching credentials that were sent elsewhere.
        sent = exchange("POST", "/api/v1/accounts", {"password": value})
        self.assertEqual(len(secret_scan.scan(har(sent, listed))), 1)

    def test_findings_never_repeat_secrets_or_their_prefixes(self):
        value = "zzQX-sensitive-fixture-password"
        other = "yyNZ-sensitive-fixture-password"
        sent = exchange(
            "POST",
            "/api/v1/accounts",
            {"password": value, "private_key": other},
            name=value,
        )
        echoed = exchange(
            "GET",
            "/api/v1/accounts/7?debug=" + value,
            response={other: value},
            name=other,
        )
        echoed["response"]["headers"] = [{"name": "X-" + other, "value": value}]
        audit = [json.dumps({"action": value, "details": {other: value}})]
        findings = secret_scan.scan(har(sent, echoed), audit)
        self.assertTrue(findings)
        for secret in [value, other]:
            self.assertNotIn(secret, " ".join(findings))
            self.assertNotIn(secret[:4], " ".join(findings))
        self.assertIn("GET", " ".join(findings))
        self.assertIn("audit log", " ".join(findings))

    def test_cli_fails_for_a_header_leak_without_printing_the_value(self):
        value = "zzQX-fixture-cli-password"
        sent = exchange("POST", "/api/v1/accounts", {"password": value})
        echoed = exchange("GET", "/api/v1/accounts/7?debug=" + value)
        echoed["response"]["headers"] = [{"name": "X-Debug", "value": value}]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "fixture.har"
            path.write_text(json.dumps(har(sent, echoed)))
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                status = secret_scan.main([str(path)])
        self.assertEqual(status, 1)
        self.assertIn("secret echoed:", output.getvalue())
        self.assertNotIn(value, output.getvalue())
        self.assertNotIn(value[:4], output.getvalue())

    def test_api_document_examples_are_not_issuance_but_headers_are_scanned(self):
        value = "fixture-documented-password"
        docs = exchange(
            "GET", "/api/docs/openapi.json", response={"example": {"password": value}}
        )
        sent = exchange(
            "POST", "/api/v1/accounts", {"password": value}, {"data": {"id": 7}}
        )
        self.assertEqual(secret_scan.scan(har(docs, sent)), [])
        docs["response"]["headers"] = [{"name": "X-Debug", "value": value}]
        findings = secret_scan.scan(har(sent, docs))
        self.assertEqual(len(findings), 1)
        self.assertIn("response header X-Debug", findings[0])

    def test_url_metadata_drops_queries_fragments_and_userinfo(self):
        value = 'fixture-url-"secret"-雪'
        sent = exchange("POST", "/api/v1/accounts", {"password": value})
        echoed = exchange(
            "GET", "/api/v1/accounts/" + quote(value), response={"note": value}
        )
        echoed["request"]["url"] += "?debug=" + quote(value) + "#" + value
        echoed["request"]["url"] = echoed["request"]["url"].replace(
            "http://", "http://fixture-user:fixture-url-pass@", 1
        )
        findings = secret_scan.scan(har(sent, echoed))
        self.assertEqual(len(findings), 1)
        output = findings[0]
        for hidden in [
            value,
            quote(value),
            "fixture-url-pass",
            "fixture-user",
            "?debug=",
            "#",
        ]:
            self.assertNotIn(hidden, output)
        self.assertIn("GET", output)

    def test_redacted_short_malformed_and_noncredential_transports_are_ignored(self):
        for transport in [
            {"headers": [{"name": "Authorization", "value": "Bearer <redacted>"}]},
            {"headers": [{"name": "Authorization", "value": "Basic !!!malformed"}]},
            {
                "headers": [
                    {
                        "name": "Authorization",
                        "value": "Basic " + base64.b64encode(b"no-colon").decode(),
                    }
                ]
            },
            {
                "headers": [
                    {
                        "name": "Cookie",
                        "value": "restmail_access=short; restmail_csrf=fixture-public-csrf",
                    }
                ]
            },
            {
                "cookies": [
                    {"name": "restmail_access", "value": "<redacted>"},
                    {"name": "theme", "value": "fixture-public-theme"},
                ]
            },
        ]:
            with self.subTest(transport=transport):
                sent = exchange("GET", "/api/v1/accounts/7")
                sent["request"].update(transport)
                echoed = exchange(
                    "GET",
                    "/api/v1/accounts/7",
                    response={
                        "notes": [
                            "<redacted>",
                            "short",
                            "fixture-public-csrf",
                            "fixture-public-theme",
                        ]
                    },
                )
                self.assertEqual(secret_scan.scan(har(sent, echoed)), [])


if __name__ == "__main__":
    unittest.main()
