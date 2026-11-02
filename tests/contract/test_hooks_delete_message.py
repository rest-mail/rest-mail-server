"""deleteMessage's example gets a message of its own, not whatever row 51 is."""

import base64
import importlib.util
import io
import json
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
# The source transaction's name, which is what a before hook receives in every
# phase of Vertrag 0.42.0, coverage included.
REAL_NAME = "/api/v1/messages/{id} > Delete a message > 204"


def load_hooks():
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    registered = []
    stub.before = lambda name: lambda function: registered.append(name) or function
    stub.after = lambda name: lambda function: function
    spec = importlib.util.spec_from_file_location(
        "contract_delete_message_hooks", ROOT / "tests/contract/hooks.py"
    )
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, registered


def token_for(address):
    claims = base64.urlsafe_b64encode(json.dumps({"address": address}).encode())
    return "Bearer header." + claims.decode().rstrip("=") + ".signature"


class Reply(io.BytesIO):
    status = 200

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


class DeleteMessageFixture(unittest.TestCase):
    def setUp(self):
        self.module, registered = load_hooks()
        self.assertIn("deleteMessage", registered)
        self.requests = []
        self.enterContext(
            patch.object(self.module, "admin_token", lambda origin: "fixture-token")
        )
        self.enterContext(patch.object(self.module, "mailbox_id", lambda origin, address: 4))
        self.enterContext(patch.object(self.module.urllib.request, "urlopen", self.answer))

    def answer(self, request, timeout):
        self.requests.append(request)
        return Reply(json.dumps({"data": {"id": 700}}).encode())

    def delete(self, path, address):
        headers = {"Authorization": token_for(address)} if address else {}
        transaction = {
            "name": REAL_NAME,
            "fullPath": path,
            "protocol": "http:",
            "host": "server.test",
            "port": 8080,
            "request": {"method": "DELETE", "body": "", "headers": headers},
        }
        self.module.delete_a_message_we_made(transaction)
        return transaction

    def test_the_fixture_admins_example_deletes_a_message_it_imported(self):
        transaction = self.delete("/api/v1/messages/51", self.module.ADMIN)
        self.assertEqual(transaction["fullPath"], "/api/v1/messages/700")
        self.assertEqual(len(self.requests), 1)
        self.assertEqual(
            self.requests[0].full_url,
            "http://server.test:8080/api/v1/accounts/4/messages/import",
        )
        body = json.loads(self.requests[0].data)
        self.assertEqual(body["folder"], "Contract delete")
        self.assertIn(b"Message-ID: <delete-", base64.b64decode(body["raw_message"]))

    def test_each_request_imports_a_message_of_its_own(self):
        first = self.delete("/api/v1/messages/51", self.module.ADMIN)
        second = self.delete("/api/v1/messages/51", self.module.ADMIN)
        self.assertEqual(first["fullPath"], second["fullPath"])
        ids = [
            base64.b64decode(json.loads(request.data)["raw_message"]).split(b"\r\n")[0]
            for request in self.requests
        ]
        self.assertEqual(len(set(ids)), 2)

    def test_a_query_survives_the_new_id(self):
        transaction = self.delete("/api/v1/messages/51?permanent=true", self.module.ADMIN)
        self.assertEqual(transaction["fullPath"], "/api/v1/messages/700?permanent=true")

    def test_other_requests_are_sent_as_they_are(self):
        for path, address in (
            ("/api/v1/messages/1", self.module.ADMIN),
            ("/api/v1/messages/0", self.module.ADMIN),
            ("/api/v1/messages/2147483647", self.module.ADMIN),
            ("/api/v1/messages/1.5", self.module.ADMIN),
            ("/api/v1/messages/51", None),
            ("/api/v1/messages/51", "ordinary@restmail.test"),
        ):
            with self.subTest(path=path, address=address):
                self.assertEqual(self.delete(path, address)["fullPath"], path)
        self.assertEqual(self.requests, [])

    def test_an_import_without_a_message_stops_the_request(self):
        with patch.object(
            self.module.urllib.request,
            "urlopen",
            lambda request, timeout: Reply(b'{"data": {}}'),
        ):
            with self.assertRaises(RuntimeError):
                self.delete("/api/v1/messages/51", self.module.ADMIN)


if __name__ == "__main__":
    unittest.main()
