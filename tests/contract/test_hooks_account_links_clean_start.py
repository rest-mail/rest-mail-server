"""The account link and client secret operations start clean in the own-data
run (#1385): each exchange resets first, nothing is made, and the link's 201
is not preceded by an unlink of a link the reset already removed."""

import json
import os
import unittest
from unittest.mock import Mock, patch

from test_hooks_pipeline import load_hooks

OPERATIONS = {
    "linkAccount": ("/api/v1/accounts/link", "POST"),
    "unlinkAccount": ("/api/v1/accounts/2147483647", "DELETE"),
    "listClientSecrets": ("/api/v1/accounts/0/client-secrets", "GET"),
    "createClientSecret": ("/api/v1/accounts/1/client-secrets", "POST"),
    "revokeClientSecret": ("/api/v1/accounts/1/client-secrets/0", "DELETE"),
    "issueClientCredentialsToken": ("/api/v1/oauth/token", "POST"),
}


def transaction(path, name, body=None):
    return {
        "protocol": "http:", "host": "api", "port": "8080",
        "fullPath": path, "name": name,
        "request": {"body": json.dumps(body), "headers": {"Authorization": "Bearer session"}},
    }


class CleanStart(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.hook = self.hooks.account_links_and_client_secrets_after_a_reset
        self.globals = self.hook.__globals__
        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        self.start_clean = Mock()
        names["start_clean"] = self.start_clean
        self.urlopen = Mock(side_effect=AssertionError("no request may be made"))
        names["urllib"] = Mock(request=Mock(urlopen=self.urlopen, Request=Mock()), error=Mock())
        names["find_in_list"] = Mock(side_effect=AssertionError("no lookup may be made"))
        names["subprocess"] = Mock(run=Mock(side_effect=AssertionError("no fixture may run")))

    def clean(self, on):
        env = patch.dict("os.environ")
        env.start()
        self.addCleanup(env.stop)
        os.environ.pop("CONTRACT_RESET_SEED", None)
        if on:
            os.environ["CONTRACT_RESET_SEED"] = "1"

    def test_each_exchange_resets_first_and_keeps_its_request(self):
        self.clean(True)
        for operation, (path, method) in OPERATIONS.items():
            for status in ("201", "204", "400", "404", "422"):
                with self.subTest(operation=operation, status=status):
                    self.start_clean.reset_mock()
                    sent = transaction(path, f"{method} {path} > x > {status} > application/json", {"a": 1})
                    before = json.dumps(sent, sort_keys=True)
                    self.hook(sent)
                    self.start_clean.assert_called_once_with(sent)
                    self.assertEqual(json.dumps(sent, sort_keys=True), before)

    def test_it_is_each_operations_first_hook(self):
        for operation in OPERATIONS:
            with self.subTest(operation=operation):
                self.assertIs(self.hooks.registered_before[operation][0].__name__, self.hook.__name__)

    def test_nothing_happens_without_a_reset(self):
        self.clean(False)
        for operation, (path, method) in OPERATIONS.items():
            self.hook(transaction(path, f"{method} {path} > x > 201 > application/json"))
        self.start_clean.assert_not_called()

    def test_the_link_after_a_reset_undoes_no_earlier_link(self):
        # The reset put eve back unlinked: no lookup, no unlink, no fixture.
        self.clean(True)
        sent = transaction(
            "/api/v1/accounts/link",
            "POST /api/v1/accounts/link > Link > 201 > application/json",
            {"address": "eve@restmail.test", "password": "supplied-by-contract-hook"},
        )
        self.hooks.link_a_seeded_account(sent)
        self.assertEqual(
            json.loads(sent["request"]["body"]),
            {"address": "eve@restmail.test", "password": self.globals["LINKABLE_PASSWORD"]},
        )
        self.urlopen.assert_not_called()

    def test_the_link_without_a_reset_still_undoes_the_earlier_link(self):
        self.clean(False)
        self.globals["find_in_list"] = Mock(return_value={"id": 5})
        self.urlopen.side_effect = None
        self.urlopen.return_value.__enter__ = Mock(return_value=Mock(status=204))
        self.urlopen.return_value.__exit__ = Mock(return_value=False)
        sent = transaction(
            "/api/v1/accounts/link",
            "POST /api/v1/accounts/link > Link > 201 > application/json",
            {"address": "eve@restmail.test", "password": "supplied-by-contract-hook"},
        )
        with patch.dict(self.globals, {"admin_token": lambda origin: "admin"}):
            self.hooks.link_a_seeded_account(sent)
        self.urlopen.assert_called_once()


if __name__ == "__main__":
    unittest.main()
