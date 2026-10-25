"""The createPipeline and testPipeline hooks point a request at the run's own
domain and pipeline, and leave each refused example as documented."""

import copy
import json
import runpy
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    """Exercise the hook entrypoint selected by the real contract configuration."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {"domain_id": 17, "pipeline_id": 11}
    callbacks = {}

    def before(name):
        def register(fn):
            callbacks.setdefault(name, []).append(fn)
            return fn

        return register

    stub.before = before
    stub.after = lambda name: lambda fn: fn
    stub.get_json = lambda transaction, which="request": json.loads(
        transaction[which]["body"]
    )
    stub.set_json = lambda transaction, body: transaction["request"].__setitem__(
        "body", json.dumps(body)
    )
    config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        loaded = runpy.run_path(str(ROOT / config["hookfiles"]))
    module = types.SimpleNamespace(**loaded["shared"])
    module.registered_before = callbacks
    return module


def sent(hook, body):
    transaction = {"request": {"body": json.dumps(body)}}
    hook(transaction)
    return json.loads(transaction["request"]["body"])


class CreatePipeline(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()

    def test_a_created_pipeline_is_inactive_on_the_run_domain(self):
        body = sent(
            self.hooks.a_pipeline_on_the_created_domain,
            {
                "domain_id": 1,
                "direction": "inbound",
                "filters": [{"name": "header_validate", "config": {}}],
            },
        )
        self.assertEqual(body["domain_id"], 17)
        self.assertIs(body["active"], False)

    def test_the_wrong_typed_example_keeps_its_value(self):
        body = sent(
            self.hooks.a_pipeline_on_the_created_domain,
            {"domain_id": 1, "direction": "inbound", "filters": [], "active": "yes"},
        )
        self.assertEqual(body["active"], "yes")
        self.assertEqual(body["domain_id"], 17)


class TestPipeline(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()

    def test_a_valid_request_tests_the_created_pipeline(self):
        body = sent(
            self.hooks.test_the_created_pipeline, {"pipeline_id": 1, "email": {}}
        )
        self.assertEqual(body["pipeline_id"], 11)

    def test_refused_examples_are_sent_as_documented(self):
        for documented in ({"pipeline_id": 1}, {"pipeline_id": "1", "email": {}}):
            self.assertEqual(
                sent(self.hooks.test_the_created_pipeline, documented), documented
            )

    def test_missing_example_keeps_its_request_expectation_and_credential(self):
        spec = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        op = spec["paths"]["/api/v1/admin/pipelines/test"]["post"]
        body = op["requestBody"]["content"]["application/json"]["examples"]["missing"][
            "value"
        ]
        refusal = op["responses"]["404"]["content"]["application/json"]["examples"][
            "missing"
        ]["value"]
        transaction = {
            "request": {
                "method": "POST",
                "uri": "http://restmail.test/api/v1/admin/pipelines/test",
                "headers": {"Authorization": "Bearer fixture-session"},
                "body": json.dumps(body),
            },
            "expected": {"statusCode": 404, "body": json.dumps(refusal)},
        }
        before = copy.deepcopy(transaction)
        registered = self.hooks.registered_before["testPipeline"]
        self.assertEqual(len(registered), 1)
        for callback in registered:
            callback(transaction)
        self.assertEqual(transaction, before)

    def test_nonplaceholder_and_invalid_targets_are_preserved(self):
        for pipeline_id in (0, 42, 2147483647, True, "1", None):
            with self.subTest(pipeline_id=pipeline_id):
                body = {"pipeline_id": pipeline_id, "email": {}}
                self.assertEqual(sent(self.hooks.test_the_created_pipeline, body), body)


if __name__ == "__main__":
    unittest.main()
