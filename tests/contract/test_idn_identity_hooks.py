"""IDN probes retain their bytes while setup finds the server's stored keys."""

import copy
import io
import json
import unittest
from unittest.mock import patch

from test_hooks_generated_domain import CREATE, ORIGIN, load_hooks


class IDNIdentityHooks(unittest.TestCase):
    def setUp(self):
        self.hooks, self.stub = load_hooks()
        self.patcher = patch.object(self.hooks, "admin_token", return_value="setup")
        self.patcher.start()
        self.addCleanup(self.patcher.stop)
        two_factor = patch.object(self.hooks, "mfa_admin_token", return_value="two-factor")
        two_factor.start()
        self.addCleanup(two_factor.stop)

    def answer(self, data, has_more=False):
        body = {"data": data}
        if has_more:
            body["pagination"] = {"has_more": True}
        response = io.BytesIO(json.dumps(body).encode())
        response.status = 204 if data is None else 200
        return response

    def transaction(self, operation, body):
        return {"name": CREATE if operation == "createDomain" else CREATE.replace("Create a new domain", "Create an alias"),
                "protocol": "http:", "host": "fixture.test", "port": "8080",
                "request": {"body": json.dumps(body)}}

    def test_unicode_domain_removes_its_normalized_leftover_only(self):
        transaction = self.transaction("createDomain", {"name": "例子.广告"})
        original = copy.deepcopy(transaction)
        replies = [self.answer([{"id": 18, "name": "xn--fsqu00a.xn--4rr70v"},
                                {"id": 1, "name": "restmail.test"}]),
                   self.answer([]), self.answer([]), self.answer(None)]
        with patch("urllib.request.urlopen", side_effect=replies) as call:
            self.hooks.create_a_domain_that_is_not_there(transaction)
        self.assertEqual(transaction, original)
        self.assertEqual(call.call_args_list[-1].args[0].full_url, ORIGIN + "/api/v1/admin/domains/18")
        self.assertEqual(call.call_args_list[-1].args[0].get_method(), "DELETE")
        self.assertEqual(call.call_count, 4)
        self.assertEqual(
            [request.args[0].full_url for request in call.call_args_list[1:3]],
            [ORIGIN + "/api/v1/admin/aliases?domain_id=18&limit=200&offset=0",
             ORIGIN + "/api/v1/admin/mailboxes?domain_id=18&limit=200&offset=0"],
        )

    def test_repeated_unicode_alias_removes_the_normalized_owned_row(self):
        source = "用户@例子.广告"
        transaction = self.transaction("createAlias", {"source_address": source, "destination_address": "a@restmail.test"})
        original = copy.deepcopy(transaction)
        domains = [{"id": 18, "name": "xn--fsqu00a.xn--4rr70v"}]
        alias = {"id": 5, "source_address": "用户@xn--fsqu00a.xn--4rr70v"}
        replies = [self.answer(domains), self.answer([]), self.answer(domains),
                   self.answer([alias]), self.answer(None)]
        with patch("urllib.request.urlopen", side_effect=replies) as call:
            self.hooks.remove_a_previous_alias_probe(transaction)
            self.hooks.remove_a_previous_alias_probe(transaction)
        self.assertEqual(transaction, original)
        self.assertEqual(call.call_args_list[-1].args[0].full_url, ORIGIN + "/api/v1/admin/aliases/5")
        self.assertEqual(call.call_args_list[-1].args[0].get_method(), "DELETE")
        self.assertEqual(call.call_count, 5)

    def test_uts46_keys_preserve_idna2008_and_nfc(self):
        self.assertEqual(self.hooks.domain_key(" ＢÜCHER.example. "), "xn--bcher-kva.example")
        self.assertEqual(self.hooks.domain_key("faß.example"), "xn--fa-hia.example")
        self.assertNotEqual(self.hooks.domain_key("faß.example"), "fass.example")
        self.assertEqual(self.hooks.address_key("E\u0301@例子.广告"), "é@xn--fsqu00a.xn--4rr70v")

    def test_an_existing_unowned_alias_is_not_deleted(self):
        transaction = self.transaction("createAlias", {"source_address": "用户@例子.广告"})
        replies = [self.answer([{"id": 18, "name": "xn--fsqu00a.xn--4rr70v"}]),
                   self.answer([{"id": 5, "source_address": "用户@xn--fsqu00a.xn--4rr70v"}])]
        with patch("urllib.request.urlopen", side_effect=replies) as call:
            self.hooks.remove_a_previous_alias_probe(transaction)
        self.assertEqual(call.call_count, 2)

    def test_normalized_domain_and_alias_lookups_follow_later_pages(self):
        domain = {"id": 18, "name": "xn--fsqu00a.xn--4rr70v"}
        alias = {"id": 5, "source_address": "用户@xn--fsqu00a.xn--4rr70v"}
        replies = [self.answer([{"id": 1, "name": "restmail.test"}], has_more=True),
                   self.answer([domain]),
                   self.answer([{"id": 1, "source_address": "a@restmail.test"}], has_more=True),
                   self.answer([alias])]
        with patch("urllib.request.urlopen", side_effect=replies) as call:
            self.assertEqual(self.hooks.hosted_domain_id(ORIGIN, {}, "例子.广告"), 18)
            self.assertEqual(self.hooks.alias_on(ORIGIN, {}, 18, "用户@例子.广告"), alias)
        self.assertEqual(call.call_count, 4)
        for request in call.call_args_list:
            self.assertEqual(request.args[0].get_method(), "GET")
        self.assertIn("offset=1", call.call_args_list[1].args[0].full_url)
        self.assertIn("offset=1", call.call_args_list[3].args[0].full_url)

    def test_normalized_sender_on_later_pages_is_cached_without_creation(self):
        sender = "用户@例子.广告"
        transaction = {"protocol": "http:", "host": "fixture.test", "port": "8080",
                       "request": {"body": json.dumps({"from": sender, "to": [sender]})}}
        original = copy.deepcopy(transaction)
        replies = [self.answer([{"id": 1, "name": "restmail.test"}], has_more=True),
                   self.answer([{"id": 18, "name": "xn--fsqu00a.xn--4rr70v"}]),
                   self.answer([{"domain_id": 18}]),
                   self.answer([{"address": "a@restmail.test"}], has_more=True),
                   self.answer([{"address": "用户@xn--fsqu00a.xn--4rr70v"}])]
        with patch("urllib.request.urlopen", side_effect=replies) as call:
            self.hooks.a_signing_mailbox_at(transaction, sender)
            self.hooks.a_signing_mailbox_at(transaction, sender)
        self.assertEqual(transaction, original)
        self.assertEqual(call.call_count, 5)
        for request in call.call_args_list:
            self.assertEqual(request.args[0].get_method(), "GET")
        self.assertIn("offset=1", call.call_args_list[1].args[0].full_url)
        self.assertIn("offset=1", call.call_args_list[4].args[0].full_url)


if __name__ == "__main__":
    unittest.main()
