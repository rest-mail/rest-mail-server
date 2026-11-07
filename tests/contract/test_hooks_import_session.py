"""closeImportSession's documented close gets a session the run opened."""

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
CLOSE = "/api/v1/accounts/{id}/messages/import-sessions/{session} > Close an import session and write its summary > "
OPEN = "/api/v1/accounts/{id}/messages/import-sessions > Open an import session > "
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
        "contract_import_session_hooks", ROOT / "tests/contract/hooks.py"
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


class ImportSessionFixture(unittest.TestCase):
    def setUp(self):
        self.module, registered = load_hooks()
        for name in ("createImportSession", "after createImportSession", "closeImportSession"):
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

    def test_the_documented_close_closes_a_session_opened_for_it(self):
        t = self.transaction(
            CLOSE + "200 > application/json",
            f"/api/v1/accounts/12/messages/import-sessions/{DOCUMENTED}",
            self.module.ADMIN,
        )
        self.module.close_an_import_session_the_run_opened(t)
        self.assertEqual(t["fullPath"], "/api/v1/accounts/4/messages/import-sessions/" + "ab" * 16)
        self.assertEqual(len(self.requests), 1)
        self.assertEqual(self.requests[0].get_method(), "POST")
        self.assertEqual(
            self.requests[0].full_url,
            "http://server.test:8080/api/v1/accounts/4/messages/import-sessions",
        )

    def test_the_unknown_session_is_asked_of_the_runs_own_account(self):
        unknown = self.module.UNKNOWN_IMPORT_SESSION
        t = self.transaction(
            CLOSE + "404 > application/json",
            f"/api/v1/accounts/12/messages/import-sessions/{unknown}",
            self.module.ADMIN,
        )
        self.module.close_an_import_session_the_run_opened(t)
        self.assertEqual(t["fullPath"], f"/api/v1/accounts/4/messages/import-sessions/{unknown}")
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
                        f"/api/v1/accounts/12/messages/import-sessions/{session}?keep=yes",
                        self.module.ADMIN,
                    )
                    original = json.loads(json.dumps(transaction))
                    self.module.close_an_import_session_the_run_opened(transaction)
                    self.assertEqual(transaction, original)
                    self.assertEqual(self.requests, [])

    def test_valid_session_coverage_probes_keep_the_query_and_request(self):
        with patch.dict(self.module.os.environ, {"CONTRACT_COVERAGE": "1"}):
            transaction = self.transaction(
                "coverage: " + CLOSE + "200 > application/json",
                "/api/v1/accounts/12/messages/import-sessions/" + "ff" * 16 + "?keep=yes",
                self.module.ADMIN,
            )
            original = json.loads(json.dumps(transaction))
            self.module.close_an_import_session_the_run_opened(transaction)
            original["fullPath"] = "/api/v1/accounts/4/messages/import-sessions/" + "ab" * 16 + "?keep=yes"
            self.assertEqual(transaction, original)
            self.assertEqual(len(self.requests), 1)

    def test_other_closes_are_sent_as_they_are(self):
        for name, path, address in (
            (CLOSE + "200 > application/json", f"/api/v1/accounts/12/messages/import-sessions/{DOCUMENTED}", None),
            (CLOSE + "200 > application/json", f"/api/v1/accounts/12/messages/import-sessions/{DOCUMENTED}", "ordinary@restmail.test"),
            (CLOSE + "200 > application/json", f"/api/v1/accounts/0/messages/import-sessions/{DOCUMENTED}", self.module.ADMIN),
            (CLOSE + "404 > application/json", f"/api/v1/accounts/12/messages/import-sessions/{DOCUMENTED}", self.module.ADMIN),
            (CLOSE + "422 > application/json", f"/api/v1/accounts/0/messages/import-sessions/{DOCUMENTED}", self.module.ADMIN),
        ):
            with self.subTest(name=name, path=path, address=address):
                t = self.transaction(name, path, address)
                self.module.close_an_import_session_the_run_opened(t)
                self.assertEqual(t["fullPath"], path)
        self.assertEqual(self.requests, [])

    def test_the_documented_open_is_on_the_runs_account_and_is_closed_after(self):
        t = self.transaction(OPEN + "201 > application/json", "/api/v1/accounts/12/messages/import-sessions", self.module.ADMIN)
        self.module.open_an_import_session_on_the_admins_account(t)
        self.assertEqual(t["fullPath"], "/api/v1/accounts/4/messages/import-sessions")
        t["real"]["body"] = json.dumps({"data": {"id": "cd" * 16, "account_id": 4}})
        self.module.close_the_opened_import_session(t)
        self.assertEqual(len(self.requests), 1)
        self.assertEqual(self.requests[0].get_method(), "DELETE")
        self.assertEqual(
            self.requests[0].full_url,
            "http://server.test:8080/api/v1/accounts/4/messages/import-sessions/" + "cd" * 16,
        )

    def test_a_refused_open_closes_nothing(self):
        t = self.transaction(OPEN + "422 > application/json", "/api/v1/accounts/0/messages/import-sessions", self.module.ADMIN)
        self.module.open_an_import_session_on_the_admins_account(t)
        self.assertEqual(t["fullPath"], "/api/v1/accounts/0/messages/import-sessions")
        t["real"]["body"] = json.dumps({"error": {"code": "validation_failed"}})
        self.module.close_the_opened_import_session(t)
        self.assertEqual(self.requests, [])

    def test_an_open_without_an_id_stops_the_close(self):
        with patch.object(
            self.module.urllib.request, "urlopen", lambda request, timeout: Reply(b'{"data": {}}')
        ):
            t = self.transaction(
                CLOSE + "200 > application/json",
                f"/api/v1/accounts/12/messages/import-sessions/{DOCUMENTED}",
                self.module.ADMIN,
            )
            with self.assertRaises(RuntimeError):
                self.module.close_an_import_session_the_run_opened(t)


if __name__ == "__main__":
    unittest.main()
