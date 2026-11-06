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

    def test_a_trace_is_read_from_a_message_sent_after_the_reset(self):
        sent = self.send(
            self.module.on_a_message_of_its_own,
            "/api/v1/admin/messages/2/trace",
            "/api/v1/admin/messages/{id}/trace > Get message trace > 200",
        )
        self.assertEqual(sent["fullPath"], "/api/v1/admin/messages/700/trace")
        self.assertEqual(
            self.events,
            ["reset", "POST /api/v1/messages/send", "GET /api/v1/admin/messages/700/trace"],
        )

    def test_a_trace_is_read_once_it_has_been_written(self):
        # The recorder writes traces in the background, so the first ask can
        # find none yet: the hook asks again until it is there.
        asked = []

        def written_later(request, timeout):
            if request.full_url.endswith("/trace") and not asked:
                asked.append(request.full_url)
                self.events.append("GET (not yet written)")
                raise self.module.urllib.error.HTTPError(request.full_url, 404, "Not Found", {}, None)
            return self.answer(request, timeout)

        self.enterContext(patch.object(self.module.urllib.request, "urlopen", written_later))
        self.enterContext(patch.object(self.module.time, "sleep", lambda seconds: None))
        sent = self.send(
            self.module.on_a_message_of_its_own,
            "/api/v1/admin/messages/2/trace",
            "/api/v1/admin/messages/{id}/trace > Get message trace > 200",
        )
        self.assertEqual(sent["fullPath"], "/api/v1/admin/messages/700/trace")
        self.assertEqual(
            self.events,
            [
                "reset",
                "POST /api/v1/messages/send",
                "GET (not yet written)",
                "GET /api/v1/admin/messages/700/trace",
            ],
        )

    def test_a_trace_never_written_names_the_sample_rate(self):
        def never_written(request, timeout):
            if request.full_url.endswith("/trace"):
                raise self.module.urllib.error.HTTPError(request.full_url, 404, "Not Found", {}, None)
            return self.answer(request, timeout)

        self.enterContext(patch.object(self.module.urllib.request, "urlopen", never_written))
        self.enterContext(patch.object(self.module, "TRACE_WAIT_SECONDS", 0))
        with self.assertRaisesRegex(RuntimeError, "no trace was written for message 700.*TRACE_SAMPLE_RATE=1"):
            self.send(
                self.module.on_a_message_of_its_own,
                "/api/v1/admin/messages/2/trace",
                "/api/v1/admin/messages/{id}/trace > Get message trace > 200",
            )

    def test_the_fixed_trace_wait_is_left_to_runs_without_a_reset(self):
        slept = []
        self.enterContext(patch.object(self.module.time, "sleep", slept.append))
        self.module.wait_for_the_trace({"name": "x"})
        self.assertEqual(slept, [])
        with patch.dict(os.environ, {"CONTRACT_RESET_SEED": ""}):
            self.module.wait_for_the_trace({"name": "x"})
        self.assertEqual(slept, [1])

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



