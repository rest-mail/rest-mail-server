"""Discovery prepares its own active domain without rewriting the probe."""

import copy
import importlib.util
import io
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch
import urllib.error
import urllib.parse


ROOT = Path(__file__).resolve().parents[2]


class AutoconfigFixtureTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.stash = {}
        self.registered = {}

        def before(pattern):
            def register(function):
                self.registered[pattern] = function
                return function
            return register

        hooks.before = before
        hooks.after = lambda pattern: lambda function: function
        spec = importlib.util.spec_from_file_location("contract_autoconfig_fixture", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)
        self.domains = [
            {"id": 1, "name": "restmail.test", "active": True},
            {"id": 2, "name": "example.com", "active": False},
        ]
        self.certificates = {1}
        self.calls = []
        self.cert_bodies = []
        self.openssl_calls = []
        self.activation_error = None
        for target, replacement in (("admin_token", lambda origin: "fixture-setup"),
                                    ("openssl", self.openssl)):
            patcher = patch.object(self.module, target, side_effect=replacement)
            patcher.start()
            self.addCleanup(patcher.stop)
        patcher = patch.object(self.module.urllib.request, "urlopen", side_effect=self.urlopen)
        patcher.start()
        self.addCleanup(patcher.stop)

    def answer(self, status, body):
        response = io.BytesIO(json.dumps(body).encode())
        response.status = status
        return response

    def refuse(self, request, status, code):
        raise urllib.error.HTTPError(request.full_url, status, code, {},
                                     io.BytesIO(json.dumps({"error": {"code": code}}).encode()))

    def urlopen(self, request, timeout):
        self.assertEqual(timeout, 10)
        self.assertEqual(request.get_header("Authorization"), "Bearer fixture-setup")
        self.calls.append(request)
        path = urllib.parse.urlsplit(request.full_url).path
        method = request.get_method()
        if path == "/api/v1/admin/domains":
            if method == "GET":
                return self.answer(200, {"data": self.domains})
            body = json.loads(request.data)
            domain = {"id": len(self.domains) + 1, "name": body["name"], "active": False}
            self.domains.append(domain)
            return self.answer(201, {"data": domain})
        if path == "/api/v1/admin/certificates":
            body = json.loads(request.data)
            self.cert_bodies.append(body)
            self.certificates.add(body["domain_id"])
            return self.answer(201, {"data": {"id": len(self.cert_bodies)}})
        if path.startswith("/api/v1/admin/domains/"):
            domain_id = int(path.rsplit("/", 1)[1])
            domain = next(domain for domain in self.domains if domain["id"] == domain_id)
            if method == "GET":
                return self.answer(200, {"data": domain})
            self.assertEqual(method, "PATCH")
            self.assertEqual(json.loads(request.data), {"active": True})
            if self.activation_error:
                return self.refuse(request, *self.activation_error)
            if domain_id not in self.certificates:
                return self.refuse(request, 409, "certificate_required")
            domain["active"] = True
            return self.answer(200, {"data": domain})
        raise AssertionError("unexpected fixture request: %s %s" % (method, path))

    def openssl(self, *args):
        self.openssl_calls.append(args)
        Path(args[args.index("-keyout") + 1]).write_text("fixture-key")
        Path(args[args.index("-out") + 1]).write_text("fixture-certificate")

    def probe(self, address, operation="mozillaAutoconfig", headers=None):
        path = "/mail/config-v1.1.xml"
        if operation == "mozillaAutoconfigWellKnown":
            path = "/.well-known/autoconfig" + path
        transaction = {
            "protocol": "http:", "host": "fixture.test", "port": "8080",
            "fullPath": path + "?" + urllib.parse.urlencode({"emailaddress": address}),
            "request": {"headers": {"Authorization": "Bearer caller"} if headers is None else headers,
                        "body": ""},
        }
        before = copy.deepcopy(transaction)
        self.registered[operation](transaction)
        self.assertEqual(transaction, before)

    def test_both_discovery_paths_prepare_the_generated_domain(self):
        for operation in ("mozillaAutoconfig", "mozillaAutoconfigWellKnown"):
            self.probe("someone@example.com", operation)
        self.assertTrue(self.domains[1]["active"])
        self.assertEqual(len(self.cert_bodies), 1)
        self.assertEqual(self.cert_bodies[0], {
            "domain_id": 2, "cert_pem": "fixture-certificate", "key_pem": "fixture-key", "auto_renew": False,
        })
        self.assertIn("subjectAltName=DNS:example.com", self.openssl_calls[0])

    def test_active_domain_is_not_changed_and_public_principal_is_preserved(self):
        self.probe("admin@restmail.test", headers={})
        self.assertTrue(all(request.get_method() == "GET" for request in self.calls))
        self.assertEqual(self.openssl_calls, [])

    def test_existing_certificate_is_reused(self):
        self.certificates.add(2)
        self.probe("someone@example.com")
        self.assertTrue(self.domains[1]["active"])
        self.assertEqual(self.openssl_calls, [])

    def test_rechecks_state_after_a_domain_update_or_certificate_deletion(self):
        self.probe("someone@example.com")
        self.domains[1]["active"] = False
        self.probe("someone@example.com")
        self.assertEqual(len(self.cert_bodies), 1)
        self.domains[1]["active"] = False
        self.certificates.remove(2)
        self.probe("someone@example.com")
        self.assertEqual(len(self.cert_bodies), 2)

    def test_missing_domain_is_hosted_and_activated(self):
        self.probe("person@other.test")
        self.assertEqual(self.domains[-1], {"id": 3, "name": "other.test", "active": True})
        self.assertIn("subjectAltName=DNS:other.test", self.openssl_calls[0])

    def test_malformed_queries_are_left_to_the_probe(self):
        for address in ("", "not-a-email", "person@localhost", "@example.com", "a@b@example.com"):
            with self.subTest(address=address):
                self.probe(address)
        self.assertEqual(self.calls, [])

    def test_other_activation_failures_are_reported(self):
        for status, code in ((403, "forbidden"), (409, "another_conflict"), (500, "internal_error")):
            with self.subTest(status=status):
                self.activation_error = (status, code)
                with self.assertRaises(urllib.error.HTTPError):
                    self.probe("someone@example.com")
        self.assertEqual(self.openssl_calls, [])


if __name__ == "__main__":
    unittest.main()
