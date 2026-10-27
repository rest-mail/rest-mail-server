"""Offline controls for the pinned contract tool cache and atomic install."""

import contextlib
import hashlib
import io
import json
import os
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import vertrag_pin


class VertragInstall(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.cache = self.root / "cache tools"
        self.workflow = self.root / "workflow.yml"
        self.binary = b"#!/bin/sh\nexit 0\n"
        self.archive = self.make_archive()
        self.sha = hashlib.sha256(self.archive).hexdigest()
        self.write_pin(self.sha)
        self.system, self.machine = "linux", "x86_64"
        self.requests = []
        for patch in [
            mock.patch.object(vertrag_pin, "WORKFLOW", str(self.workflow)),
            mock.patch.object(
                vertrag_pin,
                "this_machine",
                side_effect=lambda: (self.system, self.machine),
            ),
            mock.patch.object(vertrag_pin, "fetch", side_effect=self.fetch),
        ]:
            patch.start()
            self.addCleanup(patch.stop)

    def write_pin(self, sha):
        self.workflow.write_text(f"VERTRAG_VERSION: 0.29.1\nVERTRAG_SHA256: {sha}\n")

    def make_archive(self, link=False):
        output = io.BytesIO()
        with tarfile.open(fileobj=output, mode="w:gz") as archive:
            member = tarfile.TarInfo("vertrag")
            if link:
                member.type = tarfile.SYMTYPE
                member.linkname = "other"
                archive.addfile(member)
            else:
                member.size = len(self.binary)
                member.mode = 0o755
                archive.addfile(member, io.BytesIO(self.binary))
        return output.getvalue()

    def fetch(self, url):
        self.requests.append(url)
        name = vertrag_pin.asset("0.29.1", self.system, self.machine)
        if url.endswith("/checksums.txt"):
            return f"{self.sha}  {name}\n".encode()
        if url.endswith("/" + name):
            return self.archive
        self.fail("unexpected fixture request")

    def install(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(vertrag_pin.install(str(self.cache)), 0)
        return self.cache / "vertrag"

    def test_linux_install_refuses_a_release_checksum_different_from_ci_pin(self):
        self.write_pin("f" * 64)
        with self.assertRaisesRegex(ValueError, "CI pin"):
            self.install()
        self.assertFalse((self.cache / "vertrag").exists())
        self.assertEqual(len(self.requests), 1)

    def test_verified_cache_is_reused_without_network_or_path_tool(self):
        target = self.install()
        self.requests.clear()
        with mock.patch.dict(os.environ, {"PATH": str(self.root / "wrong tool")}):
            self.assertEqual(vertrag_pin.ensure(str(self.cache)), str(target.resolve()))
        self.assertEqual(self.requests, [])
        self.assertEqual(target.read_bytes(), self.binary)

    def test_corrupt_binary_receipt_or_platform_forces_a_verified_reinstall(self):
        for damage in ["binary", "receipt", "platform", "executable", "missing"]:
            with self.subTest(damage=damage):
                target = self.install()
                receipt = self.cache / "vertrag-pin.json"
                if damage == "binary":
                    target.write_bytes(b"corrupted executable")
                elif damage == "receipt":
                    receipt.write_text("not a receipt")
                elif damage == "platform":
                    value = json.loads(receipt.read_text())
                    value["system"] = "different-system"
                    receipt.write_text(json.dumps(value))
                elif damage == "executable":
                    target.chmod(0o644)
                else:
                    target.rename(self.root / "previous binary")
                self.requests.clear()
                with contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(
                        vertrag_pin.ensure(str(self.cache)), str(target.resolve())
                    )
                self.assertEqual(len(self.requests), 2)
                self.assertEqual(target.read_bytes(), self.binary)
                self.assertTrue(os.access(target, os.X_OK))

    def test_changed_ci_pin_invalidates_the_cache_even_with_the_same_version(self):
        self.install()
        self.write_pin("f" * 64)
        self.requests.clear()
        with self.assertRaisesRegex(ValueError, "CI pin"):
            vertrag_pin.ensure(str(self.cache))
        self.assertEqual(len(self.requests), 1)

    def test_atomic_replace_failure_keeps_the_previous_binary(self):
        self.cache.mkdir()
        target = self.cache / "vertrag"
        target.write_bytes(b"previous verified binary")
        with (
            mock.patch.object(
                vertrag_pin.os,
                "replace",
                side_effect=OSError("fixture replace failure"),
            ),
            self.assertRaisesRegex(OSError, "fixture replace failure"),
        ):
            self.install()
        self.assertEqual(target.read_bytes(), b"previous verified binary")

    def test_link_member_is_refused_before_install(self):
        self.archive = self.make_archive(link=True)
        self.sha = hashlib.sha256(self.archive).hexdigest()
        self.write_pin(self.sha)
        with self.assertRaisesRegex(ValueError, "regular file"):
            self.install()
        self.assertFalse((self.cache / "vertrag").exists())

    def test_other_platform_uses_its_pinned_release_archive_checksum(self):
        self.system, self.machine = "darwin", "arm64"
        self.write_pin("f" * 64)  # The workflow digest belongs to Linux x86_64.
        target = self.install()
        self.assertEqual(target.read_bytes(), self.binary)
        self.assertIn("darwin-arm64", self.requests[-1])

    def test_verified_but_unrunnable_tool_fails_without_exposing_its_output(self):
        self.binary = b"#!/bin/sh\necho private-fixture-diagnostic >&2\nexit 23\n"
        self.archive = self.make_archive()
        self.sha = hashlib.sha256(self.archive).hexdigest()
        self.write_pin(self.sha)
        with (
            contextlib.redirect_stderr(io.StringIO()),
            self.assertRaisesRegex(ValueError, "cannot execute") as raised,
        ):
            vertrag_pin.ensure(str(self.cache))
        self.assertNotIn("private-fixture-diagnostic", str(raised.exception))

    def test_ensure_cli_stdout_is_only_the_selected_absolute_path(self):
        output, diagnostics = io.StringIO(), io.StringIO()
        with (
            contextlib.redirect_stdout(output),
            contextlib.redirect_stderr(diagnostics),
        ):
            status = vertrag_pin.main(["vertrag_pin.py", "ensure", str(self.cache)])
        self.assertEqual(status, 0)
        self.assertEqual(
            output.getvalue().strip(), str((self.cache / "vertrag").resolve())
        )
        self.assertIn("checksum verified", diagnostics.getvalue())


if __name__ == "__main__":
    unittest.main()
