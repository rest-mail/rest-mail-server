"""The createPipeline hook sends each refused create without the field it is named for."""

import importlib.util
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


class ContractPipelineHookTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = hooks.after = lambda *args: lambda function: function
        hooks.stash = {"domain_id": 17}
        hooks.get_json = lambda transaction: json.loads(transaction["request"]["body"])

        def set_json(transaction, body):
            transaction["request"]["body"] = json.dumps(body)
        hooks.set_json = set_json
        spec = importlib.util.spec_from_file_location("contract_pipeline_hooks", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)

    def send(self, body):
        transaction = {"request": {"body": json.dumps(body)}}
        self.module.a_pipeline_on_the_created_domain(transaction)
        return json.loads(transaction["request"]["body"])

    def test_created_is_put_on_the_created_domain(self):
        body = self.send({"domain_id": 1, "direction": "inbound", "filters": [{"name": "x", "config": {}}]})
        self.assertEqual(body["domain_id"], 17)
        self.assertEqual(body["direction"], "inbound")
        self.assertEqual(body["filters"], [{"name": "header_validate", "config": {}}])
        self.assertIs(body["active"], False)

    def test_direction_missing_stays_without_direction(self):
        body = self.send({"domain_id": 1, "filters": [{"name": "x", "config": {}}]})
        self.assertNotIn("direction", body)
        self.assertIn("filters", body)

    def test_filters_missing_stays_without_filters(self):
        body = self.send({"domain_id": 1, "direction": "inbound"})
        self.assertNotIn("filters", body)
        self.assertEqual(body["direction"], "inbound")

    def test_body_array_is_sent_as_documented(self):
        self.assertEqual(self.send([]), [])


if __name__ == "__main__":
    unittest.main()
