"""The delivery and federation operations start clean in the own-data phase (#1385).

With CONTRACT_RESET_SEED=1 each exchange resets first, refusals included, on
the run's endpoint. A documented delivery or mailbox check success goes to a
mailbox made for it after the reset, and its after hook fails it unless the
answer names that mailbox.
"""

import importlib.util
import io
import json
import os
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.before = lambda name: lambda function: function
    stub.after = lambda name: lambda function: function
    stub.get_json = lambda transaction, which="request": json.loads(
        transaction[which]["body"] or "null"
    )

    def set_json(transaction, value):
        transaction["request"]["body"] = json.dumps(value)

    stub.set_json = set_json
    spec = importlib.util.spec_from_file_location(
        "contract_delivery_clean_start_hooks", ROOT / "tests/contract/hooks.py"
    )
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, stub


class Reply(io.BytesIO):
    status = 201

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


CHECK = "/api/mailboxes > Check if a mailbox address exists (used by SMTP gateway) > 200 > application/json"
DELIVER = "/api/v1/messages/deliver > Deliver a message from the SMTP gateway > 201 > application/json"
RESTMAIL_CHECK = "/restmail/mailboxes > Check if a recipient mailbox exists > 200 > application/json"
RESTMAIL_DELIVER = "/restmail/messages > Deliver a message via RESTMAIL protocol > 201 > application/json"


