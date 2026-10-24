"""The missing-id 404s are paired by their parameter's name (#625).

Since vertrag 0.30 a path parameter example a response is named after is a request
of its own (antimatter-studios/vertrag#85): `missing` is sent with its id against
the 404 alone, and no link replaces it. The hooks that used to select the missing
id are gone; this holds the description to what vertrag pairs.
"""

import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
OPERATIONS = {
    "getDomain": "/api/v1/admin/domains/{id}",
    "getCertificate": "/api/v1/admin/certificates/{id}",
    "getCustomFilter": "/api/v1/admin/custom-filters/{id}",
    "getAlias": "/api/v1/admin/aliases/{id}",
}


class MissingIsNamed(unittest.TestCase):
    def setUp(self):
        with open(ROOT / "openapi.yaml") as file:
            self.spec = yaml.safe_load(file)

    def test_the_404_is_named_after_the_missing_id(self):
        for operation_id, path in OPERATIONS.items():
            with self.subTest(operation=operation_id):
                operation = self.spec["paths"][path]["get"]
                self.assertEqual(operation["operationId"], operation_id)
                parameter = operation["parameters"][0]
                self.assertEqual(parameter["name"], "id")
                self.assertEqual(parameter["examples"]["missing"]["value"], 2147483647)
                named = {status: set((response.get("content", {}).get("application/json", {}).get("examples") or {}))
                         for status, response in operation["responses"].items() if "content" in response}
                self.assertEqual([status for status, names in named.items() if "missing" in names], ["404"])

    def test_no_hook_rewrites_the_path(self):
        registered = {}
        hooks = types.ModuleType("vertrag_hooks")
        hooks.stash = {}

        def before(*names):
            def register(function):
                for name in names:
                    registered.setdefault(name, []).append(function)
                return function
            return register
        hooks.before = before
        hooks.after = lambda *args: lambda function: function
        spec = importlib.util.spec_from_file_location("contract_hooks", ROOT / "tests/contract/hooks.py")
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(module)
        for operation_id, path in OPERATIONS.items():
            for hook in registered.get(operation_id, []):
                with self.subTest(operation=operation_id, hook=hook.__name__):
                    full_path = path.replace("{id}", "2147483647")
                    transaction = {"name": "%s > %s > 404 > application/json" % (path, operation_id),
                                   "fullPath": full_path, "protocol": "http:", "host": "fixture", "port": 8080,
                                   "request": {"headers": {}}}
                    hook(transaction)
                    self.assertEqual(transaction["fullPath"], full_path)

if __name__ == "__main__":
    unittest.main()
