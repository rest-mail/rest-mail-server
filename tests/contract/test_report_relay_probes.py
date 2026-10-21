"""Keep valid report probes valid, and state the relay rule so vertrag reads it."""

import copy
import json
from pathlib import Path
import unittest
from unittest.mock import patch

import yaml

from test_hooks_delete_domain import load_hooks

ROOT = Path(__file__).resolve().parents[2]


class ReportRelayProbes(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, registered = load_hooks()
        self.assertIn("receiveTlsReport", registered)
        self.assertNotIn("setDomainRelay", registered)
        self.hooks.get_json = lambda tx: json.loads(tx["request"]["body"])
        self.hooks.set_json = lambda tx, body: tx["request"].__setitem__("body", json.dumps(body))

    def transaction(self, body, name=""):
        return {"name": name, "protocol": "http:", "host": "fixture", "port": 8080,
                "request": {"body": json.dumps(body)}}

    def test_hosts_each_report_domain_once_without_rewriting_the_report(self):
        tx = self.transaction({"policies": [
            {"policy": {"policy-domain": "example.com"}},
            {"policy": {"policy-domain": "example.com"}},
            {"policy": {"policy-domain": "other.example."}},
        ]})
        original = copy.deepcopy(tx)
        with patch.object(self.module, "admin_token", return_value="setup"), \
                patch.object(self.module, "hosted_domain_id") as host:
            self.module.host_the_reported_policy_domains(tx)
        self.assertEqual(tx, original)
        self.assertEqual([call.args[2] for call in host.call_args_list], ["example.com", "other.example"])

    def test_malformed_reports_and_missing_domains_are_left_for_the_handler(self):
        for body in ([], {}, {"policies": None}, {"policies": "bad"},
                     {"policies": [None, {}, {"policy": []}, {"policy": {}},
                                   {"policy": {"policy-domain": 123}},
                                   {"policy": {"policy-domain": ""}}]}):
            with self.subTest(body=body):
                tx = self.transaction(body)
                original = copy.deepcopy(tx)
                with patch.object(self.module, "admin_token") as login:
                    self.module.host_the_reported_policy_domains(tx)
                login.assert_not_called()
                self.assertEqual(tx, original)

    def test_relay_rule_is_anyof_so_a_bare_relay_is_invalid(self):
        # vertrag reads schemas as draft-04 and ignores if/then, so the rule
        # that a relay needs host, port and tls_mode is stated as anyOf.
        with open(ROOT / "openapi.yaml") as f:
            relay = yaml.safe_load(f)["components"]["schemas"]["SetDomainRelayRequest"]
        self.assertNotIn("if", relay)
        self.assertNotIn("then", relay)

        def permits(body):
            for branch in relay["anyOf"]:
                if all(field in body for field in branch.get("required", [])) and all(
                        body.get(name) in rule["enum"]
                        for name, rule in branch.get("properties", {}).items()):
                    return True
            return False

        self.assertTrue(permits({"transport": "direct"}))
        self.assertTrue(permits({"transport": "relay", "host": "smtp.example.net",
                                 "port": 587, "tls_mode": "starttls"}))
        self.assertFalse(permits({"transport": "relay"}))
        self.assertFalse(permits({"transport": "relay", "host": "smtp.example.net", "port": 587}))

    def test_report_schema_requires_a_policy_domain_and_bounds_storage(self):
        with open(ROOT / "openapi.yaml") as f:
            report = yaml.safe_load(f)["components"]["schemas"]["TLSRPTReport"]
        policies = report["properties"]["policies"]
        self.assertEqual((policies["minItems"], policies["maxItems"]), (1, 100))
        self.assertIn("policy", policies["items"]["required"])
        policy = policies["items"]["properties"]["policy"]
        self.assertIn("policy-domain", policy["required"])
        self.assertEqual(policy["properties"]["policy-domain"]["format"], "hostname")
        self.assertEqual(policy["properties"]["policy-domain"]["minLength"], 1)


if __name__ == "__main__":
    unittest.main()
