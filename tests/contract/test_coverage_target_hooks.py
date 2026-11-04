"""Disposable coverage targets through the configured entrypoint and real names."""

import copy
import json
import os
import runpy
import subprocess
import sys
import threading
import types
import unittest
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import yaml
from test_hooks_two_factor_admin import session

ROOT = Path(__file__).resolve().parents[2]
ROUTES = {
    "revokeAPIKey": "/api/v1/accounts/{id}/api-keys/{credential_id}",
    "revokeCredential": "/api/v1/accounts/{id}/credentials/{credential_id}",
    "deleteQuarantine": "/api/v1/accounts/{id}/quarantine/{mid}",
    "clearSuppression": "/api/v1/admin/domains/{id}/suppressions/{sid}",
    "getCertificate": "/api/v1/admin/certificates/{id}",
}
OPERATIONS = {
    "revokeAPIKey": ("/api/v1/accounts/1/api-keys/1", "Revoke an API key", 204),
    "revokeCredential": (
        "/api/v1/accounts/1/credentials/1",
        "Revoke an app password",
        204,
    ),
    "deleteQuarantine": (
        "/api/v1/accounts/1/quarantine/41",
        "Permanently delete a quarantined message",
        200,
    ),
    "clearSuppression": (
        "/api/v1/admin/domains/1/suppressions/1",
        "Clear a suppression",
        204,
    ),
    "getCertificate": (
        "/api/v1/admin/certificates/1",
        "Get certificate details (includes cert_pem)",
        200,
    ),
}


