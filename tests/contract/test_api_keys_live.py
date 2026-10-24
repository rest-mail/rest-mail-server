import base64
import hashlib
import os
import unittest

import yaml

import api_keys_live


class TOTPTest(unittest.TestCase):
    # RFC 6238 appendix B, SHA-1 seed, last six digits.
    SECRET = base64.b32encode(b"12345678901234567890").decode().rstrip("=")

    def test_rfc6238_vectors(self):
        for at, want in [(59, "287082"), (1111111109, "081804"), (1234567890, "005924"), (20000000000, "353130")]:
            self.assertEqual(api_keys_live.totp(self.SECRET, at=at), want)

    def test_unpadded_lowercase_secret(self):
        self.assertEqual(api_keys_live.totp(self.SECRET.lower(), at=59), "287082")

    def test_eight_digits(self):
        self.assertEqual(api_keys_live.totp(self.SECRET, at=59, digits=8, algorithm=hashlib.sha1), "94287082")


class GuardTest(unittest.TestCase):
    def test_refuses_other_domains(self):
        for email in ["admin@example.com", "x@restmail.test.evil", "restmail.test@evil.com"]:
            with self.assertRaises(SystemExit):
                api_keys_live.require_fixture_account(email)

    def test_accepts_fixture_domain(self):
        api_keys_live.require_fixture_account("keys@RestMail.test")

    def test_metadata_without_secret(self):
        api_keys_live.assert_no_secret({"id": 1, "name": "k", "permissions": []})
        for field in ("token", "password", "secret"):
            with self.assertRaises(AssertionError):
                api_keys_live.assert_no_secret({"id": 1, field: "rmk_x"})


class RequiredCI(unittest.TestCase):
    def test_separate_suite_is_required_for_exactly_the_three_key_operations(self):
        root = os.path.join(os.path.dirname(__file__), "..", "..")
        with open(os.path.join(root, "vertrag.yml")) as source:
            config = yaml.safe_load(source)
        expected = {"listAPIKeys", "createAPIKey", "revokeAPIKey"}
        excluded = {item["operation"] for item in config["surface"]["exclude"]
                    if "test:contract-api-keys" in item.get("reason", "")}
        self.assertEqual(excluded, expected)
        self.assertLessEqual(expected, set(config["exclude-operation-id"]))
        with open(os.path.join(root, ".github/workflows/e2e-suite.yml")) as source:
            workflow = yaml.safe_load(source)
        steps = [step for step in workflow["jobs"]["suite"]["steps"]
                 if step.get("run") == "chore contract"]
        self.assertEqual(len(steps), 1)
        self.assertEqual(steps[0]["if"], "inputs.contract-phases == 'gate'")
        self.assertFalse(steps[0].get("continue-on-error", False))
        with open(os.path.join(root, "chores.yml")) as source:
            command = yaml.safe_load(source)["tasks"]["contract"]["cmds"][0]
        self.assertIn("set -eu", command)
        self.assertLess(command.index("chore test:contract-api-keys"), command.index("code=0"))

    def test_validation_examples_pair_with_documented_responses(self):
        root = os.path.join(os.path.dirname(__file__), "..", "..")
        with open(os.path.join(root, "openapi.yaml")) as source:
            spec = yaml.safe_load(source)
        operation = spec["paths"]["/api/v1/accounts/{id}/api-keys"]["post"]
        requests = operation["requestBody"]["content"]["application/json"]["examples"]
        refusals = operation["responses"]["422"]["content"]["application/json"]["examples"]
        self.assertEqual(set(refusals), set(requests) - {"created"})
        for example in refusals.values():
            self.assertEqual(example["value"]["error"]["code"], "validation_failed")


if __name__ == "__main__":
    unittest.main()
