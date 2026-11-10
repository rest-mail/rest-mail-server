"""App-password proofs use the gateway session, never HTTP Basic access."""

import io
import json
import os
import tempfile
import types
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import Mock, patch

import live_principals
import live_requirements
import yaml
from test_live_requirements import DOCUMENT, PAIR, REPORT


class LivePrincipals(unittest.TestCase):
    def setUp(self):
        self.endpoint = "http://public.test"
        self.internal = "https://internal.test:8443"
        self.app = {"address": "ordinary@restmail.test", "secret": "app-secret", "id": 23}
        self.principals = {
            "ordinary": {"address": self.app["address"], "password": "ordinary-password"},
            "app_password": self.app,
            "other": {"id": 42},
        }
        self.fixture = {"email": "scoped@restmail.test", "domain_id": 11}
        self.examples = {
            "getDomain": {
                "found": {"response": {"status": 200}},
                "outside_domain": {"response": {"status": 403}},
                "unauthenticated": {"response": {"status": 401}},
            },
            "listFolders": {
                "found": {"response": {"status": 200}},
                "key_wrong_scope": {"response": {"status": 404}},
            },
        }
        self.calls = []
        self.verdicts = []
        self.basic_status = 401
        self.session = {
            "token": "rmgs_real-session",
            "protocol": "imap",
            "credential_kind": "app_password",
            "user": {"id": 23},
        }
        self.client = Mock()
        self.client.expect.side_effect = self.expect
        self.context = Mock()
        self.proof_code = 0

    def expect(self, status, method, path, body=None, token=None):
        if path == "/api/v1/admin/domains":
            return {"data": [{"id": 11, "name": "restmail.test"}, {"id": 12, "name": "other.test"}]}
        if method == "POST":
            return {"data": {"id": 51, "token": "rmk_real-key"}}
        return {"data": []}

    def urlopen(self, request, **options):
        if isinstance(request, str):
            return io.BytesIO(b'{"paths":{}}')
        self.calls.append((request, options))
        if request.get_header("Authorization", "").startswith("Basic "):
            if self.basic_status != 200:
                raise urllib.error.HTTPError(request.full_url, self.basic_status, "refused", {}, io.BytesIO())
            response = io.BytesIO(b"{}")
            response.status = 200
            return response
        response = io.BytesIO(json.dumps({"data": self.session}).encode())
        response.status = 201 if request.method == "POST" else 204
        return response

    def prove(self, *args, **options):
        self.verdicts.append((args, options))
        return self.proof_code

    def run_matrix(self):
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.dict(os.environ, {"CONTRACT_INTERNAL_ENDPOINT": self.internal, "CONTRACT_MTLS_DIR": directory}),
            patch.object(live_principals, "Client", return_value=self.client),
            patch.object(live_principals.mfa_admin, "session", return_value="admin-session"),
            patch.object(live_principals, "login", return_value={"access_token": "ordinary-session"}),
            patch.object(live_principals, "mfa_session", return_value=({
                "scoped_capabilities": [{"capability": "domains:read", "scope": "domain:restmail.test"}],
                "user": {"id": 31}, "access_token": "scoped-session",
            }, "plain-session")),
            patch("ssl.create_default_context", return_value=self.context),
            patch.object(live_principals.urllib.request, "urlopen", side_effect=self.urlopen),
            patch.object(live_principals, "prove", side_effect=self.prove),
        ):
            return live_principals.run("vertrag", self.endpoint, directory, self.principals, self.fixture, self.examples)

    def test_app_password_keeps_own_200_and_other_404_through_gateway(self):
        self.assertEqual(self.run_matrix(), 0)
        self.assertEqual(len(self.verdicts), 9)
        for args, options in self.verdicts:
            if args[4].startswith("app-password-"):
                self.assertEqual(args[1], self.internal)
                self.assertEqual(options["header"], "Authorization: Bearer rmgs_real-session")
                self.assertEqual(set(options["transport"]), {"ca-cert", "cert", "cert-key"})
                self.assertEqual(options["path_values"], {"id": 23 if args[4].endswith("own") else 42})
                self.assertEqual(next(iter(args[3]["listFolders"].values()))["response"]["status"], 200 if args[4].endswith("own") else 404)
            else:
                self.assertEqual(args[1], self.endpoint)
                self.assertNotIn("transport", options)

    def test_session_opens_with_body_credential_and_closes_after_failed_proof(self):
        self.proof_code = 1
        self.assertEqual(self.run_matrix(), 1)
        gateway = [(request, options) for request, options in self.calls if request.full_url.startswith(self.internal)]
        self.assertEqual([request.method for request, _ in gateway], ["POST", "DELETE"])
        opened, closed = gateway[0][0], gateway[1][0]
        self.assertEqual(json.loads(opened.data), {"protocol": "imap", "address": self.app["address"], "password": self.app["secret"]})
        self.assertIsNone(opened.get_header("Authorization"))
        self.assertEqual(closed.get_header("Authorization"), "Bearer rmgs_real-session")
        self.assertTrue(closed.full_url.endswith("/gateway-sessions/current"))
        self.assertTrue(all(options["context"] is self.context for _, options in gateway))
        self.context.load_cert_chain.assert_called_once()
        self.assertEqual(self.client.expect.call_args.args[:3], (204, "DELETE", "/api/v1/accounts/31/api-keys/51"))

    def test_basic_is_explicitly_refused_for_both_real_mailboxes(self):
        self.assertEqual(self.run_matrix(), 0)
        basic = [request for request, _ in self.calls if request.get_header("Authorization", "").startswith("Basic ")]
        self.assertEqual({request.full_url for request in basic}, {
            self.endpoint + "/api/v1/accounts/23/folders",
            self.endpoint + "/api/v1/accounts/42/folders",
        })

    def test_basic_success_or_different_refusal_is_not_accepted(self):
        for status in (200, 403, 404, 500):
            with self.subTest(status=status):
                self.basic_status = status
                with self.assertRaisesRegex(AssertionError, "Basic.*401"):
                    self.run_matrix()

    def test_wrong_account_or_credential_kind_cannot_supply_the_app_proof(self):
        for field, value in (("user", {"id": 42}), ("credential_kind", "password"), ("protocol", "smtp")):
            with self.subTest(field=field), patch.dict(self.session, {field: value}):
                self.calls.clear()
                self.verdicts.clear()
                with self.assertRaisesRegex(AssertionError, "fixture app password"):
                    self.run_matrix()
                self.assertEqual(self.verdicts, [])
                self.assertEqual([request.method for request, _ in self.calls], ["POST", "DELETE"])

    def test_vertrag_receives_verified_transport_for_only_the_selected_operation(self):
        transport = {"ca-cert": "/certs/ca.crt", "cert": "/certs/imap.crt", "cert-key": "/certs/imap.key"}

        def run(command, **options):
            path = Path(command[command.index("--config") + 1])
            config = yaml.safe_load(path.read_text())
            self.assertEqual(config["endpoints"], [{
                "endpoint": self.internal, "operation-id": ["getThing"], "transport": transport,
            }])
            self.assertNotIn("insecure", config)
            self.assertEqual(config["principals"][0]["auth"]["header"], "Authorization: Bearer rmgs_real-session")
            (path.parent / "report.json").write_text(json.dumps(REPORT))
            return types.SimpleNamespace(returncode=0)

        with tempfile.TemporaryDirectory() as directory, patch.object(live_requirements.subprocess, "run", side_effect=run):
            self.assertEqual(live_requirements.prove(
                "vertrag", self.internal, DOCUMENT, {"getThing": {"unauthenticated": PAIR}},
                "app-password-own", directory, requirement=PAIR["requires"],
                header="Authorization: Bearer rmgs_real-session", transport=transport,
            ), 0)


if __name__ == "__main__":
    unittest.main()