class DeliveryCleanStart(unittest.TestCase):
    def setUp(self):
        self.module, self.stub = load_hooks()
        self.events = []
        self.pipelines = []
        self.enterContext(
            patch.dict(
                os.environ,
                {"CONTRACT_RESET_SEED": "1", "CONTRACT_SETUP_ORIGIN": "http://public.test:8080"},
            )
        )
        self.enterContext(patch.object(self.module, "admin_token", lambda origin: "fixture-token"))
        self.enterContext(patch.object(self.module, "mfa_admin_token", lambda origin: "mfa-token"))
        self.enterContext(patch.object(self.module.urllib.request, "urlopen", self.answer))
        self.enterContext(patch.object(self.module.time, "time_ns", lambda: 42))
        self.enterContext(
            patch.object(
                self.module.reset_seed,
                "reset_and_seed",
                lambda origin, token, fixtures: self.events.append("reset " + origin),
            )
        )

    def answer(self, request, timeout):
        self.events.append(request.get_method() + " " + request.full_url)
        if request.full_url.endswith("/api/v1/admin/domains?limit=200"):
            return Reply(json.dumps({"data": [{"id": 9, "name": "other.test"}, {"id": 1, "name": "restmail.test"}]}).encode())
        body = json.loads(request.data)
        self.pipelines.extend([body] if request.full_url.endswith("/pipelines") else [])
        return Reply(json.dumps({"data": {"id": 77, "address": body.get("address")}}).encode())

    def send(self, path, name, body=None, operation_hooks=()):
        transaction = {
            "name": name,
            "fullPath": path,
            "protocol": "https:",
            "host": "internal.test",
            "port": 8443,
            "request": {
                "method": "POST" if body is not None else "GET",
                "body": json.dumps(body) if body is not None else "",
                "headers": {},
            },
        }
        self.module.delivery_after_a_reset(transaction)
        for hook in operation_hooks:
            hook(transaction)
        return transaction

    def answered(self, transaction, status, data):
        transaction["real"] = {"statusCode": status, "body": json.dumps({"data": data})}
        self.module.the_answer_names_the_recipient_made(transaction)
        return transaction

    MADE = "contract-own-recipient-42@restmail.test"
    MADE_ON_THE_PUBLIC_LISTENER = ["reset http://public.test:8080", "POST http://public.test:8080/api/v1/accounts"]

    def test_the_gateway_check_looks_up_a_mailbox_of_its_own_on_the_public_listener(self):
        sent = self.send(
            "/api/mailboxes?address=user%40example.com",
            CHECK,
            operation_hooks=(self.module.look_up_a_hosted_mailbox,),
        )
        self.assertEqual(sent["fullPath"], "/api/mailboxes?address=contract-own-recipient-42%40restmail.test")
        self.assertEqual(self.events, self.MADE_ON_THE_PUBLIC_LISTENER)
        self.assertNotIn("fail", self.answered(sent, 200, {"exists": True, "mailbox_id": 77, "address": self.MADE}))

    def test_a_gateway_delivery_goes_to_a_mailbox_of_its_own(self):
        sent = self.send(
            "/api/v1/messages/deliver",
            DELIVER,
            {"address": "admin@restmail.test", "sender": "sender@example.net", "subject": "S", "body_text": "Hello"},
            operation_hooks=(self.module.deliver_a_whole_message,),
        )
        body = json.loads(sent["request"]["body"])
        self.assertEqual(body["address"], self.MADE)
        # The whole message is built for the recipient made, after the reset.
        self.assertIn("To: " + self.MADE, self.module.base64.b64decode(body["raw_message"]).decode())
        self.assertEqual(self.events, self.MADE_ON_THE_PUBLIC_LISTENER)
        self.assertNotIn("fail", self.answered(sent, 201, {"id": 5, "mailbox_id": 77}))

    def test_the_restmail_check_and_delivery_go_to_a_mailbox_of_its_own(self):
        # Vertrag sends the check's 200 a generated address no mailbox has.
        sent = self.send("/restmail/mailboxes?address=user%40example.test", RESTMAIL_CHECK)
        self.assertEqual(sent["fullPath"], "/restmail/mailboxes?address=contract-own-recipient-42%40restmail.test")
        self.assertNotIn("fail", self.answered(sent, 200, {"exists": True, "address": self.MADE}))
        self.events.clear()
        sent = self.send(
            "/restmail/messages",
            RESTMAIL_DELIVER,
            {"from": "someone@ext-contract.test", "to": ["eve@restmail.test"], "subject": "S"},
        )
        self.assertEqual(json.loads(sent["request"]["body"])["to"], [self.MADE])
        # restmail.test gets an inbound pipeline that does not greylist.
        self.assertEqual(
            self.events,
            self.MADE_ON_THE_PUBLIC_LISTENER
            + ["GET http://public.test:8080/api/v1/admin/domains?limit=200", "POST http://public.test:8080/api/v1/admin/pipelines"],
        )
        self.assertEqual(
            self.pipelines,
            [{"domain_id": 1, "direction": "inbound", "filters": [{"name": "header_validate", "config": {}}], "active": True}],
        )
        self.assertNotIn("fail", self.answered(sent, 201, {"delivered": [self.MADE], "failed": []}))

    def test_an_answer_for_another_mailbox_fails(self):
        for path, name, body, data, wanted in (
            ("/api/mailboxes?address=user%40example.com", CHECK, None,
             {"exists": True, "mailbox_id": 1, "address": "admin@restmail.test"}, "the mailbox id is 1"),
            ("/api/mailboxes?address=user%40example.com", CHECK, None,
             {"exists": False, "address": self.MADE, "mailbox_id": 77}, "exists is False"),
            ("/api/v1/messages/deliver", DELIVER,
             {"address": "admin@restmail.test", "sender": "sender@example.net"},
             {"id": 5, "mailbox_id": 1}, "the message is in mailbox 1"),
            ("/restmail/mailboxes?address=admin%40restmail.test", RESTMAIL_CHECK, None,
             {"exists": True, "address": "admin@restmail.test"}, "the address is 'admin@restmail.test'"),
            ("/restmail/messages", RESTMAIL_DELIVER, {"to": ["eve@restmail.test"]},
             {"delivered": [], "failed": [self.MADE]}, "delivered is []"),
        ):
            with self.subTest(name=name, wanted=wanted):
                failed = self.answered(self.send(path, name, body), 200 if " > 200" in name else 201, data)
                self.assertIn(wanted, failed["fail"])
                self.assertIn(self.MADE, failed["fail"])

    def test_a_success_with_nothing_made_for_it_fails(self):
        transaction = {"name": CHECK, "fullPath": "/api/mailboxes?address=user%40example.com"}
        self.answered(transaction, 200, {"exists": True})
        self.assertEqual(transaction["fail"], "no mailbox was made for this exchange")

    def test_refusals_reset_and_keep_what_they_name(self):
        for path, name, body in (
            ("/api/mailboxes?address=", CHECK.replace(" > 200", " > 422"), None),
            ("/api/mailboxes?address=unavailable%40example.com", CHECK, None),
            ("/api/v1/messages/deliver", DELIVER, {"address": "nobody@restmail.test", "sender": "sender@example.net"}),
            ("/api/v1/messages/deliver", DELIVER.replace(" > 201", " > 422"), {"address": ""}),
            ("/api/v1/messages/deliver", DELIVER.replace(" > 201", " > 422"), {"subject": "Hello", "body_text": "Hello"}),
            ("/restmail/messages", RESTMAIL_DELIVER.replace(" > 201", " > 422"), {"from": "someone@ext-contract.test"}),
        ):
            with self.subTest(path=path, body=body):
                self.events.clear()
                sent = self.send(path, name, body)
                self.assertEqual(sent["fullPath"], path)
                if body is not None:
                    self.assertEqual(json.loads(sent["request"]["body"]), body)
                self.assertEqual(self.events, ["reset http://public.test:8080"])
                self.assertNotIn("recipient_made", self.stub.stash)
                # The after hook leaves a refusal, or an answer for no mailbox made, to its status.
                if " > 422" in name:
                    self.answered(sent, 422, {})
                    self.assertNotIn("fail", sent)

    def test_the_reads_that_need_nothing_made_only_reset(self):
        for path, name in (
            ("/.well-known/restmail?domain=restmail.test", "/.well-known/restmail > Discover > 200 > application/json"),
            ("/restmail/capabilities", "/restmail/capabilities > Get RESTMAIL server capabilities > 200 > application/json"),
            ("/restmail/mailboxes?address=", RESTMAIL_CHECK.replace(" > 200", " > 422")),
        ):
            with self.subTest(path=path):
                self.events.clear()
                sent = self.send(path, name)
                self.assertEqual(sent["fullPath"], path)
                self.assertEqual(self.events, ["reset http://public.test:8080"])

    def test_without_the_switch_nothing_resets_or_moves(self):
        with patch.dict(os.environ, {"CONTRACT_RESET_SEED": ""}):
            sent = self.send("/restmail/mailboxes?address=admin%40restmail.test", RESTMAIL_CHECK)
            self.assertEqual(sent["fullPath"], "/restmail/mailboxes?address=admin%40restmail.test")
            self.answered(sent, 200, {"exists": True, "address": "admin@restmail.test"})
        self.assertEqual(self.events, [])
        self.assertNotIn("fail", sent)

    def test_setup_calls_use_the_exchanges_own_origin_when_no_endpoint_is_named(self):
        with patch.dict(os.environ, {"CONTRACT_SETUP_ORIGIN": ""}):
            self.send("/restmail/capabilities", "/restmail/capabilities > Get > 200 > application/json")
        self.assertEqual(self.events, ["reset https://internal.test:8443"])


if __name__ == "__main__":
    unittest.main()
