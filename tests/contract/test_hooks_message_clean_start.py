"""The message and draft operations start clean in the own-data phase (#1385).

With CONTRACT_RESET_SEED=1 each exchange resets first, refusals included, and
one aimed at a positive id gets a message (or draft) of its own, made after the
reset, since the one sendMessage's or saveDraft's link names went with it.
"""

import base64
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
    stub.get_json = lambda transaction: json.loads(transaction["request"]["body"] or "null")
    spec = importlib.util.spec_from_file_location(
        "contract_message_clean_start_hooks", ROOT / "tests/contract/hooks.py"
    )
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, stub


class Reply(io.BytesIO):
    status = 200

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


class MessageCleanStart(unittest.TestCase):
    def setUp(self):
        self.module, self.stub = load_hooks()
        self.requests = []
        self.events = []
        self.enterContext(patch.dict(os.environ, {"CONTRACT_RESET_SEED": "1"}))
        self.enterContext(
            patch.object(self.module, "admin_token", lambda origin: "fixture-token")
        )
        self.enterContext(patch.object(self.module, "mailbox_id", lambda origin, address: 4))
        self.enterContext(patch.object(self.module.urllib.request, "urlopen", self.answer))
        self.enterContext(
            patch.object(
                self.module.reset_seed,
                "reset_and_seed",
                lambda origin, token, fixtures: self.events.append("reset"),
            )
        )

    def answer(self, request, timeout):
        self.requests.append(request)
        self.events.append(request.get_method() + " " + request.full_url.split("8080", 1)[1])
        if request.get_method() == "GET":
            data = {"id": 700, "mailbox_id": 4, "thread_id": "<t@restmail.test>"}
        else:
            data = {"id": 700}
        return Reply(json.dumps({"data": data}).encode())

    def send(self, hook, path, name):
        transaction = {
            "name": name,
            "fullPath": path,
            "protocol": "http:",
            "host": "server.test",
            "port": 8080,
            "request": {"method": "GET", "body": "", "headers": {}},
        }
        hook(transaction)
        return transaction

    def test_a_linked_message_is_one_of_its_own_made_after_the_reset(self):
        sent = self.send(
            self.module.on_a_message_of_its_own,
            "/api/v1/messages/2/raw",
            "/api/v1/messages/{id}/raw > Get the raw RFC 2822 message source > 200",
        )
        self.assertEqual(sent["fullPath"], "/api/v1/messages/700/raw")
        self.assertEqual(self.events, ["reset", "POST /api/v1/accounts/4/messages/import"])
        self.assertEqual(self.module.hooks.stash["clean_starts"], 1)

    def test_a_trace_is_read_from_a_message_sent_after_the_reset(self):
        sent = self.send(
            self.module.on_a_message_of_its_own,
            "/api/v1/admin/messages/2/trace",
            "/api/v1/admin/messages/{id}/trace > Get message trace > 200",
        )
        self.assertEqual(sent["fullPath"], "/api/v1/admin/messages/700/trace")
        self.assertEqual(self.events, ["reset", "POST /api/v1/messages/send"])

    def test_refusals_reset_and_keep_their_ids(self):
        for path, name in (
            ("/api/v1/messages/0", "/api/v1/messages/{id} > Get a message by ID > 422"),
            ("/api/v1/messages/2147483647", "/api/v1/messages/{id} > Get a message by ID > 404"),
            ("/api/v1/messages/9/forward", "/api/v1/messages/{id}/forward > Forward a message > 404"),
        ):
            self.events.clear()
            sent = self.send(self.module.on_a_message_of_its_own, path, name)
            self.assertEqual(sent["fullPath"], path)
            self.assertEqual(self.events, ["reset"])

    def test_a_thread_is_the_one_of_a_message_made_after_the_reset(self):
        sent = self.send(
            self.module.on_a_thread_of_its_own,
            "/api/v1/accounts/9/threads/%3Cold%40restmail.test%3E",
            "/api/v1/accounts/{id}/threads/{threadID} > Get all messages in a thread > 200",
        )
        self.assertEqual(
            sent["fullPath"], "/api/v1/accounts/4/threads/%3Ct%40restmail.test%3E"
        )
        self.assertEqual(
            self.events,
            ["reset", "POST /api/v1/accounts/4/messages/import", "GET /api/v1/messages/700"],
        )

    def test_a_linked_draft_is_one_of_its_own_made_after_the_reset(self):
        sent = self.send(
            self.module.on_a_draft_of_its_own,
            "/api/v1/messages/draft/1",
            "/api/v1/messages/draft/{id} > Update an existing draft > 200",
        )
        self.assertEqual(sent["fullPath"], "/api/v1/messages/draft/700")
        self.assertEqual(self.events, ["reset", "POST /api/v1/messages/draft"])
        missing = self.send(
            self.module.on_a_draft_of_its_own,
            "/api/v1/messages/draft/12",
            "/api/v1/messages/draft/{id} > Update an existing draft > 404",
        )
        self.assertEqual(missing["fullPath"], "/api/v1/messages/draft/12")

    def test_without_a_reset_nothing_changes(self):
        os.environ.pop("CONTRACT_RESET_SEED")
        for hook, path in (
            (self.module.on_a_message_of_its_own, "/api/v1/messages/2"),
            (self.module.on_a_thread_of_its_own, "/api/v1/accounts/4/threads/x"),
            (self.module.on_a_draft_of_its_own, "/api/v1/messages/draft/1"),
        ):
            sent = self.send(hook, path, "x > y > 200")
            self.assertEqual(sent["fullPath"], path)
        self.assertEqual(self.events, [])


if __name__ == "__main__":
    unittest.main()
