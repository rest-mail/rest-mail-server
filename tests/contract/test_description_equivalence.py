"""The live/file input proof fails closed and never reuses a green verdict."""

import io
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import description_equivalence as proof


class DescriptionEquivalence(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.saved = self.root / "openapi.json"
        self.body = b'{"openapi":"3.1.0","paths":{"/api/health":{"get":{"operationId":"getHealth"}}}}'
        self.saved.write_bytes(self.body)
        self.report = self.root / "description-equivalence.json"
        self.config = {"endpoint": "http://fixture.test:8080"}
        self.compiled = {
            "transactions": [
                {
                    "name": "getHealth",
                    "request": {"method": "GET"},
                    "response": {"status": "200"},
                }
            ],
            "annotations": [],
        }

    def verify(self, mode="saved", other=None, body=None):
        calls = []

        def compile_document(command, stdout, check, timeout):
            self.assertEqual((check, timeout), (True, 180))
            calls.append(command)
            result = other if len(calls) == 2 and other is not None else self.compiled
            stdout.write(json.dumps(result).encode())

        with (
            patch.object(
                proof.urllib.request,
                "urlopen",
                return_value=io.BytesIO(self.body if body is None else body),
            ) as fetch,
            patch.object(proof.subprocess, "run", side_effect=compile_document),
        ):
            selected = proof.verify(
                self.config, self.saved, self.report, "/pinned/vertrag", mode
            )
        return selected, calls, fetch

    def test_saved_and_live_use_identical_transactions_and_separate_evidence(self):
        url = "http://fixture.test:8080/api/docs/openapi.json"
        for mode, want in (("saved", str(self.saved)), ("live", url)):
            with self.subTest(mode=mode):
                selected, calls, fetch = self.verify(mode)
                self.assertEqual(selected, want)
                self.assertEqual(
                    calls,
                    [
                        ["/pinned/vertrag", "compile", str(self.saved)],
                        ["/pinned/vertrag", "compile", url],
                    ],
                )
                fetch.assert_called_once_with(url, timeout=30)
                evidence = json.loads(self.report.read_text())
                self.assertEqual(evidence["transactions"], 1)
                self.assertEqual(evidence["status"], "passed")
                self.assertIn("API verdicts are separate", evidence["scope"])

    def test_changed_bytes_never_reach_compilation_or_leave_a_verdict(self):
        self.report.write_text("previous success")
        with self.assertRaisesRegex(ValueError, "changed after saving"):
            self.verify(body=self.body + b"\n")
        self.assertFalse(self.report.exists())

    def test_different_requests_schemas_or_annotations_fail(self):
        for changed in (
            {"transactions": [], "annotations": []},
            {**self.compiled, "annotations": [{"type": "error"}]},
            {"transactions": [{"response": {"status": "500"}}], "annotations": []},
        ):
            with self.subTest(changed=changed):
                self.report.write_text("previous success")
                with self.assertRaisesRegex(
                    ValueError, "different transactions or annotations"
                ):
                    self.verify(other=changed)
                self.assertFalse(self.report.exists())

    def test_empty_compilation_is_not_proof(self):
        self.compiled = {"transactions": [], "annotations": []}
        with self.assertRaisesRegex(ValueError, "no transactions"):
            self.verify()
        self.assertFalse(self.report.exists())

    def test_fetch_and_compile_failure_remove_previous_verdict(self):
        for failure in (
            OSError("unreachable"),
            subprocess.TimeoutExpired("compile", 180),
        ):
            self.report.write_text("previous success")
            with (
                patch.object(proof.urllib.request, "urlopen", side_effect=failure),
                self.assertRaises(type(failure)),
            ):
                proof.verify(self.config, self.saved, self.report, "/pinned/vertrag")
            self.assertFalse(self.report.exists())

    def test_compile_process_failure_cannot_publish_evidence(self):
        self.report.write_text("previous success")
        with (
            patch.object(
                proof.urllib.request, "urlopen", return_value=io.BytesIO(self.body)
            ),
            patch.object(
                proof.subprocess,
                "run",
                side_effect=subprocess.CalledProcessError(1, "compile"),
            ),
            self.assertRaises(subprocess.CalledProcessError),
        ):
            proof.verify(self.config, self.saved, self.report, "/pinned/vertrag")
        self.assertFalse(self.report.exists())

    def test_unknown_mode_removes_previous_verdict(self):
        self.report.write_text("previous success")
        with self.assertRaisesRegex(ValueError, "saved or live"):
            proof.verify(
                self.config, self.saved, self.report, "/pinned/vertrag", "oracle"
            )
        self.assertFalse(self.report.exists())


if __name__ == "__main__":
    unittest.main()
