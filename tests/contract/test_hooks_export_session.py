"""closeExportSession's documented close gets an export the run opened (#2116)."""

import base64
import importlib.util
import io
import json
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
CLOSE = "/api/v1/accounts/{id}/exports/{session}/close > Close an export and record how it ended > "
OPEN = "/api/v1/accounts/{id}/exports > Open an audited export of the account's data > "
DOCUMENTED = "00112233445566778899aabbccddeeff"


def load_hooks():
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    registered = []
    stub.before = lambda name: lambda function: registered.append(name) or function
    stub.after = lambda name: lambda function: registered.append("after " + name) or function
    stub.get_json = lambda transaction, field="request": json.loads(
        transaction[field]["body"] or "null"
    )
    spec = importlib.util.spec_from_file_location(
        "contract_export_session_hooks", ROOT / "tests/contract/hooks.py"
    )
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, registered


def token_for(address):
    claims = base64.urlsafe_b64encode(json.dumps({"address": address}).encode())
    return "Bearer header." + claims.decode().rstrip("=") + ".signature"


class Reply(io.BytesIO):
    status = 200

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


class ExportSessionFixture(unittest.TestCase):
    def setUp(self):
        self.module, registered = load_hooks()
        for name in ("createExportSession", "after createExportSession", "closeExportSession"):
            self.assertIn(name, registered)
        self.requests = []
        self.enterContext(
            patch.object(self.module, "admin_token", lambda origin: "fixture-token")
        )
        self.enterContext(patch.object(self.module, "mailbox_id", lambda origin, address: 4))
        self.enterContext(patch.object(self.module.urllib.request, "urlopen", self.answer))

    def answer(self, request, timeout):
        self.requests.append(request)
        return Reply(json.dumps({"data": {"id": "ab" * 16}}).encode())

    def transaction(self, name, path, address, real=""):
        headers = {"Authorization": token_for(address)} if address else {}
        return {
            "name": name,
            "fullPath": path,
            "protocol": "http:",
            "host": "server.test",
            "port": 8080,
            "request": {"body": "", "headers": headers},
            "real": {"body": real},
        }

    def test_the_documented_close_closes_an_export_opened_for_it(self):
        t = self.transaction(
            CLOSE + "200 > application/json",
            f"/api/v1/accounts/12/exports/{DOCUMENTED}/close",
            self.module.ADMIN,
        )
        self.module.close_an_export_the_run_opened(t)
        self.assertEqual(t["fullPath"], "/api/v1/accounts/4/exports/" + "ab" * 16 + "/close")
        self.assertEqual(len(self.requests), 1)
        self.assertEqual(self.requests[0].get_method(), "POST")
        self.assertEqual(self.requests[0].full_url, "http://server.test:8080/api/v1/accounts/4/exports")
        self.assertEqual(json.loads(self.requests[0].data), {"data": "mail", "format": "maildir"})

    def test_the_unknown_session_is_asked_of_the_runs_own_account(self):
        unknown = self.module.UNKNOWN_EXPORT_SESSION
        t = self.transaction(
            CLOSE + "404 > application/json",
            f"/api/v1/accounts/12/exports/{unknown}/close",
            self.module.ADMIN,
        )
        self.module.close_an_export_the_run_opened(t)
        self.assertEqual(t["fullPath"], f"/api/v1/accounts/4/exports/{unknown}/close")
        self.assertEqual(self.requests, [])

    def test_invalid_session_coverage_probes_keep_the_generated_request(self):
        with patch.dict(self.module.os.environ, {"CONTRACT_COVERAGE": "1"}):
            for session in (
                "vertrag%20does%20not%20match",
                "12345",
                "",
                DOCUMENTED[:-1],
                DOCUMENTED + "0",
                DOCUMENTED.upper(),
                "g" * 32,
                DOCUMENTED + "%0a",
            ):
                with self.subTest(session=session):
                    transaction = self.transaction(
                        "coverage: " + CLOSE + "200 > application/json",
                        f"/api/v1/accounts/12/exports/{session}/close?keep=yes",
                        self.module.ADMIN,
                    )
                    original = json.loads(json.dumps(transaction))
                    self.module.close_an_export_the_run_opened(transaction)
                    self.assertEqual(transaction, original)
                    self.assertEqual(self.requests, [])

    def test_other_closes_are_sent_as_they_are(self):
        for name, path, address in (
            (CLOSE + "200 > application/json", f"/api/v1/accounts/12/exports/{DOCUMENTED}/close", None),
            (CLOSE + "200 > application/json", f"/api/v1/accounts/12/exports/{DOCUMENTED}/close", "ordinary@restmail.test"),
            (CLOSE + "200 > application/json", f"/api/v1/accounts/0/exports/{DOCUMENTED}/close", self.module.ADMIN),
            (CLOSE + "404 > application/json", f"/api/v1/accounts/12/exports/{DOCUMENTED}/close", self.module.ADMIN),
            (CLOSE + "422 > application/json", f"/api/v1/accounts/0/exports/{DOCUMENTED}/close", self.module.ADMIN),
        ):
            with self.subTest(name=name, path=path, address=address):
                t = self.transaction(name, path, address)
                self.module.close_an_export_the_run_opened(t)
                self.assertEqual(t["fullPath"], path)
        self.assertEqual(self.requests, [])

    def closing(self, summary):
        """urlopen answering a close with summary, recording the request."""

        def answer(request, timeout):
            self.requests.append(request)
            return Reply(json.dumps({"data": summary}).encode())

        return patch.object(self.module.urllib.request, "urlopen", answer)

    def test_each_documented_open_is_on_the_runs_account_and_is_closed_after(self):
        for data, format in (("mail", "maildir"), ("contacts", "vcard"), ("calendar", "icalendar"), ("settings", "json")):
            with self.subTest(data=data):
                self.requests.clear()
                t = self.transaction(OPEN + "201 > application/json", "/api/v1/accounts/12/exports", self.module.ADMIN)
                self.module.open_an_export_on_the_admins_account(t)
                self.assertEqual(t["fullPath"], "/api/v1/accounts/4/exports")
                t["real"]["body"] = json.dumps(
                    {"data": {"id": "cd" * 16, "account_id": 4, "data": data, "format": format}}
                )
                summary = {"id": "cd" * 16, "account_id": 4, "data": data, "format": format, "result": "aborted"}
                with self.closing(summary):
                    self.module.close_the_opened_export(t)
                self.assertNotIn("fail", t)
                self.assertEqual(len(self.requests), 1)
                self.assertEqual(self.requests[0].get_method(), "POST")
                self.assertEqual(
                    self.requests[0].full_url,
                    "http://server.test:8080/api/v1/accounts/4/exports/" + "cd" * 16 + "/close",
                )
                self.assertEqual(json.loads(self.requests[0].data), {"result": "aborted", "rows": 0, "bytes": 0})

    def test_a_close_that_does_not_answer_the_opened_export_fails_the_open(self):
        opened = {"id": "cd" * 16, "account_id": 4, "data": "contacts", "format": "vcard"}
        for summary in (
            dict(opened, data="mail", format="maildir", result="aborted"),
            dict(opened, result="completed"),
            dict(opened, id="ef" * 16, result="aborted"),
            {},
        ):
            with self.subTest(summary=summary):
                t = self.transaction(OPEN + "201 > application/json", "/api/v1/accounts/4/exports", self.module.ADMIN)
                t["real"]["body"] = json.dumps({"data": opened})
                with self.closing(summary):
                    self.module.close_the_opened_export(t)
                self.assertIn("closing the export it opened answered", t["fail"])

    def test_a_close_not_found_fails_an_open_the_fixture_admin_made(self):
        def not_found(request, timeout):
            raise self.module.urllib.error.HTTPError(request.full_url, 404, "Not Found", {}, io.BytesIO(b"{}"))

        t = self.transaction(OPEN + "201 > application/json", "/api/v1/accounts/4/exports", self.module.ADMIN)
        t["real"]["body"] = json.dumps({"data": {"id": "cd" * 16, "account_id": 4, "data": "settings", "format": "json"}})
        with patch.object(self.module.urllib.request, "urlopen", not_found):
            self.module.close_the_opened_export(t)
        self.assertIn("closing the export it opened answered", t["fail"])

    def test_an_open_by_another_principal_is_closed_without_judging_the_close(self):
        def not_found(request, timeout):
            self.requests.append(request)
            raise self.module.urllib.error.HTTPError(request.full_url, 404, "Not Found", {}, io.BytesIO(b"{}"))

        t = self.transaction(OPEN + "201 > application/json", "/api/v1/accounts/7/exports", "ordinary@restmail.test")
        t["real"]["body"] = json.dumps({"data": {"id": "cd" * 16, "account_id": 7, "data": "mail", "format": "maildir"}})
        with patch.object(self.module.urllib.request, "urlopen", not_found):
            self.module.close_the_opened_export(t)
        self.assertNotIn("fail", t)
        self.assertEqual(len(self.requests), 1)

    def test_a_refused_open_closes_nothing(self):
        t = self.transaction(OPEN + "422 > application/json", "/api/v1/accounts/0/exports", self.module.ADMIN)
        self.module.open_an_export_on_the_admins_account(t)
        self.assertEqual(t["fullPath"], "/api/v1/accounts/0/exports")
        t["real"]["body"] = json.dumps({"error": {"code": "validation_failed"}})
        self.module.close_the_opened_export(t)
        self.assertEqual(self.requests, [])

    def test_an_open_without_an_id_stops_the_close(self):
        with patch.object(
            self.module.urllib.request, "urlopen", lambda request, timeout: Reply(b'{"data": {}}')
        ):
            t = self.transaction(
                CLOSE + "200 > application/json",
                f"/api/v1/accounts/12/exports/{DOCUMENTED}/close",
                self.module.ADMIN,
            )
            with self.assertRaises(RuntimeError):
                self.module.close_an_export_the_run_opened(t)


if __name__ == "__main__":
    unittest.main()
