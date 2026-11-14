import copy
import json
import unittest
from unittest.mock import MagicMock, patch

from test_hooks_send_message import load_hooks


class SendIdempotencyHookTests(unittest.TestCase):
    def setUp(self):
        self.module, self.stub = load_hooks()
        self.transaction = {
            "name": "/api/v1/messages/send > Send a message > 409 > application/json",
            "protocol": "http:",
            "host": "api.test",
            "port": 8080,
            "fullPath": "/api/v1/messages/send",
            "request": {
                "headers": {
                    "Authorization": "Bearer caller",
                    "Idempotency-Key": "contract-idempotency-conflict",
                },
                "body": '{"body_text":"A different request"}',
            },
        }

    def test_each_route_gets_a_fresh_key_primed_on_send(self):
        response = MagicMock()
        response.__enter__.return_value.status = 201
        for index, path in enumerate(("/api/v1/messages/send", "/api/v1/messages/draft/1/send", "/api/v1/messages/1/forward")):
            with self.subTest(path=path):
                transaction = copy.deepcopy(self.transaction)
                transaction["fullPath"] = path
                before_body = transaction["request"]["body"]
                with patch.object(self.module, "admin_token", return_value="setup"), patch.object(self.module.time, "time_ns", return_value=index), patch.object(self.module.urllib.request, "urlopen", return_value=response) as call:
                    self.module.a_used_send_idempotency_key(transaction)
                request = call.call_args.args[0]
                self.assertEqual(request.full_url, "http://api.test:8080/api/v1/messages/send")
                self.assertEqual(request.get_header("Authorization"), "Bearer setup")
                self.assertEqual(request.get_header("Idempotency-key"), transaction["request"]["headers"]["Idempotency-Key"])
                self.assertEqual(transaction["request"]["headers"]["Idempotency-Key"], "contract-send-" + str(index))
                self.assertEqual(transaction["request"]["body"], before_body)
                self.assertEqual(transaction["fullPath"], path)
                self.assertEqual(json.loads(request.data)["body_text"], "The first request")

    def test_other_requests_get_an_unused_key_of_the_same_length(self):
        # Keys are account-scoped, and Vertrag sends the same values on send,
        # draft send and forward: the 409's example key on every request, and
        # header probes such as maxLength (255). None may be claimed by one
        # operation for the next to conflict on, and a length probe keeps its length.
        cases = (
            (201, "contract-idempotency-conflict"),
            (404, "contract-idempotency-conflict"),
            (201, "x" * 255),
            (201, "x" * 256),
            (409, "x" * 255),
            (201, "k"),
        )
        keys = set()
        for index, (status, sent) in enumerate(cases):
            with self.subTest(status=status, length=len(sent)):
                transaction = copy.deepcopy(self.transaction)
                transaction["name"] = transaction["name"].replace("409", str(status))
                transaction["request"]["headers"]["Idempotency-Key"] = sent
                before = copy.deepcopy(transaction)
                with patch.object(self.module.time, "time_ns", return_value=1000 + index), patch.object(self.module.urllib.request, "urlopen") as call:
                    self.module.a_used_send_idempotency_key(transaction)
                call.assert_not_called()
                key = transaction["request"]["headers"]["Idempotency-Key"]
                self.assertEqual(len(key), len(sent))
                self.assertNotEqual(key, sent)
                keys.add(key)
                transaction["request"]["headers"]["Idempotency-Key"] = sent
                self.assertEqual(transaction, before)
        self.assertEqual(len(keys), len(cases))

    def test_a_request_without_a_key_is_untouched(self):
        for headers in ({"Authorization": "Bearer caller"}, {"Authorization": "Bearer caller", "Idempotency-Key": ""}):
            transaction = copy.deepcopy(self.transaction)
            transaction["name"] = transaction["name"].replace("409", "201")
            transaction["request"]["headers"] = headers
            before = copy.deepcopy(transaction)
            with patch.object(self.module.urllib.request, "urlopen") as call:
                self.module.a_used_send_idempotency_key(transaction)
            call.assert_not_called()
            self.assertEqual(transaction, before)

    def test_failed_setup_does_not_replace_the_key(self):
        response = MagicMock()
        response.__enter__.return_value.status = 202
        with patch.object(self.module, "admin_token", return_value="setup"), patch.object(self.module.urllib.request, "urlopen", return_value=response):
            with self.assertRaisesRegex(RuntimeError, "did not queue"):
                self.module.a_used_send_idempotency_key(self.transaction)
        self.assertEqual(self.transaction["request"]["headers"]["Idempotency-Key"], "contract-idempotency-conflict")


if __name__ == "__main__":
    unittest.main()
