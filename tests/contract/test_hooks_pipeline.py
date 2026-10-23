"""The createPipeline and testPipeline hooks point a request at the run's own
domain and pipeline, and leave each refused example as documented."""

import importlib.util
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {"domain_id": 17, "pipeline_id": 11}
    stub.before = stub.after = lambda name: (lambda fn: fn)
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    stub.set_json = lambda transaction, body: transaction["request"].__setitem__("body", json.dumps(body))
    spec = importlib.util.spec_from_file_location("contract_pipeline_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module


def sent(hook, body):
    transaction = {"request": {"body": json.dumps(body)}}
    hook(transaction)
    return json.loads(transaction["request"]["body"])


class CreatePipeline(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()

    def test_a_created_pipeline_is_inactive_on_the_run_domain(self):
        body = sent(self.hooks.a_pipeline_on_the_created_domain,
                    {"domain_id": 1, "direction": "inbound", "filters": [{"name": "header_validate", "config": {}}]})
        self.assertEqual(body["domain_id"], 17)
        self.assertIs(body["active"], False)

    def test_the_wrong_typed_example_keeps_its_value(self):
        body = sent(self.hooks.a_pipeline_on_the_created_domain,
                    {"domain_id": 1, "direction": "inbound", "filters": [], "active": "yes"})
        self.assertEqual(body["active"], "yes")
        self.assertEqual(body["domain_id"], 17)


class TestPipeline(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()

    def test_a_valid_request_tests_the_created_pipeline(self):
        body = sent(self.hooks.test_the_created_pipeline, {"pipeline_id": 1, "email": {}})
        self.assertEqual(body["pipeline_id"], 11)

    def test_refused_examples_are_sent_as_documented(self):
        for documented in ({"pipeline_id": 1}, {"pipeline_id": "1", "email": {}}):
            self.assertEqual(sent(self.hooks.test_the_created_pipeline, documented), documented)


if __name__ == "__main__":
    unittest.main()
