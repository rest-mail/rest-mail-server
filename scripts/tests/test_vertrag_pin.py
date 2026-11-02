"""The Vertrag pin is read and rewritten as one: version and checksum together (#628)."""

import contextlib
import io
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import vertrag_pin  # noqa: E402

PIN = """# Managed by scripts/vertrag_pin.py.
VERTRAG_VERSION: 0.29.0
VERTRAG_SHA256: 6ab32926379d4c4b205b48fb4f0d0d31fd3c1a7d79af7effacce63c6c17df3c6
"""

CHECKSUMS = """0f56306282a5516d7d59c4526f2bb890c6e88f02a3c6ffc981804a90efec49f0  vertrag-0.30.0-linux-arm64.tar.gz
1111111111111111111111111111111111111111111111111111111111111111  vertrag-0.30.0-linux-x86_64.tar.gz
"""


class VertragPin(unittest.TestCase):
    def test_reads_the_pin(self):
        self.assertEqual(
            vertrag_pin.pinned(PIN),
            ("0.29.0", "6ab32926379d4c4b205b48fb4f0d0d31fd3c1a7d79af7effacce63c6c17df3c6"),
        )

    def test_rewrites_version_and_checksum_together(self):
        sha = vertrag_pin.checksum_for(CHECKSUMS, vertrag_pin.asset("0.30.0"))
        bumped = vertrag_pin.with_pin(PIN, "0.30.0", sha)
        self.assertEqual(vertrag_pin.pinned(bumped), ("0.30.0", "1" * 64))
        self.assertEqual(
            [line for line in bumped.splitlines() if not line.startswith("VERTRAG_")],
            [line for line in PIN.splitlines() if not line.startswith("VERTRAG_")],
        )

    def test_the_real_pin_is_outside_workflows(self):
        relative = Path(vertrag_pin.PIN_FILE).relative_to(vertrag_pin.ROOT)
        self.assertEqual(relative, Path("scripts/vertrag-pin.yml"))
        with open(vertrag_pin.PIN_FILE) as f:
            version, sha = vertrag_pin.pinned(f.read())
        self.assertRegex(version, r"^\d+\.\d+\.\d+$")
        self.assertRegex(sha, r"^[0-9a-f]{64}$")
        for workflow in (Path(vertrag_pin.ROOT) / ".github/workflows").glob("*.yml"):
            with self.subTest(workflow=workflow.name):
                source = workflow.read_text()
                self.assertNotIn("VERTRAG_VERSION:", source)
                self.assertNotIn("VERTRAG_SHA256:", source)

    def test_bump_changes_only_the_tracked_pin_and_commit_am_picks_it_up(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pin = root / "scripts/vertrag-pin.yml"
            pin.parent.mkdir()
            pin.write_text(PIN)
            workflow = root / ".github/workflows/e2e-suite.yml"
            workflow.parent.mkdir(parents=True)
            workflow.write_text(
                (Path(vertrag_pin.ROOT) / ".github/workflows/e2e-suite.yml").read_text()
            )

            def git(*args):
                result = subprocess.run(
                    [
                        "git", "-c", "user.name=Pin test", "-c", "user.email=pin@example.test",
                        "-c", "commit.gpgsign=false", *args,
                    ],
                    cwd=root,
                    text=True,
                    capture_output=True,
                    check=True,
                )
                return result.stdout

            git("init", "-q")
            git("add", ".")
            git("commit", "-qm", "Record the pin fixture")
            with (
                mock.patch.object(vertrag_pin, "PIN_FILE", str(pin)),
                mock.patch.object(vertrag_pin, "latest_version", return_value="0.30.0"),
                mock.patch.object(vertrag_pin, "release_checksums", return_value=CHECKSUMS),
                contextlib.redirect_stdout(io.StringIO()),
            ):
                self.assertEqual(vertrag_pin.main(["vertrag_pin.py", "bump"]), 0)
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    self.assertEqual(vertrag_pin.main(["vertrag_pin.py", "version"]), 0)
                self.assertEqual(output.getvalue(), "0.30.0\n")
            self.assertEqual(vertrag_pin.pinned(pin.read_text()), ("0.30.0", "1" * 64))
            self.assertEqual(
                git("diff", "--name-only").splitlines(), ["scripts/vertrag-pin.yml"]
            )
            staged = git("commit", "--dry-run", "--porcelain", "-am", "Bump Vertrag")
            self.assertIn("scripts/vertrag-pin.yml", staged)
            self.assertNotIn(".github/workflows/", staged)

    def test_bump_of_the_current_version_leaves_the_pin_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            pin = Path(directory) / "vertrag-pin.yml"
            pin.write_text(PIN)
            with (
                mock.patch.object(vertrag_pin, "PIN_FILE", str(pin)),
                mock.patch.object(vertrag_pin, "latest_version", return_value="0.29.0"),
                mock.patch.object(vertrag_pin, "release_checksums") as checksums,
                contextlib.redirect_stdout(io.StringIO()),
            ):
                self.assertEqual(vertrag_pin.bump(), 0)
            checksums.assert_not_called()
            self.assertEqual(pin.read_text(), PIN)

    def test_an_archive_the_release_does_not_list_is_refused(self):
        with self.assertRaises(ValueError):
            vertrag_pin.checksum_for(CHECKSUMS, vertrag_pin.asset("0.30.0", "darwin", "arm64"))

    def test_a_file_without_a_pin_is_refused(self):
        with self.assertRaises(ValueError):
            vertrag_pin.with_pin("jobs: {}\n", "0.30.0", "1" * 64)


if __name__ == "__main__":
    unittest.main()
