"""Exercise the copied overview CLI without contacting GitHub."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/am-overview"
US = "\x1f"
SERVER = "rest-mail/rest-mail-server"
ROW = US.join(["7", "2020-01-01T00:00:00Z", "-", "A useful issue"])

FETCH = r'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
kind = Path(sys.argv[0]).name
slug = sys.argv[1]
with open(os.environ["OVERVIEW_CALLS"], "a") as f:
    f.write(kind + " " + slug + "\n")
repo = json.loads(os.environ["OVERVIEW_FIXTURES"]).get(slug, {})
item = repo.get(kind, "0 0" if kind == "counts" else "")
if isinstance(item, dict):
    print(item["error"], file=sys.stderr)
    sys.exit(1)
if item:
    print(item)
'''


class OverviewTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.calls = self.directory / "calls"
        self.environment = dict(os.environ, OVERVIEW_CALLS=str(self.calls))
        for variable, kind in [
            ("AM_OVERVIEW_FETCH", "issues"),
            ("AM_OVERVIEW_FETCH_PRS", "links"),
            ("AM_OVERVIEW_FETCH_PR_COUNTS", "counts"),
        ]:
            command = self.directory / kind
            command.write_text(FETCH)
            command.chmod(0o755)
            self.environment[variable] = str(command)

    def run_overview(self, arguments, fixtures):
        return subprocess.run(
            [str(SCRIPT), *arguments], cwd=self.directory, text=True,
            capture_output=True, timeout=20,
            env=dict(self.environment, OVERVIEW_FIXTURES=json.dumps(fixtures)),
        )

    def row(self, output, name):
        self.assertTrue(any(line.split() and line.split()[0] == name
                            for line in output.splitlines()), output)
        return next(line.split() for line in output.splitlines()
                    if line.split() and line.split()[0] == name)

    def test_summary_keeps_pr_counts_when_there_are_no_issues(self):
        result = self.run_overview(["--summary", "--project", "rest-mail-server"],
                                   {SERVER: {"counts": "3 12"}})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.row(result.stdout, "rest-mail-server"),
                         ["rest-mail-server", "0", "0d", "0", "3", "12"])

    def test_summary_includes_empty_repositories_in_totals(self):
        result = self.run_overview(
            ["--summary", "--project", "conventions", "--project", "rest-mail-server"],
            {"rest-mail/conventions": {"counts": "2 5"},
             SERVER: {"issues": ROW, "counts": "3 7"}},
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.row(result.stdout, "conventions")[-2:], ["2", "5"])
        self.assertEqual(self.row(result.stdout, "TOTAL"), ["TOTAL", "1", "5", "12"])

    def test_tsv_has_stable_columns_for_unlabelled_issues(self):
        result = self.run_overview(["--tsv", "--project", "rest-mail-server"],
                                   {SERVER: {"issues": ROW}})
        self.assertEqual(result.returncode, 0, result.stderr)
        fields = result.stdout.strip().split("\t")
        self.assertEqual(len(fields), 5)
        self.assertEqual(fields[:2], ["rest-mail-server", "7"])
        self.assertEqual(fields[3:], ["-", "A useful issue"])

    def test_list_names_empty_projects_and_associated_prs(self):
        first = "https://github.com/rest-mail/rest-mail-server/pull/8"
        second = "https://github.com/rest-mail/rest-mail-server/pull/9"
        result = self.run_overview(
            ["--list", "--project", "conventions", "--project", "rest-mail-server"],
            {SERVER: {"issues": ROW, "links": US.join(["7", first + " " + second])}},
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        for value in ["conventions — 0 open", "(no open issues)",
                      "rest-mail-server — 1 open", first, second, "A useful issue"]:
            self.assertIn(value, result.stdout)

    def test_failed_issue_fetch_is_never_an_empty_queue(self):
        result = self.run_overview(["--summary", "--project", "rest-mail-server"],
                                   {SERVER: {"issues": {"error": "unavailable"}}})
        self.assertEqual(result.returncode, 4)
        self.assertIn("FETCH FAILED", result.stdout)
        self.assertIn("incomplete", result.stderr)

    def test_failed_pr_counts_are_unknown_and_nonzero(self):
        result = self.run_overview(["--summary", "--project", "rest-mail-server"],
                                   {SERVER: {"issues": ROW, "counts": "invalid"}})
        self.assertEqual(result.returncode, 4)
        self.assertEqual(self.row(result.stdout, "rest-mail-server")[-2:], ["?", "?"])
        self.assertEqual(self.row(result.stdout, "TOTAL")[-2:], ["0+?", "0+?"])

    def test_failed_pr_links_are_reported_in_list_mode(self):
        result = self.run_overview(["--list", "--project", "rest-mail-server"],
                                   {SERVER: {"issues": ROW, "links": {"error": "denied"}}})
        self.assertEqual(result.returncode, 4)
        self.assertIn("FETCH FAILED", result.stdout)

    def test_rate_limit_stops_before_the_next_repository(self):
        result = self.run_overview(
            ["--summary", "--project", "conventions", "--project", "deploy"],
            {"rest-mail/conventions": {"issues": {"error": "API rate limit exceeded"}}},
        )
        self.assertEqual(result.returncode, 5)
        self.assertEqual(result.stdout, "")
        self.assertEqual(self.calls.read_text().splitlines(), ["issues rest-mail/conventions"])

    def test_repository_snapshot_matches_the_core_manifest(self):
        result = subprocess.run(
            ["bash", "-c", '. "$1"; printf "%s\\n" "${CONSTELLATION_REPOSITORIES[@]}"',
             "overview-test", str(ROOT / "scripts/constellation-repositories.sh")],
            capture_output=True, text=True, check=True,
        )
        entries = [line.split(":", 1) for line in result.stdout.splitlines()]
        manifest = json.loads((ROOT / "constellation.json").read_text())
        self.assertEqual([slug for _, slug in entries],
                         sorted(repo["repository"] for repo in manifest["repositories"]))
        self.assertEqual(len({name for name, _ in entries}), len(entries))

    def jq_filter(self, option, value):
        query = subprocess.run([str(SCRIPT), option], capture_output=True,
                               text=True, check=True).stdout
        return subprocess.run(["jq", "-r", query], input=json.dumps(value),
                              capture_output=True, text=True, check=True).stdout.splitlines()

    def test_issue_filter_excludes_pull_requests_and_keeps_empty_fields(self):
        issue = {"number": 7, "created_at": "2020-01-01T00:00:00Z",
                 "labels": [], "title": "A useful issue"}
        self.assertEqual(self.jq_filter("--print-jq", [issue, {**issue, "pull_request": {}}]), [ROW])

    def test_pr_filter_combines_mentions_and_closing_links_without_duplicates(self):
        first = {"number": 2, "url": "https://github.com/rest-mail/rest-mail-server/pull/2"}
        last = {"number": 9, "url": "https://github.com/rest-mail/rest-mail-server/pull/9"}
        node = {"number": 7, "closedByPullRequestsReferences": {"nodes": [last]},
                "timelineItems": {"nodes": [
                    {"source": {"__typename": "PullRequest", **last}},
                    {"source": {"__typename": "PullRequest", **first}},
                    {"source": {"__typename": "Issue", "number": 99}},
                ]}}
        value = {"data": {"repository": {"issues": {"nodes": [node]}}}}
        self.assertEqual(self.jq_filter("--print-pr-jq", value),
                         [US.join(["7", first["url"] + " " + last["url"]])])


if __name__ == "__main__":
    unittest.main()
