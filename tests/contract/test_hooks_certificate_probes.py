"""createCertificate's probes hold throwaway material (probes.py) for their isolated
domain, and a hook cannot change a generated body. So they go out as drawn, and what follows
them removes what they installed. testFilter's probes are not touched at all."""

import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import types
import unittest
from unittest.mock import patch

import probes

ROOT = Path(__file__).resolve().parents[2]
CREATE = "/api/v1/admin/certificates > Upload a new TLS certificate > 201 > application/json"
ORIGIN = "http://fixture.test:8080"


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    registered = {}
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}

    def before(name):
        def register(fn):
            registered.setdefault(name, []).append(fn)
            return fn

        return register

    stub.before = before
    stub.after = lambda name: (lambda fn: fn)
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])

    def set_json(transaction, value):
        transaction["request"]["body"] = json.dumps(value)

    stub.set_json = set_json
    spec = importlib.util.spec_from_file_location("contract_certificate_probe_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, stub, registered


def transaction(body, name=CREATE, operation="createCertificate"):
    return {
        "name": name,
        "operationId": operation,
        "protocol": "http:",
        "host": "fixture.test",
        "port": 8080,
        "fullPath": "/api/v1/admin/certificates",
        "request": {"body": json.dumps(body)},
    }


class Answer(io.BytesIO):
    status = 200

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


class CertificateProbes(unittest.TestCase):
    def setUp(self):
        self.hooks, self.stub, self.registered = load_hooks()
        self.stub.stash["domain"] = "run.test"
        self.stub.stash["domain_id"] = 7
        certificate, key = probes.probe_certificate()
        self.pinned = {"domain_id": 1, "cert_pem": certificate, "key_pem": key}
        self.calls = []
        self.pages = {
            0: [
                {"id": 3, "issuer": "CN=run.test"},
                {"id": 4, "issuer": self.hooks.PROBE_CERTIFICATE_ISSUER},
            ],
            2: [{"id": 5, "issuer": self.hooks.PROBE_CERTIFICATE_ISSUER}],
        }
        for patcher in (
            patch.object(self.hooks, "admin_token", lambda origin: "setup"),
            patch("urllib.request.urlopen", self.answer),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)

    def answer(self, request, timeout=None):
        path = request.full_url.removeprefix(ORIGIN)
        self.calls.append((request.get_method(), path))
        if request.get_method() == "GET":
            domains = {
                "/api/v1/admin/domains/1": {"id": 1, "name": "example.org"},
                "/api/v1/admin/domains/7": {"id": 7, "name": "run.test"},
            }
            if path in domains:
                return Answer(json.dumps({"data": domains[path]}).encode())
            if not path.startswith("/api/v1/admin/certificates?limit=100&offset="):
                raise AssertionError("unexpected fixture read: " + path)
            offset = int(path.rsplit("offset=", 1)[1])
            return Answer(json.dumps({"data": self.pages.get(offset, [])}).encode())
        return Answer(b"")

    def read_pem(self, arguments, pem):
        return subprocess.run(
            ["openssl", *arguments], input=pem, check=True, capture_output=True, text=True
        ).stdout

    def test_the_hook_knows_the_issuer_of_the_pinned_certificate(self):
        issuer = self.read_pem(["x509", "-noout", "-issuer", "-nameopt", "RFC2253"], self.pinned["cert_pem"])
        self.assertEqual(issuer.strip().removeprefix("issuer="), self.hooks.PROBE_CERTIFICATE_ISSUER)

    def test_the_pinned_certificate_is_current_and_only_names_its_isolated_domain(self):
        checked = subprocess.run(
            ["openssl", "x509", "-noout", "-checkend", "0"],
            input=self.pinned["cert_pem"], capture_output=True, text=True,
        )
        self.assertEqual(checked.returncode, 0, checked.stdout)
        plan = json.loads(probes.FIXTURE_PLAN.read_text())
        names = self.read_pem(["x509", "-noout", "-ext", "subjectAltName"], self.pinned["cert_pem"])
        self.assertEqual(names.splitlines()[1].strip(), "DNS:" + plan["certificate_domain"]["name"])
        for domain in (plan["domain"], plan["delivery_recipient"]["address"].split("@", 1)[1]):
            matched = subprocess.run(
                ["openssl", "x509", "-noout", "-checkhost", domain],
                input=self.pinned["cert_pem"], capture_output=True, text=True,
            )
            self.assertEqual(matched.stdout.splitlines()[-1], f"Hostname {domain} does NOT match certificate")

    def test_the_pinned_key_belongs_to_the_certificate(self):
        self.assertEqual(
            self.read_pem(["x509", "-noout", "-pubkey"], self.pinned["cert_pem"]),
            self.read_pem(["pkey", "-pubout"], self.pinned["key_pem"]),
        )

    def test_a_documented_placeholder_gets_material_of_its_own_on_the_runs_domain(self):
        sent = transaction({"domain_id": 1, "cert_pem": "certificate", "key_pem": "key", "auto_renew": False})
        self.hooks.a_self_signed_certificate(sent)
        body = json.loads(sent["request"]["body"])
        self.assertEqual(body["domain_id"], 7)
        self.assertNotIn(body["cert_pem"], (self.pinned["cert_pem"], "certificate"))
        self.assertFalse(body["auto_renew"])
        self.assertFalse(self.stub.stash.get("probe_certificates_installed"))

    def test_the_documented_sends_of_a_probing_process_get_material_of_their_own(self):
        for _ in range(self.hooks.DOCUMENTED_SENDS):
            sent = transaction(self.pinned)
            self.hooks.a_self_signed_certificate(sent)
            body = json.loads(sent["request"]["body"])
            self.assertEqual(body["domain_id"], 7)
            self.assertNotEqual(body["cert_pem"], self.pinned["cert_pem"])
        self.assertFalse(self.stub.stash.get("probe_certificates_installed"))

    def test_a_probe_goes_out_as_drawn_and_is_remembered(self):
        for _ in range(self.hooks.DOCUMENTED_SENDS):
            self.hooks.a_self_signed_certificate(transaction(self.pinned))
        domain_id = json.loads(probes.FIXTURE_PLAN.read_text())["certificate_domain"]["id"]
        for body in ({**self.pinned, "domain_id": domain_id}, {**self.pinned, "auto_renew": True}, {**self.pinned, "vertragUnexpected": "extra"}):
            sent = transaction(body)
            drawn = sent["request"]["body"]
            self.hooks.a_self_signed_certificate(sent)
            self.assertEqual(sent["request"]["body"], drawn)
        self.assertTrue(self.stub.stash["probe_certificates_installed"])

    def test_a_refused_probe_is_left_alone(self):
        for body in (
            {"domain_id": 0, "cert_pem": "x", "key_pem": "x"},
            {"domain_id": "vertrag-not-a-number", "cert_pem": "x", "key_pem": "x"},
            {"domain_id": 1.5, "cert_pem": "x", "key_pem": "x"},
            {"cert_pem": "x", "key_pem": "x"},
        ):
            sent = transaction(body)
            drawn = sent["request"]["body"]
            self.hooks.a_self_signed_certificate(sent)
            self.assertEqual(sent["request"]["body"], drawn)
        self.assertFalse(self.stub.stash.get("probe_certificates_installed"))

    def test_what_follows_the_probes_removes_only_their_certificates(self):
        self.stub.stash["probe_certificates_installed"] = True
        self.hooks.remove_the_probe_certificates(transaction({}, name="next", operation="getCertificate"))
        self.assertEqual(
            self.calls,
            [
                ("GET", "/api/v1/admin/certificates?limit=100&offset=0"),
                ("GET", "/api/v1/admin/certificates?limit=100&offset=2"),
                ("GET", "/api/v1/admin/certificates?limit=100&offset=3"),
                ("DELETE", "/api/v1/admin/certificates/4"),
                ("DELETE", "/api/v1/admin/certificates/5"),
            ],
        )
        self.assertFalse(self.stub.stash["probe_certificates_installed"])

    def test_the_removal_waits_for_the_probes_and_runs_once(self):
        self.hooks.remove_the_probe_certificates(transaction({}, name="next", operation="getCertificate"))
        self.assertEqual(self.calls, [])
        self.stub.stash["probe_certificates_installed"] = True
        self.hooks.remove_the_probe_certificates(transaction(self.pinned))
        self.assertEqual(self.calls, [])
        self.assertTrue(self.stub.stash["probe_certificates_installed"])
        self.hooks.remove_the_probe_certificates(transaction({}, name="next", operation="getCertificate"))
        count = len(self.calls)
        self.hooks.remove_the_probe_certificates(transaction({}, name="next", operation="getCertificate"))
        self.assertEqual(len(self.calls), count)

    def test_the_removal_is_registered_for_every_operation(self):
        self.assertIn(self.hooks.remove_the_probe_certificates, self.registered["*"])

    def test_no_hook_rewrites_a_test_filter_probe(self):
        for hook in self.registered["testFilter"]:
            self.assertEqual(hook.__name__, "pace_pipeline_previews")


if __name__ == "__main__":
    unittest.main()