class FixtureAPI(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.calls.append((self.path, self.headers["Authorization"], body))
        self.server.serial += 1
        data = json.dumps({"data": dict(body, id=self.server.serial)}).encode()
        self.send_response(self.server.failure or 201)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class CoverageTargetHooks(unittest.TestCase):
    def setUp(self):
        stub = types.ModuleType("vertrag_hooks")
        stub.stash = {}
        self.callbacks = {}

        def before(operation):
            def register(callback):
                self.callbacks.setdefault(operation, []).append(callback)
                return callback

            return register

        stub.before = before
        stub.after = lambda _: lambda callback: callback
        stub.get_json = lambda transaction, which="request": json.loads(
            transaction[which]["body"]
        )
        stub.set_json = lambda transaction, body: transaction["request"].update(
            body=json.dumps(body)
        )
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        with patch.dict(sys.modules, {"vertrag_hooks": stub}):
            self.loaded = runpy.run_path(str(ROOT / config["hookfiles"]))
        self.shared = self.loaded["shared"]
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.serial = 100
        self.server.calls = []
        self.server.failure = None
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.thread.join)
        self.addCleanup(self.server.shutdown)
        origin = f"http://127.0.0.1:{self.server.server_port}"
        stub.stash["setup_admin_tokens"] = {origin: "setup-session"}
        overrides = patch.dict(
            self.shared,
            {
                "mfa_admin_token": lambda _: "setup-mfa-session",
                "mailbox_address": lambda *_: "alice@restmail.test",
                "openssl": self.openssl,
            },
        )
        overrides.start()
        self.addCleanup(overrides.stop)
        environment = patch.dict(os.environ, {"CONTRACT_COVERAGE": "1"})
        environment.start()
        self.addCleanup(environment.stop)
        self.targets = self.loaded["targets"]
        self.sleep = patch.object(self.targets["time"], "sleep").start()
        self.fixture = patch.object(
            self.targets["subprocess"], "run", side_effect=self.held_mail
        ).start()
        self.addCleanup(patch.stopall)

    def openssl(self, *args):
        Path(args[args.index("-keyout") + 1]).write_text("fixture key")
        Path(args[args.index("-out") + 1]).write_text("fixture certificate")

    def held_mail(self, *args, **kwargs):
        self.server.serial += 1
        targets = {
            "account_id": 4,
            "message_id": self.server.serial,
            "domain_id": 8,
            "suppression_id": self.server.serial,
        }
        return types.SimpleNamespace(
            stdout="HELD_MAIL_FIXTURE="
            + json.dumps(targets)
            + "\ncontract fixtures prepared\n"
        )

    def transaction(self, operation, path=None, status=None):
        original, summary, success = OPERATIONS[operation]
        status = status or success
        content = " > application/json" if status == 200 else ""
        return {
            "name": f"{ROUTES[operation]} > {summary} > {status}{content}",
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_port),
            "fullPath": (path or original) + "?keep=yes",
            "request": {
                "method": "GET" if operation == "getCertificate" else "DELETE",
                "headers": {"Authorization": session(self.shared["ADMIN"])},
                "body": '{ "generated": [null, false, 0] }',
            },
        }

    def send(self, operation, transaction):
        callbacks = [
            callback
            for callback in self.callbacks[operation]
            if callback.__name__.endswith("_for_coverage")
        ]
        self.assertEqual(len(callbacks), 1)
        callbacks[0](transaction)

    def mailbox_probe(self, path="/api/v1/admin/mailboxes/1", status=200):
        return {
            "name": "/api/v1/admin/mailboxes/{id} > Update a mailbox > %d > application/json" % status,
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_port),
            "fullPath": path + "?keep=yes",
            "request": {
                "method": "PATCH",
                "headers": {"Authorization": session(self.shared["mfa_admin"].ADDRESS)},
                "body": '{"password": "generated"}',
            },
        }

    def update_mailbox(self, transaction):
        [callback] = [
            callback
            for callback in self.callbacks["updateMailbox"]
            if callback.__name__ == "a_disposable_mailbox_for_coverage"
        ]
        callback(transaction)

    def test_a_generated_password_never_reaches_the_seeded_account(self):
        first, second = self.mailbox_probe(), self.mailbox_probe()
        body = first["request"]["body"]
        self.update_mailbox(first)
        self.update_mailbox(second)
        self.assertRegex(first["fullPath"], r"^/api/v1/admin/mailboxes/10[0-9]\?keep=yes$")
        self.assertEqual(second["fullPath"], first["fullPath"])
        self.assertEqual(first["request"]["body"], body)
        [(path, authorization, made)] = self.server.calls
        self.assertEqual(path, "/api/v1/accounts")
        self.assertEqual(authorization, "Bearer setup-mfa-session")
        self.assertRegex(made["address"], r"^contract-update-[0-9]+@restmail\.test$")

    def test_only_coverages_literal_mailbox_success_moves(self):
        for transaction in (
            self.mailbox_probe("/api/v1/admin/mailboxes/57"),
            self.mailbox_probe("/api/v1/admin/mailboxes/0"),
            self.mailbox_probe(status=404),
        ):
            before = copy.deepcopy(transaction)
            self.update_mailbox(transaction)
            self.assertEqual(transaction, before)
        with patch.dict(os.environ, {"CONTRACT_COVERAGE": "0"}):
            transaction = self.mailbox_probe()
            before = copy.deepcopy(transaction)
            self.update_mailbox(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_only_the_path_changes_and_every_request_gets_a_fresh_target(self):
        for operation in OPERATIONS:
            with self.subTest(operation=operation):
                paths = []
                for _ in range(3):
                    transaction = self.transaction(operation)
                    before = copy.deepcopy(transaction)
                    self.send(operation, transaction)
                    paths.append(transaction["fullPath"])
                    self.assertNotEqual(transaction["fullPath"], before["fullPath"])
                    before["fullPath"] = transaction["fullPath"]
                    self.assertEqual(transaction, before)
                    self.assertTrue(transaction["fullPath"].endswith("?keep=yes"))
                self.assertEqual(len(set(paths)), 3)

    def test_quarantine_minimum_mid_gets_its_own_target_too(self):
        transaction = self.transaction(
            "deleteQuarantine", "/api/v1/accounts/1/quarantine/1"
        )
        self.send("deleteQuarantine", transaction)
        self.assertEqual(
            transaction["fullPath"], "/api/v1/accounts/4/quarantine/101?keep=yes"
        )

    def test_the_helper_returns_exact_row_ids_and_never_replenishes_the_queue(self):
        for operation in ("deleteQuarantine", "clearSuppression"):
            self.send(operation, self.transaction(operation))
        self.assertEqual(self.fixture.call_count, 2)
        for call in self.fixture.call_args_list:
            self.assertEqual(call.args, (["chore", "test:contract-held-mail-fixture"],))
            self.assertEqual(
                call.kwargs,
                {"check": True, "capture_output": True, "timeout": 20, "text": True},
            )

    def test_credentials_are_created_with_a_separate_mfa_setup_session(self):
        for operation in ("revokeAPIKey", "revokeCredential"):
            self.send(operation, self.transaction(operation))
        self.assertEqual(len(self.server.calls), 2)
        for path, authorization, body in self.server.calls:
            self.assertEqual(authorization, "Bearer setup-mfa-session")
            self.assertEqual(body["password"], self.shared["mfa_admin"].PASSWORD)
            self.assertTrue(body["name"].startswith("contract-revoke-"))
            if path.endswith("/api-keys"):
                self.assertEqual(
                    body["permissions"],
                    [
                        {
                            "capability": "mail:read",
                            "scope": "mailbox:alice@restmail.test",
                        }
                    ],
                )
            else:
                self.assertNotIn("permissions", body)
        self.sleep.assert_called_with(1.1)

    def test_certificate_is_not_on_a_shared_domain_or_auto_renewed(self):
        self.send("getCertificate", self.transaction("getCertificate"))
        domain, certificate = self.server.calls
        self.assertEqual(domain[0], "/api/v1/admin/domains")
        self.assertTrue(domain[2]["name"].startswith("contract-certificate-"))
        self.assertEqual(certificate[0], "/api/v1/admin/certificates")
        self.assertEqual(certificate[2]["domain_id"], 101)
        self.assertIs(certificate[2]["auto_renew"], False)

    def test_other_targets_and_invalid_path_segments_are_not_rewritten(self):
        for operation, (original, _, _) in OPERATIONS.items():
            segments = original.split("/")
            for index, value in enumerate(segments):
                if not value.isdecimal():
                    continue
                for target in ("0", "2", "x", "1.5", "2147483647"):
                    parts = segments.copy()
                    parts[index] = target
                    transaction = self.transaction(operation, "/".join(parts))
                    before = copy.deepcopy(transaction)
                    with self.subTest(
                        operation=operation, path=transaction["fullPath"]
                    ):
                        self.send(operation, transaction)
                        self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])
        self.fixture.assert_not_called()

    def test_refusals_other_phases_and_other_principals_are_not_rewritten(self):
        for operation in OPERATIONS:
            for status in (401, 403, 404, 422, 500):
                transaction = self.transaction(operation, status=status)
                before = copy.deepcopy(transaction)
                self.send(operation, transaction)
                self.assertEqual(transaction, before)
            for address in (None, "contract-ordinary@restmail.test"):
                transaction = self.transaction(operation)
                transaction["request"]["headers"] = (
                    {} if address is None else {"Authorization": session(address)}
                )
                before = copy.deepcopy(transaction)
                self.send(operation, transaction)
                self.assertEqual(transaction, before)
            with patch.dict(os.environ, {"CONTRACT_COVERAGE": "0"}):
                transaction = self.transaction(operation)
                before = copy.deepcopy(transaction)
                self.send(operation, transaction)
                self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])
        self.fixture.assert_not_called()

    def test_existing_held_mail_hooks_leave_coverage_to_the_new_hooks(self):
        for operation in ("deleteQuarantine", "clearSuppression"):
            transaction = self.transaction(operation)
            for callback in self.callbacks[operation]:
                callback(transaction)
            self.assertIn(
                "/101?keep=yes" if operation == "deleteQuarantine" else "/102?keep=yes",
                transaction["fullPath"],
            )

    def test_setup_errors_are_not_swallowed(self):
        self.server.failure = 500
        for operation in ("revokeAPIKey", "revokeCredential", "getCertificate"):
            with (
                self.subTest(operation=operation),
                self.assertRaises(urllib.error.HTTPError),
            ):
                self.send(operation, self.transaction(operation))
        self.fixture.side_effect = subprocess.CalledProcessError(1, "fixture")
        for operation in ("deleteQuarantine", "clearSuppression"):
            with (
                self.subTest(operation=operation),
                self.assertRaises(subprocess.CalledProcessError),
            ):
                self.send(operation, self.transaction(operation))

    def test_missing_or_invalid_helper_ids_fail_before_sending(self):
        for output in (
            "",
            "HELD_MAIL_FIXTURE={}",
            'HELD_MAIL_FIXTURE={"account_id":true}',
        ):
            self.fixture.side_effect = None
            self.fixture.return_value = types.SimpleNamespace(stdout=output)
            with self.subTest(output=output), self.assertRaises(RuntimeError):
                self.send("deleteQuarantine", self.transaction("deleteQuarantine"))


if __name__ == "__main__":
    unittest.main()
