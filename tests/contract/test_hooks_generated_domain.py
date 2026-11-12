"""createDomain's probes name the generator's example.com, and a hook cannot change a
generated body. So the setup frees that name, whatever earlier probes put at it."""

import importlib.util
import io
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

ROOT = Path(__file__).resolve().parents[2]
CREATE = "/api/v1/admin/domains > Create a new domain > 201 > application/json"
ORIGIN = "http://fixture.test:8080"


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.before = lambda name: (lambda fn: fn)
    stub.after = lambda name: (lambda fn: fn)
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    spec = importlib.util.spec_from_file_location("contract_generated_domain_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, stub


class Answer(io.BytesIO):
    status = 200

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


class GeneratedDomain(unittest.TestCase):
    def setUp(self):
        self.hooks, self.stub = load_hooks()
        self.calls = []
        self.domains = [{"id": 18, "name": "example.com"}, {"id": 1, "name": "restmail.test"}]
        for name, value in (("admin_token", "setup"), ("mfa_admin_token", "two-factor")):
            patcher = patch.object(self.hooks, name, lambda origin, value=value: value)
            patcher.start()
            self.addCleanup(patcher.stop)
        patcher = patch("urllib.request.urlopen", self.answer)
        patcher.start()
        self.addCleanup(patcher.stop)

    def answer(self, request, timeout=None):
        path = request.full_url.removeprefix(ORIGIN)
        self.calls.append((request.get_method(), path, request.headers["Authorization"]))
        data = []
        parsed = urlsplit(path)
        if request.get_method() == "GET" and parsed.path == "/api/v1/admin/domains":
            query = parse_qs(parsed.query)
            offset = int(query.get("offset", [0])[0])
            limit = int(query.get("limit", [50])[0])
            data = self.domains[offset:offset + limit]
            return Answer(json.dumps({
                "data": data,
                "pagination": {"has_more": offset + len(data) < len(self.domains)},
            }).encode())
        elif request.get_method() == "GET" and parsed.path == "/api/v1/admin/aliases" and parse_qs(parsed.query).get("domain_id") == ["18"]:
            data = [{"id": 5}]
        elif request.get_method() == "GET" and parsed.path == "/api/v1/admin/mailboxes" and parse_qs(parsed.query).get("domain_id") == ["18"]:
            data = [{"id": 7, "address": "someone@" + self.domains[0]["name"]}, {"id": 9}]
            data.append({"id": 11, "address": "postmaster@" + self.domains[0]["name"]})
        return Answer(json.dumps({"data": data}).encode())

    def probe(self, name, status=CREATE):
        body = json.dumps({"name": name, "dmarc_policy": "reject"})
        return {
            "name": status,
            "protocol": "http:",
            "host": "fixture.test",
            "port": "8080",
            "request": {"body": body},
        }

    def test_a_probe_keeps_its_name_and_finds_it_free(self):
        self.stub.stash.update(
            probe_mailboxes={ORIGIN + "\nsomeone@example.com": True, ORIGIN + "\nadmin@restmail.test": True},
            created_account_probes={ORIGIN + "\nsomeone@example.com": True},
            link_fixture_prepared={ORIGIN: True},
        )
        transaction = self.probe("example.com")
        drawn = transaction["request"]["body"]
        self.hooks.create_a_domain_that_is_not_there(transaction)
        self.assertEqual(transaction["request"]["body"], drawn)
        made = [call for call in self.calls if call[0] == "DELETE"]
        self.assertEqual(
            made,
            [
                ("DELETE", "/api/v1/admin/aliases/5", "Bearer two-factor"),
                ("DELETE", "/api/v1/admin/mailboxes/7", "Bearer two-factor"),
                ("DELETE", "/api/v1/admin/mailboxes/9", "Bearer two-factor"),
                ("DELETE", "/api/v1/admin/domains/18", "Bearer setup"),
            ],
        )
        self.assertEqual(self.stub.stash["probe_mailboxes"], {ORIGIN + "\nadmin@restmail.test": True})
        self.assertEqual(self.stub.stash["created_account_probes"], {})
        self.assertNotIn(ORIGIN, self.stub.stash["link_fixture_prepared"])

    def test_the_domain_postmaster_is_retired_with_the_domain(self):
        # Deleting a domain's last postmaster on its own is refused with 409
        # postmaster_required; the domain DELETE retires it (#1705).
        self.hooks.create_a_domain_that_is_not_there(self.probe("example.com"))
        deleted = [call[1] for call in self.calls if call[0] == "DELETE"]
        self.assertNotIn("/api/v1/admin/mailboxes/11", deleted)
        self.assertEqual(deleted[-1], "/api/v1/admin/domains/18")

    def test_what_follows_the_probes_hosts_the_domain_again(self):
        self.hooks.create_a_domain_that_is_not_there(self.probe("example.com"))
        self.calls.clear()
        following = self.probe("example.com", "anything")
        following["operationId"] = "createDomain"
        self.hooks.host_the_generated_domain_again(following)
        self.assertEqual(self.calls, [])
        following["operationId"] = "microsoftAutodiscover"
        with patch.object(self.hooks.subprocess, "run") as fixture:
            self.hooks.host_the_generated_domain_again(following)
            self.assertEqual(
                [call[:2] for call in self.calls],
                [
                    ("GET", "/api/v1/admin/domains"),
                    ("DELETE", "/api/v1/admin/domains/18"),
                ],
            )
            self.assertEqual(fixture.call_args.args[0], ["chore", "test:contract-link-fixture"])
            self.calls.clear()
            self.hooks.host_the_generated_domain_again(following)
            self.assertEqual(self.calls, [])
            self.assertEqual(fixture.call_count, 1)

    def test_a_documented_create_only_removes_its_own_leftover(self):
        self.hooks.create_a_domain_that_is_not_there(self.probe("restmail.test"))
        self.assertEqual(
            [call[:2] for call in self.calls if call[0] == "DELETE"],
            [("DELETE", "/api/v1/admin/domains/1")],
        )

    def test_a_refused_transaction_changes_nothing(self):
        self.hooks.create_a_domain_that_is_not_there(self.probe("example.com", CREATE.replace("201", "409")))
        self.assertEqual(self.calls, [])

    def test_idn_probes_free_the_same_canonical_domain_on_a_later_page(self):
        canonical = "xn--fsqu00a.xn--4rr70v"
        self.domains = [{"id": index + 100, "name": f"filler-{index}.example"} for index in range(220)]
        self.domains.append({"id": 999, "name": canonical})
        for name in ("例子.广告", canonical):
            with self.subTest(name=name):
                self.calls.clear()
                transaction = self.probe(name)
                before = json.loads(json.dumps(transaction))
                self.hooks.create_a_domain_that_is_not_there(transaction)
                self.assertEqual(transaction, before)
                self.assertIn(
                    ("GET", "/api/v1/admin/domains?limit=200&offset=200", "Bearer setup"),
                    self.calls,
                )
                self.assertEqual(
                    [call[:2] for call in self.calls if call[0] == "DELETE"],
                    [("DELETE", "/api/v1/admin/domains/999")],
                )

    def test_idn_domain_addresses_are_removed_before_recreating_it(self):
        canonical = "xn--fsqu00a.xn--4rr70v"
        self.domains = [{"id": 18, "name": canonical}]
        self.stub.stash["sender_mailboxes"] = {
            ORIGIN + "\n用户@" + canonical: True,
            ORIGIN + "\nother@restmail.test": True,
        }
        transaction = self.probe("例子.广告")
        before = json.loads(json.dumps(transaction))
        self.hooks.create_a_domain_that_is_not_there(transaction)
        self.assertEqual(transaction, before)
        self.assertEqual(
            [call for call in self.calls if call[0] == "DELETE"],
            [
                ("DELETE", "/api/v1/admin/aliases/5", "Bearer two-factor"),
                ("DELETE", "/api/v1/admin/mailboxes/7", "Bearer two-factor"),
                ("DELETE", "/api/v1/admin/mailboxes/9", "Bearer two-factor"),
                ("DELETE", "/api/v1/admin/domains/18", "Bearer setup"),
            ],
        )
        self.assertEqual(self.stub.stash["sender_mailboxes"], {ORIGIN + "\nother@restmail.test": True})
        self.assertFalse(self.stub.stash.get("generated_domain_freed"))


if __name__ == "__main__":
    unittest.main()