class AttachmentCleanStart(unittest.TestCase):
    """getAttachment reads an attachment of its own, with known bytes, made
    after its reset; refusals keep their ids."""

    def setUp(self):
        self.module, self.stub = load_hooks()
        self.events = []
        self.imported = []
        self.enterContext(patch.dict(os.environ, {"CONTRACT_RESET_SEED": "1"}))
        self.enterContext(patch.object(self.module, "admin_token", lambda origin: "fixture-token"))
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
        self.events.append(request.get_method() + " " + request.full_url.split("8080", 1)[1])
        if request.get_method() == "POST":
            self.imported.append(json.loads(request.data))
            data = {"id": 700}
        else:
            data = [{"id": 91, "filename": "contract.bin"}]
        return Reply(json.dumps({"data": data}).encode())

    def send(self, path, name, headers=None):
        transaction = {
            "name": name,
            "fullPath": path,
            "protocol": "http:",
            "host": "server.test",
            "port": 8080,
            "request": {"method": "GET", "body": "", "headers": dict(headers or {})},
        }
        self.module.on_an_attachment_of_its_own(transaction)
        return transaction

    def answered(self, transaction, status, body=b"", headers=None):
        transaction["real"] = {
            "statusCode": status,
            "headers": dict(headers or {}),
            "body": body.decode("latin-1"),
        }
        self.module.the_attachment_answers_its_bytes(transaction)
        return transaction

    def test_a_read_goes_to_an_attachment_of_its_own_made_after_the_reset(self):
        name = "/api/v1/attachments/{id} > Download an attachment > 206 > application/octet-stream"
        sent = self.send("/api/v1/attachments/1", name, {"Range": "bytes=0-9"})
        self.assertEqual(sent["fullPath"], "/api/v1/attachments/91")
        self.assertEqual(
            self.events,
            ["reset", "POST /api/v1/accounts/4/messages/import", "GET /api/v1/messages/700/attachments"],
        )
        raw = base64.b64decode(self.imported[0]["raw_message"])
        self.assertIn(base64.b64encode(self.module.ATTACHMENT_BYTES), raw)
        self.assertIn(b'filename="contract.bin"', raw)
        self.assertEqual(self.stub.stash["attachment_made"], name)

    def test_every_read_of_an_attachment_that_is_there_gets_one(self):
        for status in ("200", "206", "304", "412", "416"):
            self.events.clear()
            sent = self.send("/api/v1/attachments/1", f"/api/v1/attachments/{{id}} > Download > {status}")
            self.assertEqual(sent["fullPath"], "/api/v1/attachments/91", status)

    def test_refusals_reset_and_keep_their_ids(self):
        for path, status in (
            ("/api/v1/attachments/0", "422"),
            ("/api/v1/attachments/2147483647", "404"),
            ("/api/v1/attachments/1", "401"),
            ("/api/v1/attachments/7", "403"),
            ("/api/v1/attachments/3", "500"),
            ("/api/v1/attachments/1", "429"),
        ):
            self.events.clear()
            sent = self.send(path, f"/api/v1/attachments/{{id}} > Download > {status} > application/json")
            self.assertEqual(sent["fullPath"], path)
            self.assertEqual(self.events, ["reset"])
        self.assertNotIn("attachment_made", self.stub.stash)

    def test_without_a_reset_nothing_changes(self):
        with patch.dict(os.environ, {"CONTRACT_RESET_SEED": ""}):
            sent = self.send("/api/v1/attachments/1", "/api/v1/attachments/{id} > Download > 200")
            self.assertEqual(sent["fullPath"], "/api/v1/attachments/1")
            self.assertEqual(self.events, [])
            sent["real"] = {"statusCode": 200, "body": "other"}
            self.module.the_attachment_answers_its_bytes(sent)
            self.assertNotIn("fail", sent)

    def test_the_answer_is_held_to_the_attachments_bytes(self):
        data = self.module.ATTACHMENT_BYTES
        total = len(data)
        name = "/api/v1/attachments/{id} > Download > 200 > application/octet-stream"
        whole = self.answered(self.send("/api/v1/attachments/1", name), 200, data)
        self.assertNotIn("fail", whole)
        other = self.answered(self.send("/api/v1/attachments/1", name), 200, b"seeded bytes")
        self.assertIn("the body (12 bytes) is not the", other["fail"])

        name = "/api/v1/attachments/{id} > Download > 206 > application/octet-stream"
        ranged = self.send("/api/v1/attachments/1", name, {"Range": "bytes=0-9"})
        self.answered(ranged, 206, data[:10], {"content-range": f"bytes 0-9/{total}"})
        self.assertNotIn("fail", ranged)
        ranged = self.send("/api/v1/attachments/1", name, {"Range": "bytes=0-9"})
        self.answered(ranged, 206, data[:10], {"content-range": "bytes 0-9/4"})
        self.assertIn("Content-Range is 'bytes 0-9/4'", ranged["fail"])

        name = "/api/v1/attachments/{id} > Download > 206 > multipart/byteranges"
        parts = b"".join(
            b"--b\r\nContent-Range: bytes %d-%d/%d\r\nContent-Type: application/octet-stream\r\n\r\n%s\r\n"
            % (first, last, total, data[first : last + 1])
            for first, last in ((0, 1), (4, 5))
        ) + b"--b--\r\n"
        multiple = self.send("/api/v1/attachments/1", name, {"Range": "bytes=0-1,4-5"})
        self.answered(multiple, 206, parts)
        self.assertNotIn("fail", multiple)
        multiple = self.send("/api/v1/attachments/1", name, {"Range": "bytes=0-1,4-5"})
        self.answered(multiple, 206, parts.replace(data[4:6], b"??"))
        self.assertIn("no part holds bytes 4-5", multiple["fail"])

        name = "/api/v1/attachments/{id} > Download > 416 > text/plain"
        past = self.send("/api/v1/attachments/1", name, {"Range": "bytes=1000-"})
        self.answered(past, 416, b"invalid range: failed to overlap\n", {"Content-Range": f"bytes */{total}"})
        self.assertNotIn("fail", past)

        for status in (304, 412):
            name = f"/api/v1/attachments/{{id}} > Download > {status}"
            bodyless = self.answered(self.send("/api/v1/attachments/1", name), status)
            self.assertNotIn("fail", bodyless)

    def test_a_read_with_no_attachment_made_for_it_fails(self):
        transaction = {"name": "/api/v1/attachments/{id} > Download > 304", "request": {}}
        self.answered(transaction, 304)
        self.assertEqual(transaction["fail"], "no attachment was made for this read")

    def test_another_status_and_refusals_are_left_to_the_status_check(self):
        name = "/api/v1/attachments/{id} > Download > 200 > application/octet-stream"
        missing = self.answered(self.send("/api/v1/attachments/1", name), 404, b"{}")
        self.assertNotIn("fail", missing)
        refused = {"name": "/api/v1/attachments/{id} > Download > 422", "request": {}}
        self.answered(refused, 422, b"{}")
        self.assertNotIn("fail", refused)


if __name__ == "__main__":
    unittest.main()
