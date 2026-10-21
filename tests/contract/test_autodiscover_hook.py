"""Autodiscover's XML hook sends the request paired with each response."""

import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


class ContractAutodiscoverHookTests(unittest.TestCase):
    def test_hook_sends_the_xml_named_on_each_response(self):
        hooks = types.ModuleType("vertrag_hooks")
        registered = []
        hooks.before = lambda pattern: lambda function: registered.append(pattern) or function
        hooks.after = lambda pattern: lambda function: function
        spec = importlib.util.spec_from_file_location("contract_autodiscover_hooks", ROOT / "tests/contract/hooks.py")
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(module)
        self.assertIn("microsoftAutodiscover", registered)

        with open(ROOT / "openapi.yaml") as f:
            operation = yaml.safe_load(f)["paths"]["/autodiscover/autodiscover.xml"]["post"]
        requests = operation["requestBody"]["content"]["application/xml"]["examples"]
        # XML examples are paired but have no serialized body in vertrag 0.29.
        # Coverage probes use the same transaction name prefixed by a phase.
        for prefix in ("", "coverage: "):
            for status, media_type in (("200", "application/xml"), ("400", "text/plain")):
                responses = operation["responses"][status]["content"][media_type]["examples"]
                self.assertEqual(len(responses), 1)
                example_name = next(iter(responses))
                with self.subTest(phase=prefix, status=status, example=example_name):
                    transaction = {
                        "name": "%s/autodiscover/autodiscover.xml > %s > %s > %s" % (
                            prefix, operation["summary"], status, media_type),
                        "request": {"body": "", "headers": {"Content-Type": "application/xml"}},
                    }
                    module.an_outlook_autodiscover_request(transaction)
                    self.assertEqual(transaction["request"]["body"], requests[example_name]["value"])
                    self.assertEqual(transaction["request"]["headers"]["Content-Type"], "application/xml")


if __name__ == "__main__":
    unittest.main()
