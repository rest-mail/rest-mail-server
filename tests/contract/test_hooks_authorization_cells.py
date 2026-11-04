"""Authorization cells get disposable targets without changing their principal."""

import copy
import io
import json
import os
import unittest
from unittest.mock import patch

from test_hooks_create_certificate import names_in
from test_hooks_two_factor_admin import load_hooks, session, transaction


class AuthorizationCellHooks(unittest.TestCase):
    def setUp(self):
        self.module, self.stub, self.registered = load_hooks()
        self.environment = patch.dict(os.environ, {"CONTRACT_AUTHORIZATION": "1"})
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def request(self, path, body, status, address=None):
        request = transaction(path, session(address or self.module.ADMIN), json.dumps(body))
        request["expected"] = {"statusCode": str(status)}
        request["name"] = f"{path} > Example > {status} > application/json"
        return request

    def recovery(self):
        return self.request("/api/v1/auth/recover/email", {"email": "unknown@example.test"}, 200)

    def certificate(self):
        return self.request(
            "/api/v1/admin/certificates",
            {"domain_id": 34, "cert_pem": "certificate", "key_pem": "key", "auto_renew": None},
            201,
        )

    def draft(self, address=None):
        return self.request(
            "/api/v1/messages/draft/12?keep=yes",
            {"from": self.module.ADMIN, "to": [self.module.ADMIN], "subject": "Draft, edited"},
            200,
            address,
        )

    def test_registered_before_certificate_material_is_generated(self):
        self.assertEqual(self.registered["requestRecoveryEmail"], ["a_fresh_recovery_address"])
        certificate_hooks = self.registered["createCertificate"]
        self.assertLess(
            certificate_hooks.index("a_domain_for_the_authorization_certificate"),
            certificate_hooks.index("a_self_signed_certificate"),
        )
        self.assertIn("a_draft_for_the_authorization_admin", self.registered["updateDraft"])

    def test_each_public_recovery_cell_gets_a_different_unknown_address(self):
        addresses = []
        credentials = [None, session(self.module.ADMIN), session("ordinary@restmail.test"), "Basic app"]
        for credential in credentials:
            request = self.recovery()
            request["request"]["headers"] = {"Authorization": credential} if credential else {}
            headers = copy.deepcopy(request["request"]["headers"])
            expected = copy.deepcopy(request["expected"])
            self.module.a_fresh_recovery_address(request)
            addresses.append(json.loads(request["request"]["body"])["email"])
            self.assertEqual(request["request"]["headers"], headers)
            self.assertEqual(request["expected"], expected)
        self.assertEqual(len(set(addresses)), len(credentials))
        self.assertTrue(all(address.endswith("@example.test") for address in addresses))
        self.assertNotIn("unknown@example.test", addresses)

    def test_recovery_refusals_and_nonplaceholder_bodies_are_unchanged(self):
        for status, body in ((429, {"email": "unknown@example.test"}), (422, {"email": 3}),
                             (200, {"email": "person@example.test"}), (200, [])):
            with self.subTest(status=status, body=body):
                request = self.request("/api/v1/auth/recover/email", body, status)
                before = copy.deepcopy(request)
                self.module.a_fresh_recovery_address(request)
                self.assertEqual(request, before)

    def test_certificate_uses_a_new_real_domain_and_matching_san(self):
        request = self.certificate()
        before = copy.deepcopy(request)
        domain = {"id": 72, "name": "contract-certificate.test"}
        self.stub.stash.update({"domain_id": 34, "domain": "deleted.test"})
        with patch.object(self.module, "fixture_call", return_value=domain) as setup:
            self.module.a_domain_for_the_authorization_certificate(request)
            setup.assert_called_once()
            self.assertEqual(setup.call_args.args[1:3], ("POST", "/api/v1/admin/domains"))
            self.assertEqual(setup.call_args.args[3]["server_type"], "traditional")
            self.module.a_self_signed_certificate(request)
        body = json.loads(request["request"]["body"])
        self.assertEqual(body["domain_id"], 72)
        self.assertEqual(names_in(body["cert_pem"]), [domain["name"]])
        self.assertIsNone(body["auto_renew"])
        self.assertEqual(request["expected"], before["expected"])
        self.assertEqual(request["request"]["headers"], before["request"]["headers"])
        self.assertEqual(self.stub.stash["domain_id"], 34)

    def test_draft_is_created_as_the_same_admin_for_every_cell(self):
        for address in (self.module.ADMIN, self.module.mfa_admin.ADDRESS):
            with self.subTest(address=address):
                request = self.draft(address)
                before = copy.deepcopy(request)
                with patch.object(self.module.urllib.request, "urlopen", return_value=io.BytesIO(
                    json.dumps({"data": {"id": 91}}).encode()
                )) as setup:
                    self.module.a_draft_for_the_authorization_admin(request)
                sent = setup.call_args.args[0]
                self.assertEqual(sent.get_header("Authorization"), session(address))
                self.assertEqual(sent.method, "POST")
                self.assertEqual(sent.full_url, "http://api:8080/api/v1/messages/draft")
                self.assertEqual(json.loads(sent.data)["from"], self.module.ADMIN)
                self.assertEqual(request["fullPath"], "/api/v1/messages/draft/91?keep=yes")
                self.assertEqual(request["request"], before["request"])
                self.assertEqual(request["expected"], before["expected"])

    def test_draft_setup_failure_is_not_hidden_or_skipped(self):
        request = self.draft()
        before = copy.deepcopy(request)
        with patch.object(self.module.urllib.request, "urlopen", side_effect=OSError("setup failed")):
            with self.assertRaisesRegex(OSError, "setup failed"):
                self.module.a_draft_for_the_authorization_admin(request)
        self.assertEqual(request, before)

    def test_denied_principals_and_refusals_keep_their_targets(self):
        for factory, hook in (
            (self.certificate, self.module.a_domain_for_the_authorization_certificate),
            (self.draft, self.module.a_draft_for_the_authorization_admin),
        ):
            for credential in (None, session("ordinary@restmail.test"), "Basic app"):
                request = factory()
                request["request"]["headers"] = {"Authorization": credential} if credential else {}
                before = copy.deepcopy(request)
                hook(request)
                self.assertEqual(request, before)
            for status in (401, 403, 404, 422):
                request = factory()
                request["expected"]["statusCode"] = str(status)
                before = copy.deepcopy(request)
                hook(request)
                self.assertEqual(request, before)

    def test_examples_and_coverage_probes_are_byte_for_byte_unchanged(self):
        with patch.dict(os.environ, {"CONTRACT_AUTHORIZATION": "", "CONTRACT_COVERAGE": "1"}):
            for factory, hook in (
                (self.recovery, self.module.a_fresh_recovery_address),
                (self.certificate, self.module.a_domain_for_the_authorization_certificate),
                (self.draft, self.module.a_draft_for_the_authorization_admin),
            ):
                request = factory()
                before = copy.deepcopy(request)
                hook(request)
                self.assertEqual(request, before)


if __name__ == "__main__":
    unittest.main()
