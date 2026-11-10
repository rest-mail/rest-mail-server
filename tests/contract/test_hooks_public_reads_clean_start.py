"""The public reads and the mail client discovery reads start clean in the
own-data run (#1385): each exchange resets first, and nothing in the request
changes."""

import json
import unittest
from unittest.mock import Mock, patch

from test_hooks_pipeline import load_hooks

OPERATIONS = (
    "getHealth",
    "describeServer",
    "apiRoot",
    "swaggerUI",
    "openapiSpec",
    "openapiDocument",
    "prometheusMetrics",
    "mozillaAutoconfig",
    "mozillaAutoconfigWellKnown",
    "microsoftAutodiscover",
)
DISCOVERY = ("mozillaAutoconfig", "mozillaAutoconfigWellKnown")


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
        self.patch = patch.dict(self.hooks.public_reads_after_a_reset.__globals__)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        self.events = []
        names["start_clean"] = Mock(side_effect=lambda t: self.events.append("reset"))
        names["an_active_domain_for_autoconfig"] = Mock(
            side_effect=lambda t: self.events.append("domain")
        )

    def test_each_exchange_only_resets(self):
        for path, name in (
            ("/api/health", "x > 200 > application/json"),
            ("/metrics", "x > 404 > text/plain"),
            ("/mail/config-v1.1.xml?emailaddress=admin%40restmail.test", "x > 200 > application/xml"),
            ("/autodiscover/autodiscover.xml?emailaddress=", "x > 400 > application/json"),
        ):
            with self.subTest(path=path, name=name):
                self.events.clear()
                sent = transaction(path, name, {"a": 1})
                self.hooks.public_reads_after_a_reset(sent)
                self.assertEqual(self.events, ["reset"])
                self.assertEqual(sent["fullPath"], path)
                self.assertEqual(json.loads(sent["request"]["body"]), {"a": 1})

    def test_an_autodiscover_that_succeeds_gets_an_active_domain_after_its_reset(self):
        path = "/autodiscover/autodiscover.xml?emailaddress=user%40example.com"
        sent = transaction(path, "x > 200 > application/xml", {"a": 1})
        self.hooks.public_reads_after_a_reset(sent)
        self.assertEqual(self.events, ["reset", "domain"])
        self.assertEqual(sent["fullPath"], path)
        self.assertEqual(json.loads(sent["request"]["body"]), {"a": 1})

    def test_the_reset_is_each_operations_first_hook(self):
        for operation in OPERATIONS:
            first = self.hooks.registered_before[operation][0]
            self.assertEqual(first.__name__, "public_reads_after_a_reset", operation)

    def test_the_discovery_domain_is_prepared_after_the_reset(self):
        for operation in DISCOVERY:
            names = [h.__name__ for h in self.hooks.registered_before[operation]]
            self.assertLess(
                names.index("public_reads_after_a_reset"),
                names.index("an_active_domain_for_autoconfig"),
                operation,
            )


class WithoutAReset(unittest.TestCase):
    def test_nothing_changes(self):
        hooks = load_hooks()
        start_clean = Mock()
        with patch.dict("os.environ", {"CONTRACT_RESET_SEED": ""}), patch.dict(
            hooks.public_reads_after_a_reset.__globals__, {"start_clean": start_clean}
        ):
            sent = transaction("/api/health", "x > 200 > application/json", {"a": 1})
            hooks.public_reads_after_a_reset(sent)
        start_clean.assert_not_called()
        self.assertEqual(sent["fullPath"], "/api/health")


if __name__ == "__main__":
    unittest.main()
