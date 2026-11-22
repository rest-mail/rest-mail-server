"""Attachment fixtures preserve invalid targets and upload exact binary bytes."""

import copy
import io
import runpy
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent


class DraftAttachmentHooksTests(unittest.TestCase):
    def setUp(self):
        self.registered = {}
        stub = types.ModuleType("vertrag_hooks")
        stub.stash = {}

        def register(stage):
            def decorator(operation):
                def retain(function):
                    self.registered[stage, operation] = function
                    return function
                return retain
            return decorator

        stub.before, stub.after = register("before"), register("after")
        with patch.dict(sys.modules, vertrag_hooks=stub):
            module = runpy.run_path(str(ROOT / "draft_attachment_hooks.py"))
        self.calls = []

        def fixture(transaction, method, path, body=None):
            self.calls.append((method, path, body))
            return {"id": 81}

        module["register_draft_attachment_hooks"](fixture, lambda origin: "setup", "admin@restmail.test")

    def transaction(self, operation, status, method, suffix="", target="1"):
        return {
            "name": f"/api/v1/messages/draft/{{id}}/attachments > {operation} > {status} > application/json",
            "fullPath": f"http://localhost:8080/api/v1/messages/draft/{target}/attachments{suffix}?kept=true",
            "protocol": "http:", "host": "localhost", "port": 8080,
            "request": {"method": method, "body": "attachment", "headers": {}},
        }

    def test_upload_gets_a_draft_without_changing_bytes(self):
        transaction = self.transaction("uploadDraftAttachment", 201, "POST")
        request = copy.deepcopy(transaction["request"])
        self.registered["before", "uploadDraftAttachment"](transaction)
        self.assertIn("/draft/81/attachments?kept=true", transaction["fullPath"])
        self.assertEqual(transaction["request"], request)
        self.registered["after", "uploadDraftAttachment"](transaction)
        self.assertEqual(self.calls[-1][:2], ("DELETE", "/api/v1/messages/81?permanent=true"))

    def test_list_and_delete_upload_a_real_fixture(self):
        for operation, method, status, suffix in (("listDraftAttachments", "GET", 200, ""), ("deleteDraftAttachment", "DELETE", 204, "/1")):
            with self.subTest(operation=operation):
                transaction = self.transaction(operation, status, method, suffix)
                response = io.BytesIO(b'{"data":{"id":92}}')
                response.status = 201
                with patch("urllib.request.urlopen", return_value=response) as opening:
                    self.registered["before", operation](transaction)
                request = opening.call_args.args[0]
                self.assertEqual(request.data, b"attachment")
                self.assertEqual(request.get_header("Content-type"), "application/octet-stream")
                self.assertEqual(request.get_header("X-attachment-filename"), "contract.pdf")
                expected = "/draft/81/attachments" + ("/92" if suffix else "")
                self.assertIn(expected + "?kept=true", transaction["fullPath"])

    def test_refusals_and_non_placeholder_targets_are_unchanged(self):
        for status, target in ((404, "1"), (422, "0"), (201, "2147483647")):
            transaction = self.transaction("uploadDraftAttachment", status, "POST", target=target)
            original = copy.deepcopy(transaction)
            self.registered["before", "uploadDraftAttachment"](transaction)
            self.assertEqual(transaction, original)
        self.assertEqual(self.calls, [])


if __name__ == "__main__":
    unittest.main()
