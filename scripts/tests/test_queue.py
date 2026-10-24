"""Exercise the copied merge-queue CLI without contacting GitHub (#819)."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/am-queue"
US = "\x1f"
SERVER = "rest-mail/rest-mail-server"
IMAP = "rest-mail/go-imap"
WEBSITE = "rest-mail/website"
THREE = ["--project", "rest-mail-server", "--project", "go-imap",
         "--project", "website"]

# Called with every slug one request asks about; each call is one line of
# QUEUE_CALLS. A slug whose fixture is {"error": ...} fails the whole call
# with that message, so a combined request fails as a whole and only that
# slug's own request fails after it. Otherwise each slug's rows are printed,
# slug first.
FETCH = r'''#!/usr/bin/env python3
import json, os, sys
with open(os.environ["QUEUE_CALLS"], "a") as f:
    f.write(" ".join(sys.argv[1:]) + "\n")
fixtures = json.loads(os.environ["QUEUE_FIXTURES"])
for slug in sys.argv[1:]:
    item = fixtures.get(slug, [])
    if isinstance(item, dict):
        print(item["error"], file=sys.stderr)
        sys.exit(1)
for slug in sys.argv[1:]:
    for row in fixtures.get(slug, []):
        print("\x1f".join([slug] + row))
'''


def row(position, state, queued, number, title):
    return [str(position), state, queued, str(number),
            f"https://github.com/o/r/pull/{number}", title]


QUEUED = {
    SERVER: [row(1, "AWAITING_CHECKS", "2026-10-24T14:01", 818, "a refusal"),
             row(2, "QUEUED", "2026-10-24T14:05", 819, "the queue")],
    IMAP: [row(1, "MERGEABLE", "2026-10-24T13:46", 403, "an imap fix")],
}


class QueueTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.calls = self.directory / "calls"
        self.calls.write_text("")
        command = self.directory / "fetch"
        command.write_text(FETCH)
        command.chmod(0o755)
        self.cache = self.directory / "cache"
        self.environment = dict(
            os.environ, AM_QUEUE_FETCH=str(command), QUEUE_CALLS=str(self.calls),
            XDG_CACHE_HOME=str(self.cache), AM_GITHUB_CACHE_TTL="0")

    def run_queue(self, arguments, fixtures=QUEUED, **environment):
        return subprocess.run(
            [str(SCRIPT), *arguments], cwd=self.directory, text=True,
            capture_output=True, timeout=20,
            env=dict(self.environment, QUEUE_FIXTURES=json.dumps(fixtures),
                     **environment))

    def requests(self):
        return self.calls.read_text().splitlines()

    def numbers(self, tsv):
        return [line.split("\t")[4] for line in tsv.splitlines()[1:]]

    def entry(self, name):
        return self.cache / "am/github/rest-mail" / name / "merge-queue"

    def test_one_request_lists_every_queued_pull_request(self):
        result = self.run_queue(THREE)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.requests(), [f"{IMAP} {SERVER} {WEBSITE}"])
        self.assertIn("AWAITING_CHECKS", result.stdout)
        self.assertIn("TOTAL — 3 queued pull requests across 2 project(s)",
                      result.stdout)

    def test_tsv_is_one_row_per_pull_request_in_constellation_then_queue_order(self):
        result = self.run_queue([*THREE, "--tsv"])
        self.assertEqual(result.stdout.splitlines()[0],
                         "project\tposition\tstate\tqueued\tpr\turl\ttitle")
        self.assertEqual(self.numbers(result.stdout), ["403", "818", "819"])

    def test_one_pull_request_is_counted_in_the_singular(self):
        result = self.run_queue(["--project", "go-imap"])
        self.assertIn("TOTAL — 1 queued pull request across 1 project(s)",
                      result.stdout)

    def test_an_empty_queue_says_so(self):
        result = self.run_queue(["--project", "website"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("no pull request is queued in any of the 1 project(s)",
                      result.stdout)

    def test_a_name_outside_the_constellation_or_an_unknown_flag_is_refused(self):
        self.assertEqual(self.run_queue(["--project", "nope"]).returncode, 2)
        self.assertEqual(self.run_queue(["--sideways"]).returncode, 2)

    def test_a_failed_combined_request_asks_each_project_alone(self):
        fixtures = dict(QUEUED, **{IMAP: {"error": "HTTP 502"}})
        result = self.run_queue(THREE, fixtures)
        self.assertEqual(result.returncode, 4, result.stderr)
        self.assertIn("FETCH FAILED  go-imap", result.stderr)
        self.assertIn("a refusal", result.stdout)
        self.assertEqual(len(self.requests()), 4)

    def test_a_rate_limit_stops_after_one_request_with_no_table(self):
        for message in ["API rate limit exceeded (HTTP 403)",
                        "You have exceeded a secondary rate limit (HTTP 403)"]:
            with self.subTest(message=message):
                self.calls.write_text("")
                fixtures = dict(QUEUED, **{IMAP: {"error": message}})
                result = self.run_queue(THREE, fixtures)
                self.assertEqual(result.returncode, 5, result.stderr)
                self.assertEqual(len(self.requests()), 1)
                self.assertNotIn("PROJECT", result.stdout)

    def test_the_filter_reads_the_default_branch_queue_of_each_alias(self):
        if shutil.which("jq") is None:
            self.fail("jq is needed to check the filter; install jq")
        source = SCRIPT.read_text()
        self.assertIn("mergeQueue {", source,
                      "the queue must be the default branch's, not a named one")
        response = {"data": {
            "r0": {"mergeQueue": {"entries": {"nodes": [{
                "position": 1, "state": "AWAITING_CHECKS",
                "enqueuedAt": "2026-10-24T14:01:09Z",
                "pullRequest": {"number": 818, "title": "", "url": "u/818"}}]}}},
            "r1": {"mergeQueue": None}}}
        program = subprocess.run([str(SCRIPT), "--print-jq"], text=True,
                                 capture_output=True, check=True).stdout
        rows = subprocess.run(["jq", "-r", program], input=json.dumps(response),
                              text=True, capture_output=True, check=True).stdout
        self.assertEqual(rows.strip().split(US),
                         ["r0", "1", "AWAITING_CHECKS", "2026-10-24T14:01",
                          "818", "u/818", "(no title)"])

    def test_answers_are_kept_for_300_seconds_and_only_stale_ones_asked(self):
        del self.environment["AM_GITHUB_CACHE_TTL"]
        self.run_queue(THREE)
        self.assertEqual(len(self.requests()), 1)
        for name in ["rest-mail-server", "go-imap", "website"]:
            self.assertTrue(self.entry(name).exists(), name)
        self.calls.write_text("")
        result = self.run_queue([*THREE, "--tsv"])
        self.assertEqual(self.requests(), [])
        self.assertEqual(self.numbers(result.stdout), ["403", "818", "819"])

        def age(name, seconds):
            then = time.time() - seconds
            os.utime(self.entry(name), (then, then))

        age("go-imap", 299)
        self.run_queue(THREE)
        self.assertEqual(self.requests(), [])
        age("go-imap", 301)
        age("website", 301)
        self.run_queue(THREE)
        self.assertEqual(self.requests(), [f"{IMAP} {WEBSITE}"])
        self.calls.write_text("")
        self.run_queue([*THREE, "--no-cache"])
        self.assertEqual(len(self.requests()), 1)
        self.assertEqual(
            self.run_queue(THREE, AM_GITHUB_CACHE_TTL="soon").returncode, 2)

    def test_a_failed_fetch_keeps_nothing(self):
        del self.environment["AM_GITHUB_CACHE_TTL"]
        result = self.run_queue(["--project", "go-imap"],
                                {IMAP: {"error": "HTTP 502"}})
        self.assertEqual(result.returncode, 4)
        self.assertFalse(self.entry("go-imap").exists())
        self.calls.write_text("")
        self.run_queue(["--project", "go-imap"])
        self.assertEqual(len(self.requests()), 1)


if __name__ == "__main__":
    unittest.main()
