"""The Vertrag pin is read and rewritten as one: version and checksum together (#628)."""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import vertrag_pin  # noqa: E402

WORKFLOW = """      - name: Install vertrag (pinned + checksum-verified)
        env:
          VERTRAG_VERSION: 0.29.0
          # sha256 of the archive
          VERTRAG_SHA256: 6ab32926379d4c4b205b48fb4f0d0d31fd3c1a7d79af7effacce63c6c17df3c6
        run: |
          echo "${VERTRAG_SHA256}"
"""

CHECKSUMS = """0f56306282a5516d7d59c4526f2bb890c6e88f02a3c6ffc981804a90efec49f0  vertrag-0.30.0-linux-arm64.tar.gz
1111111111111111111111111111111111111111111111111111111111111111  vertrag-0.30.0-linux-x86_64.tar.gz
"""


class VertragPin(unittest.TestCase):
    def test_reads_the_pin(self):
        self.assertEqual(
            vertrag_pin.pinned(WORKFLOW),
            ("0.29.0", "6ab32926379d4c4b205b48fb4f0d0d31fd3c1a7d79af7effacce63c6c17df3c6"),
        )

    def test_rewrites_version_and_checksum_together(self):
        sha = vertrag_pin.checksum_for(CHECKSUMS, vertrag_pin.asset("0.30.0"))
        bumped = vertrag_pin.with_pin(WORKFLOW, "0.30.0", sha)
        self.assertEqual(vertrag_pin.pinned(bumped), ("0.30.0", "1" * 64))
        # Nothing else in the workflow changes.
        self.assertEqual(
            [l for l in bumped.splitlines() if "VERTRAG_VERSION:" not in l and "VERTRAG_SHA256:" not in l],
            [l for l in WORKFLOW.splitlines() if "VERTRAG_VERSION:" not in l and "VERTRAG_SHA256:" not in l],
        )

    def test_the_real_workflow_carries_a_pin(self):
        with open(vertrag_pin.WORKFLOW) as f:
            version, sha = vertrag_pin.pinned(f.read())
        self.assertRegex(version, r"^\d+\.\d+\.\d+$")
        self.assertRegex(sha, r"^[0-9a-f]{64}$")

    def test_an_archive_the_release_does_not_list_is_refused(self):
        with self.assertRaises(ValueError):
            vertrag_pin.checksum_for(CHECKSUMS, vertrag_pin.asset("0.30.0", "darwin", "arm64"))

    def test_a_workflow_without_a_pin_is_refused(self):
        with self.assertRaises(ValueError):
            vertrag_pin.with_pin("jobs: {}\n", "0.30.0", "1" * 64)


if __name__ == "__main__":
    unittest.main()
