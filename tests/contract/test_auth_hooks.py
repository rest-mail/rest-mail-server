"""Authentication hooks preserve the spec's paired refusals and probes."""

import copy
import importlib.util
import json
from pathlib import Path
import re
import sys
import types
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


class ContractAuthHookTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = lambda pattern: lambda function: function
        hooks.after = lambda pattern: lambda function: function
        hooks.get_json = lambda transaction, source=None: json.loads(
            transaction["real" if source == "real" else "request"]["body"])
        hooks.set_json = lambda transaction, body: transaction["request"].update(body=json.dumps(body))
        hooks.stash = {"refresh_token": "live-fixture-token"}
        spec = importlib.util.spec_from_file_location("contract_auth_hooks", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)
        self.hooks = hooks
        with open(ROOT / "openapi.yaml") as f:
            self.paths = yaml.safe_load(f)["paths"]

    def apply(self, hook, body):
        transaction = {"request": {"body": json.dumps(body)}}
        hook(transaction)
        return json.loads(transaction["request"]["body"])

    def test_login_examples_replace_only_the_fixture_password(self):
        op = self.paths["/api/v1/auth/login"]["post"]
        for name, example in op["requestBody"]["content"]["application/json"]["examples"].items():
            with self.subTest(example=name):
                expected = copy.deepcopy(example["value"])
                if expected.get("password") == "fixture-password":
                    expected["password"] = self.module.ADMIN_PASSWORD
                self.assertEqual(self.apply(self.module.sign_in_as_the_fixture_admin, example["value"]), expected)
        self.assertNotEqual(op["requestBody"]["content"]["application/json"]["examples"]["wrong_password"]["value"]["password"],
                            self.module.ADMIN_PASSWORD)

    def test_refresh_examples_replace_only_the_runtime_token(self):
        op = self.paths["/api/v1/auth/refresh"]["post"]
        for name, example in op["requestBody"]["content"]["application/json"]["examples"].items():
            with self.subTest(example=name):
                expected = copy.deepcopy(example["value"])
                if expected.get("refresh_token") == "fixture-refresh-token":
                    expected["refresh_token"] = "live-fixture-token"
                self.assertEqual(self.apply(self.module.refresh_the_sign_in, example["value"]), expected)

    def test_hooks_preserve_coverage_mutations(self):
        for hook in (self.module.sign_in_as_the_fixture_admin, self.module.refresh_the_sign_in):
            for body in ([], None, {}, {"email": "bad"}, {"password": None},
                         {"password": 42}, {"refresh_token": None}, {"refresh_token": 42}):
                with self.subTest(hook=hook.__name__, body=body):
                    self.assertEqual(self.apply(hook, body), body)
        body = {"password": "fixture-password", "client": "desktop"}
        self.assertEqual(self.apply(self.module.sign_in_as_the_fixture_admin, body),
                         {"password": self.module.ADMIN_PASSWORD, "client": "desktop"})

    def test_refusals_do_not_overwrite_the_saved_refresh_token(self):
        for path, after in (("/api/v1/auth/login", self.module.keep_the_refresh_token),
                            ("/api/v1/auth/refresh", self.module.keep_the_refreshed_token)):
            op = self.paths[path]["post"]
            for status in ("401", "422"):
                if status not in op["responses"]:
                    continue
                for example in op["responses"][status]["content"]["application/json"]["examples"].values():
                    after({"real": {"body": json.dumps(example["value"])}})
                    self.assertEqual(self.hooks.stash["refresh_token"], "live-fixture-token")
            after({"real": {"body": json.dumps({"data": {"refresh_token": "rotated-token"}})}})
            self.assertEqual(self.hooks.stash["refresh_token"], "rotated-token")
            self.hooks.stash["refresh_token"] = "live-fixture-token"

    def test_documented_auth_refusals_are_sent_and_paired(self):
        with open(ROOT / "vertrag.yml") as f:
            config = yaml.safe_load(f)
        patterns = [re.compile(p) for p in config["exclude-matching"]]
        for path in ("/api/v1/auth/login", "/api/v1/auth/refresh"):
            op = self.paths[path]["post"]
            requests = op["requestBody"]["content"]["application/json"]
            self.assertNotIn("example", requests)
            for status in ("401", "422"):
                if status not in op["responses"]:
                    continue
                name = "%s > %s > %s > application/json" % (path, op["summary"], status)
                self.assertFalse(any(p.search(name) for p in patterns), name)
                response = op["responses"][status]["content"]["application/json"]
                self.assertNotIn("example", response)
                self.assertTrue(response["examples"])
                self.assertLessEqual(set(response["examples"]), set(requests["examples"]))
            self.assertNotIn({"operation": op["operationId"], "status": "401"},
                             [{"operation": e.get("operation"), "status": e.get("status")}
                              for e in config["surface"]["exclude"]])


if __name__ == "__main__":
    unittest.main()
