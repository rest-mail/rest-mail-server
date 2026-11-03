"""The pipeline hooks point a request at the run's own domain and a pipeline of
its own, leave each refused example as documented, and after a reset (#1385)
start clean and make what the exchange needs."""

import copy
import json
import runpy
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

import yaml

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    """Exercise the hook entrypoint selected by the real contract configuration."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {"domain_id": 17}
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

    def test_a_valid_request_tests_a_pipeline_of_its_own(self):
        transaction = {
            "protocol": "http:", "host": "api", "port": "8080",
            "request": {"body": json.dumps({"pipeline_id": 1, "email": {}})},
        }
        with patch.dict(self.hooks.test_the_created_pipeline.__globals__) as names:
            call = names["fixture_call"] = Mock(return_value={"id": 23})
            self.hooks.test_the_created_pipeline(transaction)
        call.assert_called_once_with(transaction, "POST", "/api/v1/admin/pipelines", {
            "domain_id": 17,
            "direction": "inbound",
            "filters": [{"name": "header_validate", "config": {}}],
            "active": False,
        })
        self.assertEqual(json.loads(transaction["request"]["body"])["pipeline_id"], 23)
        self.assertNotIn("pipeline_id", self.hooks.hooks.stash)

    def test_before_the_run_has_a_domain_it_is_sent_as_before(self):
        self.hooks.hooks.stash.pop("domain_id")
        body = {"pipeline_id": 1, "email": {}}
        self.assertEqual(sent(self.hooks.test_the_created_pipeline, body), body)

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
        self.assertEqual(registered.count(self.hooks.test_the_created_pipeline), 1)
        with patch.object(self.hooks.time, "sleep") as sleep:
            for callback in registered:
                callback(transaction)
        sleep.assert_called_once_with(0.55)
        self.assertEqual(transaction, before)

    def test_nonplaceholder_and_invalid_targets_are_preserved(self):
        self.hooks.hooks.stash["domain_id"] = 17
        for pipeline_id in (0, 42, 2147483647, True, "1", None):
            with self.subTest(pipeline_id=pipeline_id):
                body = {"pipeline_id": pipeline_id, "email": {}}
                self.assertEqual(sent(self.hooks.test_the_created_pipeline, body), body)


class CleanStart(unittest.TestCase):
    """With CONTRACT_RESET_SEED=1 (the own-data phase), each moved exchange
    resets first and makes its own domain and pipeline."""

    def setUp(self):
        self.hooks = load_hooks()
        self.env = patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.globals = self.hooks.test_the_created_pipeline.__globals__
        self.made = []

        def fixture_call(transaction, method, path, body=None):
            self.made.append((method, path, body))
            return {"id": 30 + len(self.made), "name": "made.test"}

        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        names["fixture_call"] = fixture_call
        names["start_clean"] = self.start_clean = Mock(
            side_effect=lambda transaction: self.forget()
        )

    def forget(self):
        self.hooks.hooks.stash.pop("domain_id", None)

    def transaction(self, path, name, body=None):
        return {
            "protocol": "http:", "host": "api", "port": "8080",
            "fullPath": path, "name": name,
            "request": {"body": json.dumps(body)},
        }

    def test_create_resets_and_puts_the_pipeline_on_a_domain_of_its_own(self):
        transaction = self.transaction(
            "/api/v1/admin/pipelines",
            "/api/v1/admin/pipelines > Create a new pipeline > 201",
            {"domain_id": 1, "direction": "inbound", "filters": []},
        )
        self.hooks.a_pipeline_on_the_created_domain(transaction)
        self.start_clean.assert_called_once()
        self.assertEqual(self.made[0][:2], ("POST", "/api/v1/admin/domains"))
        self.assertEqual(json.loads(transaction["request"]["body"])["domain_id"], 31)

    def test_an_undecodable_create_resets_and_is_sent_as_documented(self):
        transaction = self.transaction("/api/v1/admin/pipelines", "x > 400", [])
        self.hooks.a_pipeline_on_the_created_domain(transaction)
        self.start_clean.assert_called_once()
        self.assertEqual(self.made, [])
        self.assertEqual(json.loads(transaction["request"]["body"]), [])

    def test_a_preview_resets_and_tests_a_pipeline_on_a_domain_of_its_own(self):
        transaction = self.transaction(
            "/api/v1/admin/pipelines/test", "x > 200", {"pipeline_id": 1, "email": {}}
        )
        self.hooks.test_the_created_pipeline(transaction)
        self.start_clean.assert_called_once()
        self.assertEqual([m[1] for m in self.made], [
            "/api/v1/admin/domains", "/api/v1/admin/pipelines",
        ])
        self.assertEqual(self.made[1][2]["domain_id"], 31)
        self.assertEqual(json.loads(transaction["request"]["body"])["pipeline_id"], 32)

    def test_a_refused_preview_resets_and_is_sent_as_documented(self):
        body = {"pipeline_id": 2147483647, "email": {}}
        transaction = self.transaction("/api/v1/admin/pipelines/test", "x > 404", body)
        self.hooks.test_the_created_pipeline(transaction)
        self.start_clean.assert_called_once()
        self.assertEqual(self.made, [])
        self.assertEqual(json.loads(transaction["request"]["body"]), body)

    def test_delete_resets_and_deletes_a_pipeline_of_its_own(self):
        transaction = self.transaction(
            "/api/v1/admin/pipelines/5", "x > Delete pipeline > 200 > application/json"
        )
        self.hooks.delete_a_pipeline_of_its_own(transaction)
        self.start_clean.assert_called_once()
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/pipelines/32")
        missing = self.transaction("/api/v1/admin/pipelines/2147483647", "x > 404")
        self.hooks.delete_a_pipeline_of_its_own(missing)
        self.assertEqual(missing["fullPath"], "/api/v1/admin/pipelines/2147483647")

    def test_an_update_resets_and_updates_a_pipeline_of_its_own(self):
        for status in ("200", "422"):
            with self.subTest(status=status):
                self.made.clear()
                self.start_clean.reset_mock()
                transaction = self.transaction("/api/v1/admin/pipelines/5", "x > " + status)
                self.hooks.update_a_pipeline_of_its_own(transaction)
                self.start_clean.assert_called_once()
                self.assertEqual([m[1] for m in self.made], [
                    "/api/v1/admin/domains", "/api/v1/admin/pipelines",
                ])
                self.assertEqual(self.made[1][2]["domain_id"], 31)
                self.assertFalse(self.made[1][2]["active"])
                self.assertEqual(transaction["fullPath"], "/api/v1/admin/pipelines/32")

    def test_a_refused_update_resets_and_keeps_its_id(self):
        for refused in ("2147483647", "0", "abc"):
            with self.subTest(id=refused):
                self.start_clean.reset_mock()
                other = self.transaction("/api/v1/admin/pipelines/" + refused, "x > 404")
                self.hooks.update_a_pipeline_of_its_own(other)
                self.start_clean.assert_called_once()
                self.assertEqual(other["fullPath"], "/api/v1/admin/pipelines/" + refused)
        self.assertEqual(self.made, [])

    def test_a_pipeline_listing_gets_a_pipeline_of_its_own_after_its_reset(self):
        events = []
        self.start_clean.side_effect = lambda transaction: events.append("reset")
        made = self.globals["fixture_call"]

        def recorded(transaction, method, path, body=None):
            events.append(path)
            return made(transaction, method, path, body)

        self.globals["fixture_call"] = recorded
        transaction = self.transaction("/api/v1/admin/pipelines", "x > 200")
        self.hooks.pipeline_reads_after_a_reset(transaction)
        self.assertEqual(events, [
            "reset", "/api/v1/admin/domains", "/api/v1/admin/pipelines",
        ])
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/pipelines")

    def test_the_other_pipeline_reads_only_reset(self):
        for path, name in (
            ("/api/v1/admin/pipelines?domain_id=x", "x > 422"),
            ("/api/v1/admin/pipelines/logs", "x > 200"),
            ("/api/v1/admin/pipelines/logs?limit=-1", "x > 422"),
            ("/api/v1/admin/pipelines/analytics", "x > 200"),
        ):
            with self.subTest(path=path, name=name):
                self.start_clean.reset_mock()
                transaction = self.transaction(path, name)
                self.hooks.pipeline_reads_after_a_reset(transaction)
                self.start_clean.assert_called_once()
                self.assertEqual(transaction["fullPath"], path)
        self.assertEqual(self.made, [])


class WithoutAReset(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()

    def test_delete_update_and_reads_change_nothing(self):
        for hook in (
            self.hooks.delete_a_pipeline_of_its_own,
            self.hooks.update_a_pipeline_of_its_own,
            self.hooks.pipeline_reads_after_a_reset,
        ):
            transaction = {
                "fullPath": "/api/v1/admin/pipelines/5",
                "name": "x > 200",
            }
            hook(transaction)
            self.assertEqual(transaction["fullPath"], "/api/v1/admin/pipelines/5")


if __name__ == "__main__":
    unittest.main()
