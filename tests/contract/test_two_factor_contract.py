"""Own-2FA schema, ordinary contract coverage and complete admin route inventory."""

import importlib.util
from pathlib import Path
import re
import sys
import types
import unittest
from unittest.mock import patch
import urllib.parse

import yaml

ROOT = Path(__file__).resolve().parents[2]
METHODS = {"get", "post", "put", "patch", "delete"}
STATUS = "/api/v1/auth/2fa"
ENROLL = STATUS + "/enroll"


def included(path):
    return path.startswith("/api/v1/admin/") or path == STATUS or path.startswith(STATUS + "/")


class TwoFactorContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.spec = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        cls.config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        cls.patterns = [re.compile(p) for p in cls.config["exclude-matching"]]

    def test_entire_admin_and_two_factor_registration_matches_spec_both_ways(self):
        routes = (ROOT / "internal/api/routes.go").read_text()
        registered = {(method.upper(), path) for method, path in
                      re.findall(r'\.\s*(Get|Post|Put|Patch|Delete)\s*\(\s*"([^"\n]+)"', routes)
                      if included(path)}
        # Routes registered with endpoint.Register name their method and path in an
        # endpoint.Operation, beside their handler (#669).
        for source in (ROOT / "internal/api").rglob("*.go"):
            if source.name.endswith("_test.go"):
                continue
            for method, path in re.findall(
                    r'Method:\s*http\.Method(Get|Post|Put|Patch|Delete),\s*Path:\s*"([^"\n]+)"',
                    source.read_text()):
                if included(path):
                    registered.add((method.upper(), path))
        documented = {(method.upper(), path) for path, item in self.spec["paths"].items()
                      if included(path) for method in METHODS if method in item}
        self.assertTrue(registered, "empty registration scan")
        self.assertEqual(registered, documented)
        ids = [self.spec["paths"][path][method.lower()]["operationId"] for method, path in documented]
        self.assertEqual(len(ids), len(set(ids)), "operation ids must be unique")

    def test_new_successes_and_unauthenticated_refusals_are_counted(self):
        never = set(self.config["exclude-operation-id"])
        uncounted = {e["operation"] for e in self.config["surface"]["exclude"]
                     if "operation" in e and "status" not in e}
        for path, method, operation in [(STATUS, "get", "getTwoFactorStatus"),
                                        (ENROLL, "post", "enrollTwoFactor")]:
            op = self.spec["paths"][path][method]
            self.assertEqual(op["operationId"], operation)
            self.assertNotIn(operation, never)
            self.assertNotIn(operation, uncounted)
            self.assertNotIn("requestBody", op)
            for status in ("200", "401"):
                name = f"{path} > {op['summary']} > {status} > application/json"
                self.assertFalse(any(p.search(name) for p in self.patterns), name)
                if status == "401":
                    self.assertIn(name, self.config["auth"]["except"])

    def test_only_new_fault_responses_are_excluded(self):
        narrow = re.compile(r"^/api/v1/auth/2fa(/enroll)? > .* > 500( >|$)")
        self.assertIn(narrow.pattern, self.config["exclude-matching"])
        for path in (STATUS, ENROLL):
            self.assertTrue(narrow.search(f"{path} > Example > 500 > application/json"))
        for path in (STATUS + "/confirm", STATUS + "/disable", "/api/v1/admin/domains"):
            self.assertIsNone(narrow.search(f"{path} > Example > 500 > application/json"))
        for operation in ("getTwoFactorStatus", "enrollTwoFactor"):
            entries = [e for e in self.config["surface"]["exclude"] if e.get("operation") == operation]
            self.assertEqual([e.get("status") for e in entries], ["500"])

    def test_every_two_factor_operation_requires_only_a_person_session(self):
        for path, item in self.spec["paths"].items():
            if path != STATUS and not path.startswith(STATUS + "/"):
                continue
            for method in METHODS & item.keys():
                self.assertEqual(item[method]["security"], [{"CookieAuth": []}, {"BearerAuth": []}])

    def test_enrollment_pacing_never_rewrites_the_principal(self):
        hooks = types.ModuleType("vertrag_hooks")
        registered = []
        hooks.before = lambda pattern: lambda function: registered.append((pattern, function)) or function
        hooks.after = lambda pattern: lambda function: function
        spec = importlib.util.spec_from_file_location("two_factor_contract_hooks", ROOT / "tests/contract/hooks.py")
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(module)
        selected = [function for pattern, function in registered if pattern == "enrollTwoFactor"]
        self.assertEqual(selected, [module.pace_auth_requests])
        self.assertFalse(any(pattern == "getTwoFactorStatus" for pattern, _ in registered))

    def test_response_examples_and_required_fields(self):
        want = {
            STATUS: ("get", "TwoFactorStatus", {"enabled", "pending"}),
            ENROLL: ("post", "TwoFactorEnrollment", {"secret", "otpauth_url", "recovery_codes"}),
        }
        for path, (method, name, fields) in want.items():
            response = self.spec["paths"][path][method]["responses"]["200"]["content"]["application/json"]
            self.assertEqual(response["schema"]["properties"]["data"]["$ref"], f"#/components/schemas/{name}")
            schema = self.spec["components"]["schemas"][name]
            self.assertEqual(set(schema["properties"]), fields)
            self.assertEqual(set(schema["required"]), fields)
            self.assertIs(schema["additionalProperties"], False)
            self.assertEqual(set(response["example"]["data"]), fields)
        enrollment = self.spec["paths"][ENROLL]["post"]["responses"]["200"]["content"]["application/json"]["example"]["data"]
        self.assertIsInstance(enrollment["secret"], str)
        self.assertEqual(urllib.parse.urlsplit(enrollment["otpauth_url"]).scheme, "otpauth")
        self.assertTrue(all(isinstance(code, str) for code in enrollment["recovery_codes"]))

    def test_all_local_schema_references_resolve(self):
        def visit(node):
            if isinstance(node, dict):
                ref = node.get("$ref", "")
                if ref.startswith("#/"):
                    value = self.spec
                    for part in ref[2:].split("/"):
                        key = part.replace("~1", "/").replace("~0", "~")
                        self.assertIn(key, value, ref)
                        value = value[key]
                for value in node.values():
                    visit(value)
            elif isinstance(node, list):
                for value in node:
                    visit(value)
        visit(self.spec)


if __name__ == "__main__":
    unittest.main()
