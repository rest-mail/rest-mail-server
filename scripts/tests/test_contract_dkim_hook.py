"""DKIM setup substitutes only the documented private-key placeholder."""

import copy
import importlib.util
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


class ContractDKIMHookTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = hooks.after = lambda *args: lambda function: function
        hooks.stash = {}
        hooks.get_json = lambda transaction: json.loads(transaction["request"]["body"])
        hooks.set_json = lambda transaction, body: transaction["request"].update(body=json.dumps(body))
        spec = importlib.util.spec_from_file_location("contract_dkim_hooks", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)
        with (ROOT / "openapi.yaml").open() as source:
            operation = yaml.safe_load(source)["paths"]["/api/v1/admin/dkim/{id}"]["put"]
        self.examples = operation["requestBody"]["content"]["application/json"]["examples"]

    def transaction(self, body):
        return {"request": {"body": json.dumps(body), "headers": {"Authorization": "Bearer caller"}}}

    def test_spec_examples_keep_every_field_except_the_placeholder(self):
        def generate(*args):
            self.assertEqual(args[:2], ("genrsa", "-out"))
            Path(args[2]).write_text("fresh private key")

        with patch.object(self.module, "openssl", side_effect=generate) as openssl:
            for name, example in self.examples.items():
                with self.subTest(example=name):
                    body = copy.deepcopy(example["value"])
                    transaction = self.transaction(body)
                    expected = copy.deepcopy(body)
                    before_calls = openssl.call_count
                    if isinstance(expected, dict) and expected.get("private_key") == "fixture-signing-key":
                        expected["private_key"] = "fresh private key"
                    self.module.a_fresh_signing_key(transaction)
                    self.assertEqual(json.loads(transaction["request"]["body"]), expected)
                    self.assertEqual(openssl.call_count - before_calls, int(expected != body))
                    self.assertEqual(transaction["request"]["headers"], {"Authorization": "Bearer caller"})

    def test_coverage_mutations_are_not_repaired(self):
        bodies = [None, [], "key", 42, {}, {"selector": "contract"}]
        for value in [None, "", 0, False, [], {}, "not a key"]:
            bodies.append({"selector": "contract", "private_key": value})
        with patch.object(self.module, "openssl") as openssl:
            for body in bodies:
                with self.subTest(body=body):
                    transaction = self.transaction(body)
                    before = copy.deepcopy(transaction)
                    self.module.a_fresh_signing_key(transaction)
                    self.assertEqual(transaction, before)
            openssl.assert_not_called()

    def test_generated_key_is_scoped_and_hook_leaves_it_unchanged(self):
        with (ROOT / "vertrag.yml").open() as source:
            config = yaml.safe_load(source)
        self.assertFalse(config.get("fuzz", {}).get("pin"), "named error examples must not be pinned")
        pin = {"private_key": (ROOT / "tests/contract/fixtures/dkim-key.pem").read_text()}
        with (ROOT / "openapi.yaml").open() as source:
            spec = yaml.safe_load(source)
        # Pins match declared property names globally. Catch any new schema
        # that would unintentionally share this signing-key fixture.
        owners = {name for name, schema in spec["components"]["schemas"].items()
                  if "private_key" in schema.get("properties", {})}
        self.assertEqual(owners, {"SetDKIMKeyRequest"})
        for selector in ["x", "", "invalid_selector", "x" * 64, 42]:
            with self.subTest(selector=selector):
                transaction = self.transaction({"selector": selector, **pin})
                before = copy.deepcopy(transaction)
                with patch.object(self.module, "openssl") as openssl:
                    self.module.a_fresh_signing_key(transaction)
                    openssl.assert_not_called()
                self.assertEqual(transaction, before)


if __name__ == "__main__":
    unittest.main()
