"""Exercise the constellation merge queue CLI without contacting GitHub."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/am-queue"
SERVER = "rest-mail/rest-mail-server"
IMAP = "rest-mail/go-imap"
EMPTY = "rest-mail/conventions"
US = "\x1f"
ROW = US.join(["1", "AWAITING_CHECKS", "2026-10-24T14:01", "830",
               "https://github.com/rest-mail/rest-mail-server/pull/830", "A change"])

FETCH = r'''#!/usr/bin/env python3
import json, os, sys
with open(os.environ["QUEUE_CALLS"], "a") as f:
    f.write(" ".join(sys.argv[1:]) + "\n")
fixtures = json.loads(os.environ["QUEUE_FIXTURES"])
for slug in sys.argv[1:]:
    item = fixtures.get(slug, "")
    if isinstance(item, dict):
        print(item["error"], file=sys.stderr)
        sys.exit(1)
for slug in sys.argv[1:]:
    for row in fixtures.get(slug, "").splitlines():
        print(slug + "\x1f" + row)
'''

GH = r'''#!/usr/bin/env python3
import json, os, subprocess, sys
with open(os.environ["QUEUE_CALLS"], "a") as f:
    f.write(json.dumps(sys.argv[1:]) + "\n")
query = sys.argv[sys.argv.index("--jq") + 1]
result = subprocess.run(["jq", "-r", query], input=os.environ["QUEUE_RESPONSE"],
                        text=True, capture_output=True)
sys.stdout.write(result.stdout)
sys.stderr.write(result.stderr)
sys.exit(result.returncode)
'''


class QueueTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.calls = self.directory / "calls"
        command = self.directory / "fetch"
        command.write_text(FETCH)
        command.chmod(0o755)
        self.environment = dict(
            os.environ, AM_QUEUE_FETCH=str(command), QUEUE_CALLS=str(self.calls),
            XDG_CACHE_HOME=str(self.directory / "cache"), AM_GITHUB_CACHE_TTL="0",
        )

    def run_queue(self, arguments=(), fixtures=None, **environment):
        return subprocess.run(
            [str(SCRIPT), *arguments], cwd=self.directory, text=True,
            capture_output=True, timeout=20,
            env=dict(self.environment, QUEUE_FIXTURES=json.dumps(fixtures or {}),
                     **environment),
        )

    def projects(self):
        return ["--project", "conventions", "--project", "go-imap",
                "--project", "rest-mail-server"]

    def cache(self, slug):
        return self.directory / "cache/am/github" / slug / "merge-queue"

    def test_combines_every_selected_repository_in_one_request(self):
        result = self.run_queue(self.projects(), {SERVER: ROW, IMAP: ROW})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls.read_text().splitlines(),
                         [f"{EMPTY} {IMAP} {SERVER}"])
        for value in ["AWAITING_CHECKS", "#830", "A change",
                      "TOTAL — 2 queued pull requests across 2 project(s)"]:
            self.assertIn(value, result.stdout)

    def test_tsv_preserves_queue_position_and_columns(self):
        second = ROW.replace("1" + US, "2" + US, 1).replace("830", "831")
        result = self.run_queue(["--project", "rest-mail-server", "--tsv"],
                                {SERVER: ROW + "\n" + second})
        self.assertEqual(result.returncode, 0, result.stderr)
        rows = [line.split("\t") for line in result.stdout.splitlines()]
        self.assertEqual(rows[0], ["project", "position", "state", "queued",
                                   "pr", "url", "title"])
        self.assertEqual([row[1] for row in rows[1:]], ["1", "2"])
        self.assertEqual([row[4] for row in rows[1:]], ["830", "831"])

    def test_empty_queue_is_explicit_and_successful(self):
        result = self.run_queue(self.projects())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("no pull request is queued in any of the 3 project(s)", result.stdout)

    def test_failed_batch_falls_back_and_names_only_the_failed_repository(self):
        result = self.run_queue(self.projects(),
                                {SERVER: ROW, IMAP: {"error": "denied"}})
        self.assertEqual(result.returncode, 4)
        self.assertEqual(self.calls.read_text().splitlines(),
                         [f"{EMPTY} {IMAP} {SERVER}", EMPTY, IMAP, SERVER])
        self.assertIn("A change", result.stdout)
        self.assertIn("FETCH FAILED  go-imap", result.stderr)
        self.assertNotIn("FETCH FAILED  rest-mail-server", result.stderr)
        self.assertIn("incomplete", result.stderr)
        self.assertFalse(self.cache(IMAP).exists())

    def test_rate_limits_stop_before_fallback_and_print_no_table(self):
        for message in ["API rate limit exceeded (HTTP 403)",
                        "You have exceeded a secondary rate limit (HTTP 403)"]:
            with self.subTest(message=message):
                self.calls.write_text("")
                result = self.run_queue(self.projects(),
                                        {IMAP: {"error": message}, SERVER: ROW})
                self.assertEqual(result.returncode, 5)
                self.assertEqual(result.stdout, "")
                self.assertEqual(len(self.calls.read_text().splitlines()), 1)

    def test_default_cache_ttl_is_300_seconds_and_only_stale_answers_are_fetched(self):
        self.environment.pop("AM_GITHUB_CACHE_TTL")
        first = self.run_queue(self.projects(), {SERVER: ROW})
        self.assertEqual(first.returncode, 0, first.stderr)
        for slug in [EMPTY, IMAP, SERVER]:
            self.assertTrue(self.cache(slug).exists())
        self.calls.write_text("")
        fresh = time.time() - 290
        os.utime(self.cache(SERVER), (fresh, fresh))
        cached = self.run_queue(self.projects())
        self.assertEqual(cached.returncode, 0, cached.stderr)
        self.assertIn("A change", cached.stdout)
        self.assertEqual(self.calls.read_text(), "")
        stale = time.time() - 301
        for slug in [EMPTY, IMAP]:
            os.utime(self.cache(slug), (stale, stale))
        result = self.run_queue(self.projects())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls.read_text().splitlines(), [f"{EMPTY} {IMAP}"])

    def test_no_cache_and_zero_ttl_refresh_previously_cached_answers(self):
        self.environment.pop("AM_GITHUB_CACHE_TTL")
        self.run_queue(self.projects(), {SERVER: ROW})
        for arguments, environment in [(["--no-cache"], {}),
                                       ([], {"AM_GITHUB_CACHE_TTL": "0"})]:
            with self.subTest(arguments=arguments):
                self.calls.write_text("")
                result = self.run_queue(self.projects() + arguments, **environment)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.calls.read_text().splitlines(),
                                 [f"{EMPTY} {IMAP} {SERVER}"])
                self.assertEqual(self.cache(SERVER).read_text(), "")

    def test_failed_answer_is_not_cached_and_next_run_retries(self):
        self.environment.pop("AM_GITHUB_CACHE_TTL")
        args = ["--project", "go-imap"]
        result = self.run_queue(args, {IMAP: {"error": "unavailable"}})
        self.assertEqual(result.returncode, 4)
        self.assertFalse(self.cache(IMAP).exists())
        self.calls.write_text("")
        result = self.run_queue(args)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls.read_text().splitlines(), [IMAP])

    def test_invalid_arguments_fail_before_fetching(self):
        for args, environment in [(["--project", "unknown"], {}),
                                   (["--project"], {}), (["--sideways"], {}),
                                   ([], {"AM_GITHUB_CACHE_TTL": "soon"})]:
            with self.subTest(arguments=args):
                result = self.run_queue(args, **environment)
                self.assertEqual(result.returncode, 2)
                self.assertFalse(self.calls.exists())

    def test_real_query_uses_default_branch_and_maps_graphql_aliases(self):
        command = self.directory / "gh"
        command.write_text(GH)
        command.chmod(0o755)
        self.environment.pop("AM_QUEUE_FETCH")
        response = {"data": {
            "r0": {"mergeQueue": None},
            "r1": {"mergeQueue": {"entries": {"nodes": [{
                "position": 1, "state": "QUEUED", "enqueuedAt": "2026-10-24T14:01:09Z",
                "pullRequest": {"number": 830, "title": "", "url": "u/830"},
            }]}}},
        }}
        result = self.run_queue(["--project", "conventions", "--project", "go-imap", "--tsv"],
                                PATH=str(self.directory) + os.pathsep + os.environ["PATH"],
                                QUEUE_RESPONSE=json.dumps(response))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("go-imap\t1\tQUEUED\t2026-10-24T14:01\t830\tu/830\t(no title)",
                      result.stdout)
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        self.assertEqual(len(calls), 1)
        query = next(arg for arg in calls[0] if arg.startswith("query="))
        self.assertIn('r0: repository(owner: "rest-mail", name: "conventions")', query)
        self.assertIn('r1: repository(owner: "rest-mail", name: "go-imap")', query)
        self.assertIn("mergeQueue {", query)
        self.assertNotIn("mergeQueue(branch", query)

    def test_ci_discovers_queue_tests_and_chore_forwards_arguments(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        self.assertIn("python3 -m unittest discover -s scripts/tests -p 'test_*.py'", workflow)
        chores = (ROOT / "chores.yml").read_text()
        self.assertIn("  queue:list:\n", chores)
        self.assertIn("scripts/am-queue {{.CLI_ARGS}}", chores)


if __name__ == "__main__":
    unittest.main()
