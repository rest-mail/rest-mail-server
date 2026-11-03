"""MTA-STS examples get repeat-safe state through the real admin API shape."""

import copy
import importlib.util
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import re
import sys
import threading
import types
import unittest
import urllib.error
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
POLICY = re.compile(r"^/api/v1/admin/domains/(\d+)/mta-sts$")


class FixtureAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def answer(self, status, value=None):
        payload = json.dumps({"data": value}).encode() if value is not None else b""
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.calls.append(("POST", self.path, dict(self.headers), body))
        if self.path != "/api/v1/admin/domains":
            return self.answer(404)
        self.server.serial += 1
        domain = dict(body, id=self.server.serial)
        self.server.domains[domain["id"]] = domain
        self.answer(201, domain)

    def do_PUT(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.calls.append(("PUT", self.path, dict(self.headers), body))
        match = POLICY.fullmatch(self.path)
        if self.server.failure:
            return self.answer(self.server.failure)
        if not match or int(match[1]) not in self.server.domains:
            return self.answer(404)
        domain_id = int(match[1])
        status = 200 if domain_id in self.server.policies else 201
        self.server.policies[domain_id] = dict(body, domain_id=domain_id)
        self.answer(status, self.server.policies[domain_id])

    def do_DELETE(self):
        self.server.calls.append(("DELETE", self.path, dict(self.headers), None))
        match = POLICY.fullmatch(self.path)
        if self.server.failure:
            return self.answer(self.server.failure)
        if not match or int(match[1]) not in self.server.policies:
            return self.answer(404)
        del self.server.policies[int(match[1])]
        self.answer(204)


class MtaStsHookTests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.calls = []
        self.server.domains = {1: {"name": "restmail.test"}, 7: {"name": "linked.test"}}
        self.server.policies = {}
        self.server.serial = 40
        self.server.failure = None
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.stub = types.ModuleType("vertrag_hooks")
        self.stub.stash = {}
        self.registered = []
        self.stub.before = lambda name: lambda fn: self.registered.append((name, fn.__name__)) or fn
        self.stub.after = lambda _: lambda fn: fn
        self.stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
        spec = importlib.util.spec_from_file_location("contract_mta_sts_hooks", ROOT / "tests/contract/hooks.py")
        self.hooks = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": self.stub}):
            spec.loader.exec_module(self.hooks)
        origin = "http://127.0.0.1:%d" % self.server.server_port
        self.stub.stash["setup_admin_tokens"] = {origin: "setup-session"}

    def transaction(self, status, domain="1", prefix="", body=None):
        return {
            "name": prefix + "/api/v1/admin/domains/{id}/mta-sts > Policy > %d > application/json" % status,
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_port),
            "fullPath": "/api/v1/admin/domains/%s/mta-sts?keep=yes" % domain,
            "request": {
                "headers": {"Authorization": "Bearer original-session"},
                "body": json.dumps(body),
            },
            "expected": {"statusCode": str(status)},
        }

    def test_registered_for_each_policy_operation(self):
        for operation in ("setMtaStsPolicy", "getMtaStsPolicy", "deleteMtaStsPolicy"):
            self.assertIn((operation, "prepare_mta_sts_policy_example"), self.registered)

    def test_create_placeholder_reuses_a_private_domain_without_a_policy(self):
        self.server.policies[1] = {"mode": "none"}
        for _ in range(2):
            transaction = self.transaction(201, "2", body={"active": False, "max_age": 0})
            before = copy.deepcopy(transaction)
            self.hooks.prepare_mta_sts_policy_example(transaction)
            domain_id = self.server.serial
            before["fullPath"] = "/api/v1/admin/domains/%d/mta-sts?keep=yes" % domain_id
            self.assertEqual(transaction, before)
            self.assertIn(domain_id, self.server.domains)
            self.assertNotIn(domain_id, self.server.policies)
            self.server.policies[domain_id] = {"mode": "testing"}
        self.assertEqual(self.server.policies[1], {"mode": "none"})
        self.assertEqual([call[0] for call in self.server.calls], ["POST", "DELETE", "DELETE"])

    def test_linked_create_clears_only_its_own_policy(self):
        self.server.policies = {1: {"mode": "none"}, 7: {"mode": "testing"}}
        transaction = self.transaction(201, "7")
        before = copy.deepcopy(transaction)
        self.hooks.prepare_mta_sts_policy_example(transaction)
        self.assertEqual(transaction, before)
        self.assertEqual(self.server.policies, {1: {"mode": "none"}})

    def test_each_create_probe_keeps_its_values_and_starts_without_a_policy(self):
        for domain in ("1", "7"):
            self.server.domains[int(domain)] = {"name": "probe.test"}
            self.server.policies[int(domain)] = {"mode": "testing"}
            transaction = self.transaction(201, domain, body={"active": False, "max_age": 0})
            before = copy.deepcopy(transaction)
            self.hooks.prepare_mta_sts_policy_example(transaction)
            self.assertEqual(transaction, before)
            self.assertNotIn(int(domain), self.server.policies)
        self.assertEqual([call[0] for call in self.server.calls], ["DELETE", "DELETE"])

    def test_read_update_and_delete_restore_a_consumed_policy_without_after_hooks(self):
        for status in (200, 200, 204):
            for prefix in ("", "coverage: "):
                with self.subTest(status=status, prefix=prefix):
                    self.server.policies.clear()
                    transaction = self.transaction(status, prefix=prefix, body={"max_age": 0, "active": False})
                    before = copy.deepcopy(transaction)
                    self.hooks.prepare_mta_sts_policy_example(transaction)
                    self.assertEqual(transaction, before)
                    policy = self.server.policies[1]
                    self.assertTrue(policy["active"])
                    self.assertEqual(policy["mx_hosts"], "mail.example.com")
        self.assertEqual([call[0] for call in self.server.calls], ["PUT"] * 6)
        self.assertTrue(all(call[2]["Authorization"] == "Bearer setup-session" for call in self.server.calls))

    def test_refusals_and_malformed_ids_have_no_setup(self):
        for status, domain in ((401, "1"), (403, "1"), (404, "1"), (422, "2"),
                               (201, "0"), (200, "x"), (204, "1.5")):
            transaction = self.transaction(status, domain)
            before = copy.deepcopy(transaction)
            self.hooks.prepare_mta_sts_policy_example(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_missing_domains_are_not_created_for_parameter_probes(self):
        for status in (200, 201, 204):
            transaction = self.transaction(status, "2147483647", "coverage: ")
            before = copy.deepcopy(transaction)
            self.hooks.prepare_mta_sts_policy_example(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(len(self.server.domains), 2)
        self.assertEqual(self.server.policies, {})

    def test_setup_failures_are_not_swallowed(self):
        self.server.failure = 500
        for status in (200, 201, 204):
            with self.subTest(status=status), self.assertRaises(urllib.error.HTTPError):
                self.hooks.prepare_mta_sts_policy_example(self.transaction(status))

    def policy_file(self, status, prefix=""):
        media = "text/plain" if status == 200 else "application/json"
        return {
            "name": prefix + "/.well-known/mta-sts.txt > MTA-STS policy file (RFC 8461) > %d > %s" % (status, media),
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_port),
            "fullPath": "/.well-known/mta-sts.txt",
            "request": {"headers": {"Host": "mta-sts.example.com"}, "body": ""},
            "expected": {"statusCode": str(status)},
        }

    def test_the_policy_file_hook_is_registered(self):
        self.assertIn(("mtaStsPolicyFile", "ask_for_the_created_domains_policy"), self.registered)

    def test_found_asks_for_a_domain_holding_exactly_the_found_policy(self):
        # The policy file's text is derived from these fields; the found pair's
        # response is "version: STSv1\nmode: enforce\nmx: mail.example.com\nmax_age: 604800\n".
        found = {"mode": "enforce", "mx_hosts": "mail.example.com", "max_age": 604800, "active": True}
        for prefix in ("", "coverage: "):
            with self.subTest(prefix=prefix):
                transaction = self.policy_file(200, prefix)
                self.hooks.ask_for_the_created_domains_policy(transaction)
                domain_id = self.server.serial
                name = self.server.domains[domain_id]["name"]
                self.assertEqual(transaction["request"]["headers"]["Host"], "mta-sts." + name)
                self.assertEqual(self.server.policies[domain_id], dict(found, domain_id=domain_id))
        # One domain of its own, its policy put again before each request.
        self.assertEqual([call[0] for call in self.server.calls], ["POST", "PUT", "PUT"])
        self.assertTrue(all(call[2]["Authorization"] == "Bearer setup-session" for call in self.server.calls))
        self.assertNotIn(1, self.server.policies)

    def test_found_restores_a_policy_something_deactivated(self):
        self.hooks.ask_for_the_created_domains_policy(self.policy_file(200))
        domain_id = self.server.serial
        self.server.policies[domain_id]["active"] = False
        self.hooks.ask_for_the_created_domains_policy(self.policy_file(200))
        self.assertTrue(self.server.policies[domain_id]["active"])

    def test_found_makes_its_domain_again_once_it_is_gone(self):
        first = self.policy_file(200)
        self.hooks.ask_for_the_created_domains_policy(first)
        del self.server.domains[self.server.serial]
        again = self.policy_file(200)
        self.hooks.ask_for_the_created_domains_policy(again)
        self.assertEqual([call[0] for call in self.server.calls], ["POST", "PUT", "PUT", "POST", "PUT"])
        self.assertNotEqual(again["request"]["headers"]["Host"], first["request"]["headers"]["Host"])
        self.assertTrue(self.server.policies[self.server.serial]["active"])

    def test_found_setup_failures_are_not_swallowed(self):
        self.server.failure = 500
        with self.assertRaises(urllib.error.HTTPError):
            self.hooks.ask_for_the_created_domains_policy(self.policy_file(200))

    def test_other_policy_file_requests_still_ask_for_the_created_domain(self):
        self.stub.stash["domain"] = "created.test"
        transaction = self.policy_file(404)
        self.hooks.ask_for_the_created_domains_policy(transaction)
        self.assertEqual(transaction["request"]["headers"]["Host"], "mta-sts.created.test")
        self.assertEqual(self.server.calls, [])

    def test_the_run_provides_the_active_policy_the_hook_sets_up(self):
        import yaml

        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        self.assertEqual(config["provides"], {"state": ["active_policy"]})
        examples = json.loads((ROOT / "openapi-example.json").read_text())
        needing = sorted(
            f"{operation}/{name}"
            for operation, pairs in examples.items()
            for name, pair in pairs.items()
            if pair.get("requires", {}).get("state") == "active_policy"
        )
        self.assertEqual(needing, ["mtaStsPolicyFile/found"])


if __name__ == "__main__":
    unittest.main()
