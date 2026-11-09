"""The rest of the filter operations (testFilter, listCustomFilters and
validateCustomFilter) start clean in the own-data run (#1385): each exchange
resets first, and a listing that succeeds gets a custom filter of its own."""

import json
import unittest
from unittest.mock import Mock, patch

from test_hooks_pipeline import load_hooks

OPERATIONS = ("testFilter", "listCustomFilters", "validateCustomFilter")


def transaction(path, name, body=None):
    return {
        "protocol": "http:", "host": "api", "port": "8080",
        "fullPath": path, "name": name,
        "request": {"body": json.dumps(body)},
    }


class CleanStart(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.env = patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.globals = self.hooks.filter_requests_after_a_reset.__globals__
        self.events = []

        def fixture_call(transaction, method, path, body=None):
            self.events.append((method, path))
            return {"id": 41}

        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        names["fixture_call"] = fixture_call
        names["start_clean"] = Mock(side_effect=lambda t: self.events.append("reset"))

    def test_a_listing_gets_a_custom_filter_of_its_own_after_its_reset(self):
        sent = transaction("/api/v1/admin/custom-filters", "x > 200 > application/json")
        self.hooks.filter_requests_after_a_reset(sent)
        self.assertEqual(self.events, ["reset", ("POST", "/api/v1/admin/custom-filters")])
        self.assertEqual(sent["fullPath"], "/api/v1/admin/custom-filters")

    def test_the_rest_only_reset(self):
        for path, name in (
            ("/api/v1/admin/custom-filters?domain_id=x", "x > 422 > application/json"),
            ("/api/v1/admin/custom-filters", "x > 401 > application/json"),
            ("/api/v1/admin/pipelines/test-filter", "x > 200 > application/json"),
            ("/api/v1/admin/pipelines/test-filter", "x > 422 > application/json"),
            ("/api/v1/admin/custom-filters/validate", "x > 200 > application/json"),
            ("/api/v1/admin/custom-filters/validate", "x > 422 > application/json"),
        ):
            with self.subTest(path=path, name=name):
                self.events.clear()
                body = {"filter_name": "header_validate", "config": {}}
                sent = transaction(path, name, body)
                self.hooks.filter_requests_after_a_reset(sent)
                self.assertEqual(self.events, ["reset"])
                self.assertEqual(sent["fullPath"], path)
                self.assertEqual(json.loads(sent["request"]["body"]), body)

    def test_the_reset_is_each_operations_first_hook(self):
        for operation in OPERATIONS:
            first = self.hooks.registered_before[operation][0]
            self.assertEqual(first.__name__, "filter_requests_after_a_reset", operation)


class WithoutAReset(unittest.TestCase):
    def test_nothing_changes(self):
        hooks = load_hooks()
        with patch.dict("os.environ", {"CONTRACT_RESET_SEED": ""}):
            for path in ("/api/v1/admin/custom-filters", "/api/v1/admin/pipelines/test-filter"):
                sent = transaction(path, "x > 200 > application/json", {"a": 1})
                hooks.filter_requests_after_a_reset(sent)
                self.assertEqual(sent["fullPath"], path)
                self.assertEqual(json.loads(sent["request"]["body"]), {"a": 1})


if __name__ == "__main__":
    unittest.main()
