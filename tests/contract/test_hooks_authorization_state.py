"""What the authorization phase sends meets state of its own, not what the runs
before it left behind (#376): a draft for the admin's cell, and an address of its
own for each documented recovery request."""

import base64
import json
import os
import unittest
from unittest.mock import patch

from test_hooks_example_state import load_hooks

DRAFT = "/api/v1/messages/draft/{id} > Update an existing draft > %s > application/json"
RECOVERY = "/api/v1/auth/recover/email > Request a password recovery link > %s > application/json"


def session(address):
    payload = base64.urlsafe_b64encode(json.dumps({"address": address}).encode()).decode()
    return "Bearer header." + payload.rstrip("=") + ".signature"


class UpdateDraftCell(unittest.TestCase):
    def setUp(self):
        self.hooks, _ = load_hooks()
        environment = dict(os.environ)
        environment.pop("CONTRACT_COVERAGE", None)
        environment.pop("CONTRACT_OWNERSHIP", None)
        patcher = patch.dict(os.environ, environment, clear=True)
        patcher.start()
        self.addCleanup(patcher.stop)

    def cell(self, address=None, path="/api/v1/messages/draft/12", status="200"):
        return {
            "name": DRAFT % status,
            "fullPath": path + "?kept=1",
            "request": {
                "headers": {"Authorization": session(address or self.hooks.ADMIN)},
                "body": '{"subject": "Draft, edited"}',
            },
        }

    def send(self, transaction):
        with patch.object(self.hooks, "fixture_call", return_value={"id": 45}) as api:
            self.hooks.update_a_draft_of_its_own(transaction)
        return api

    def test_the_admins_cell_updates_a_draft_of_its_own(self):
        transaction = self.cell()
        body = transaction["request"]["body"]
        api = self.send(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/messages/draft/45?kept=1")
        self.assertEqual(transaction["request"]["body"], body)
        made = api.call_args.args[3]
        self.assertEqual((made["from"], made["to"]), (self.hooks.ADMIN, [self.hooks.ADMIN]))

    def test_everything_else_keeps_its_id(self):
        cells = {
            "another principal": self.cell("ordinary@restmail.test"),
            "a linked draft": self.cell(path="/api/v1/messages/draft/45"),
            "the missing example": self.cell(path="/api/v1/messages/draft/2147483647", status="404"),
        }
        for label, transaction in cells.items():
            with self.subTest(label):
                path = transaction["fullPath"]
                api = self.send(transaction)
                self.assertEqual(transaction["fullPath"], path)
                api.assert_not_called()

    def test_coverage_and_ownership_keep_their_own_targets(self):
        for name in ("CONTRACT_COVERAGE", "CONTRACT_OWNERSHIP"):
            with self.subTest(name), patch.dict(os.environ, {name: "1"}):
                transaction = self.cell()
                api = self.send(transaction)
                self.assertEqual(transaction["fullPath"], "/api/v1/messages/draft/12?kept=1")
                api.assert_not_called()


class RecoveryRequest(unittest.TestCase):
    def setUp(self):
        self.hooks, _ = load_hooks()

    def request(self, body, status="200"):
        transaction = {"name": RECOVERY % status, "request": {"body": json.dumps(body)}}
        self.hooks.a_recovery_request_for_an_address_of_its_own(transaction)
        return json.loads(transaction["request"]["body"])

    def test_each_documented_success_asks_about_its_own_address(self):
        addresses = {self.request({"email": "unknown@example.test"})["email"] for _ in range(6)}
        self.assertEqual(len(addresses), 6)
        for address in addresses:
            self.assertRegex(address, r"^unknown-[0-9]+@example\.test$")

    def test_generated_bodies_and_refusals_stay_as_they_are(self):
        for body, status in (
            ({"email": "someone@example.com"}, "200"),
            ({"email": "not-an-address"}, "422"),
            ({"email": "unknown@example.test"}, "422"),
            ({}, "200"),
            ([], "200"),
        ):
            with self.subTest(body=body, status=status):
                self.assertEqual(self.request(body, status), body)


if __name__ == "__main__":
    unittest.main()
