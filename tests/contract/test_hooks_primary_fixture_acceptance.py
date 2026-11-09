import json
import unittest
from unittest.mock import Mock, patch

from test_clean_start_batch_regressions import registered_hooks


class PrimaryFixtureAcceptance(unittest.TestCase):
    def setUp(self):
        environment = patch.dict("os.environ", {"CONTRACT_RESET_SEED": ""})
        environment.start()
        self.addCleanup(environment.stop)
        self.callbacks, self.loaded, self.stub = registered_hooks()

    def invoke(self, phase, operation, transaction, replacements):
        for callback in self.callbacks[phase].get(operation, []):
            with patch.dict(callback.__globals__, replacements):
                callback(transaction)

    def test_primary_attachment_reads_make_the_attachment_without_resetting(self):
        for expected in ("200", "206", "304", "412", "416"):
            with self.subTest(expected=expected):
                transaction = {
                    "name": f"/api/v1/attachments/{{id}} > Download > {expected}",
                    "fullPath": "/api/v1/attachments/1?download=1",
                    "request": {"body": "{}"},
                }
                make_attachment = Mock(return_value=42)
                reset = Mock()
                self.invoke("before", "getAttachment", transaction, {
                    "an_attachment_of_its_own": make_attachment,
                    "start_clean": reset,
                })
                make_attachment.assert_called_once_with(transaction)
                reset.assert_not_called()
                self.assertEqual(transaction["fullPath"], "/api/v1/attachments/42?download=1")
                self.assertEqual(self.stub.stash["attachment_made"], transaction["name"])

    def test_primary_attachment_proof_rejects_wrong_bytes(self):
        transaction = {
            "name": "/api/v1/attachments/{id} > Download > 200",
            "fullPath": "/api/v1/attachments/42",
            "request": {"body": "{}"},
            "real": {"statusCode": 200, "body": "wrong bytes", "headers": {}},
        }
        self.stub.stash["attachment_made"] = transaction["name"]
        self.invoke("after", "getAttachment", transaction, {})
        self.assertIn("fail", transaction)

    def test_primary_restmail_success_gets_a_recipient_and_nongreylisting_pipeline(self):
        transaction = {
            "name": "/restmail/messages > Deliver > 201 > application/json",
            "fullPath": "/restmail/messages",
            "request": {"body": json.dumps({"to": ["eve@restmail.test"]})},
        }
        recipient = Mock(return_value={"id": 42, "address": "own@restmail.test"})
        pipeline = Mock()
        reset = Mock()
        self.invoke("before", "restmailDeliver", transaction, {
            "a_recipient_of_its_own": recipient,
            "an_inbound_pipeline_without_greylisting": pipeline,
            "start_clean": reset,
        })
        recipient.assert_called_once_with(transaction)
        pipeline.assert_called_once_with(transaction)
        reset.assert_not_called()
        self.assertEqual(json.loads(transaction["request"]["body"])["to"], ["own@restmail.test"])
        self.assertEqual(self.stub.stash["recipient_made"]["id"], 42)

    def test_primary_restmail_proof_rejects_the_wrong_recipient(self):
        transaction = {
            "name": "/restmail/messages > Deliver > 201 > application/json",
            "fullPath": "/restmail/messages",
            "request": {"body": "{}"},
            "real": {"statusCode": 201, "body": json.dumps({
                "data": {"delivered": ["eve@restmail.test"], "failed": []},
            })},
        }
        self.stub.stash["recipient_made"] = {
            "id": 42, "address": "own@restmail.test", "name": transaction["name"],
        }
        self.invoke("after", "restmailDeliver", transaction, {})
        self.assertIn("fail", transaction)

    def test_primary_refusals_keep_their_inputs_and_make_no_success_fixture(self):
        for operation, path, expected in (
            ("getAttachment", "/api/v1/attachments/2147483647", "404"),
            ("restmailDeliver", "/restmail/messages", "401"),
        ):
            with self.subTest(operation=operation):
                transaction = {
                    "name": f"{path} > Refused > {expected} > application/json",
                    "fullPath": path,
                    "request": {"body": json.dumps({"to": ["eve@restmail.test"]})},
                }
                original = json.loads(json.dumps(transaction))
                fixture = Mock()
                self.invoke("before", operation, transaction, {
                    "an_attachment_of_its_own": fixture,
                    "a_recipient_of_its_own": fixture,
                    "an_inbound_pipeline_without_greylisting": fixture,
                    "start_clean": fixture,
                })
                fixture.assert_not_called()
                self.assertEqual(transaction, original)
