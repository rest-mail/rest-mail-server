"""The daily contract tier's issues, with gh replaced by a recording stand-in (#626)."""

import json
import os
import re
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/contract_tier_issues.py"
WORKFLOWS = ROOT / ".github/workflows"
sys.path.insert(0, str(ROOT / "scripts"))

import contract_tier_issues as tier

SEARCH_AFTER = 'fuzz: /api/v1/accounts/{id}/search > Search messages > 200 > application/json · query parameter "after" · valid'
SEARCH_BEFORE = 'fuzz: /api/v1/accounts/{id}/search > Search messages > 200 > application/json · query parameter "before" · valid'
AUDIT_SINCE = 'fuzz: /api/v1/admin/audit > Read the audit log, newest first > 200 > application/json · query parameter "since" · valid'
NEVER_DRAWN = "fuzz: /api/v1/old > Gone > 200 > application/json · body · valid"
SKIPPED = 'fuzz: /api/mailboxes > Check > 200 > application/json · query parameter "address" · valid'

STAND_IN = """#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
record = {"args": args}
if "--body-file" in args:
    record["stdin"] = sys.stdin.read()
with open(os.environ["GH_LOG"], "a") as log:
    log.write(json.dumps(record) + "\\n")
fail = os.environ.get("GH_FAIL")
if fail and " ".join(args[:2]) == fail:
    sys.stderr.write("HTTP 403: refused\\n")
    sys.exit(1)
if args[:2] == ["issue", "list"]:
    with open(os.environ["GH_ISSUES"]) as issues:
        sys.stdout.write(issues.read())
elif args[:2] == ["label", "list"]:
    sys.stdout.write(os.environ.get("GH_LABELS", "[]"))
"""


def failing(
    name,
    uri="/x",
    errors=("the server returned 422 for a body its own schema permits"),
    method="GET",
):
    errors = [errors] if isinstance(errors, str) else list(errors)
    return {
        "name": name,
        "status": "fail",
        "method": method,
        "uri": uri,
        "errors": errors,
    }


def passing(name):
    return {"name": name, "status": "pass", "method": "GET", "uri": "/x"}


def marker(tier_name, name):
    return f"<!-- contract-tier-issue:{tier_name}:{tier.key_of(name)} -->"


class Harness(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.dir = Path(temp.name)
        self.reports = self.dir / "reports"
        self.reports.mkdir()
        self.log = self.dir / "gh.log"
        self.issues_file = self.dir / "issues.json"
        self.issues_file.write_text("[]")
        bin_dir = self.dir / "bin"
        bin_dir.mkdir()
        stand_in = bin_dir / "gh"
        stand_in.write_text(STAND_IN)
        stand_in.chmod(stand_in.stat().st_mode | stat.S_IEXEC)
        self.env = {
            "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
            "GH_LOG": str(self.log),
            "GH_ISSUES": str(self.issues_file),
            "GITHUB_EVENT_NAME": "schedule",
            "GITHUB_REF": "refs/heads/main",
            "GITHUB_REPOSITORY": "rest-mail/rest-mail-server",
            "GITHUB_SHA": "abc123",
            "GITHUB_RUN_ID": "77",
            "GITHUB_SERVER_URL": "https://github.com",
        }

    def fuzz_report(self, results, seed="17760610729703352915"):
        (self.reports / "fuzz.json").write_text(
            json.dumps(
                {
                    "vertrag": {"version": "0.42.0"},
                    "summary": {"total": len(results) or 1},
                    "results": results,
                }
            )
        )
        lines = ["94 link(s) checked, 0 finding(s)"]
        if seed:
            lines.append(f"seed: {seed} (replay with --seed {seed})")
        lines += [
            "pinned in every generated body: -----BEGIN PRIVATE KEY-----",
            "MC4CAQAw",
            "-----END PRIVATE KEY-----",
        ]
        (self.reports / "contract-fuzz.log").write_text("\n".join(lines) + "\n")

    def faults_report(self, rows):
        table = [
            "# Contract faults",
            "",
            "| Fault | Operation | Phase | Verdict |",
            "|---|---|---|---|",
        ]
        table += [
            f"| {fault} | {operation} | {phase} | {verdict} |"
            for fault, operation, phase, verdict in rows
        ]
        (self.reports / "faults.md").write_text("\n".join(table) + "\n")

    def existing(self, *issues):
        rows = []
        for number, tier_name, name, state, reason in issues:
            body = f"{marker(tier_name, name)}\nintro\n\nFirst seen: [run 1](https://example.test), commit `old`\n"
            if name == tier.RUN_KEY:
                body = body.replace(tier.key_of(name), tier.RUN_KEY)
            rows.append(
                {"number": number, "state": state, "stateReason": reason, "body": body}
            )
        self.issues_file.write_text(json.dumps(rows))

    def run_script(self, tier_name="fuzz", result="success", extra=(), env=None):
        env = {**self.env, **(env or {})}
        wrapper = textwrap.dedent(f"""
            import sys
            sys.path.insert(0, {str(ROOT / "scripts")!r})
            import contract_tier_issues as tier
            tier.PAUSE_SECONDS = 0
            sys.exit(tier.main(sys.argv[1:]))
        """)
        return subprocess.run(
            [
                sys.executable,
                "-B",
                "-c",
                wrapper,
                "--tier",
                tier_name,
                "--result",
                result,
                "--reports",
                str(self.reports),
                *extra,
            ],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

    def calls(self):
        if not self.log.exists():
            return []
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def writes(self, verb=None):
        writing = ("create", "edit", "close", "reopen", "comment")
        return [
            call
            for call in self.calls()
            if call["args"][0] == "issue"
            and call["args"][1] in writing
            and verb in (None, call["args"][1])
        ]

    def assert_ok(self, out):
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)


class FuzzFindings(Harness):
    def test_each_finding_opens_one_issue_carrying_its_seed(self):
        self.fuzz_report(
            [
                failing(SEARCH_AFTER, "/api/v1/accounts/1/search?q=Subject&after=a"),
                failing(AUDIT_SINCE, "/api/v1/admin/audit?since=a"),
                passing(SEARCH_BEFORE),
            ]
        )
        self.assert_ok(self.run_script())
        created = self.writes("create")
        self.assertEqual(len(created), 2)
        self.assertEqual(len(self.writes()), 2)
        by_title = {
            call["args"][call["args"].index("--title") + 1]: call for call in created
        }
        title = 'Contract fuzz: GET /api/v1/accounts/{id}/search: query parameter "after" · valid'
        self.assertIn(title, by_title)
        call = by_title[title]
        self.assertEqual(
            call["args"][call["args"].index("--label") + 1], "contract-fuzz"
        )
        body = call["stdin"]
        self.assertIn(marker("fuzz", SEARCH_AFTER), body)
        self.assertIn("chore contract:fuzz -- --seed 17760610729703352915", body)
        self.assertIn("/api/v1/accounts/1/search?q=Subject&after=a", body)
        self.assertIn("commit `abc123`", body)
        self.assertIn("vertrag 0.42.0", body)

    def test_the_label_is_made_only_when_an_issue_opens_and_it_is_missing(self):
        self.fuzz_report([failing(SEARCH_AFTER)])
        self.assert_ok(self.run_script())
        self.assertTrue(
            any(call["args"][:2] == ["label", "create"] for call in self.calls())
        )
        self.log.unlink()
        self.assert_ok(
            self.run_script(env={"GH_LABELS": json.dumps([{"name": "contract-fuzz"}])})
        )
        self.assertFalse(
            any(call["args"][:2] == ["label", "create"] for call in self.calls())
        )

    def test_a_finding_seen_again_rewrites_the_open_issue_and_keeps_first_seen(self):
        self.existing((5, "fuzz", SEARCH_AFTER, "OPEN", ""))
        self.fuzz_report([failing(SEARCH_AFTER)], seed="42")
        self.assert_ok(self.run_script())
        self.assertEqual(self.writes("create"), [])
        edit = self.writes("edit")
        self.assertEqual(len(edit), 1)
        self.assertEqual(edit[0]["args"][2], "5")
        self.assertIn("--seed 42", edit[0]["stdin"])
        self.assertIn(
            "First seen: [run 1](https://example.test), commit `old`", edit[0]["stdin"]
        )
        self.assertIn(marker("fuzz", SEARCH_AFTER), edit[0]["stdin"])
        self.assertEqual(self.writes("comment"), [], "an update notifies nobody")

    def test_a_pass_closes_the_issue(self):
        self.existing((5, "fuzz", SEARCH_AFTER, "OPEN", ""))
        self.fuzz_report([passing(SEARCH_AFTER)])
        self.assert_ok(self.run_script())
        closed = self.writes("close")
        self.assertEqual([call["args"][2] for call in closed], ["5"])
        self.assertEqual(
            closed[0]["args"][closed[0]["args"].index("--reason") + 1], "completed"
        )
        self.assertIn(
            "run 77", closed[0]["args"][closed[0]["args"].index("--comment") + 1]
        )

    def test_a_transaction_not_sent_this_run_says_nothing(self):
        self.existing(
            (5, "fuzz", NEVER_DRAWN, "OPEN", ""), (6, "fuzz", SKIPPED, "OPEN", "")
        )
        self.fuzz_report(
            [
                passing(SEARCH_AFTER),
                {"name": SKIPPED, "status": "skip", "errors": ["nothing generated"]},
            ]
        )
        self.assert_ok(self.run_script())
        self.assertEqual(self.writes(), [])

    def test_a_transaction_that_passed_and_failed_in_one_run_is_still_failing(self):
        self.existing((5, "fuzz", SEARCH_AFTER, "OPEN", ""))
        self.fuzz_report([passing(SEARCH_AFTER), failing(SEARCH_AFTER)])
        self.assert_ok(self.run_script())
        self.assertEqual(self.writes("close"), [])
        self.assertEqual(len(self.writes("edit")), 1)

    def test_a_failure_after_a_pass_reopens_the_same_issue(self):
        self.existing((5, "fuzz", SEARCH_AFTER, "CLOSED", "COMPLETED"))
        self.fuzz_report([failing(SEARCH_AFTER)])
        self.assert_ok(self.run_script())
        self.assertEqual(self.writes("create"), [])
        self.assertEqual([call["args"][2] for call in self.writes("reopen")], ["5"])
        self.assertEqual([call["args"][2] for call in self.writes("edit")], ["5"])

    def test_an_issue_a_person_closed_as_not_planned_stays_closed(self):
        self.existing((5, "fuzz", SEARCH_AFTER, "CLOSED", "NOT_PLANNED"))
        self.fuzz_report([failing(SEARCH_AFTER)])
        self.assert_ok(self.run_script())
        self.assertEqual(self.writes(), [])

    def test_a_run_opens_at_most_the_cap_and_warns_about_the_rest(self):
        extra = tier.MAX_OPENED + 3
        self.fuzz_report(
            [
                failing(f"fuzz: /p{index} > S > 200 > application/json · body · valid")
                for index in range(extra)
            ]
        )
        out = self.run_script()
        self.assert_ok(out)
        self.assertEqual(len(self.writes("create")), tier.MAX_OPENED)
        self.assertEqual(out.stdout.count("::warning::the next run opens:"), 3)

    def test_a_failed_job_with_a_report_files_the_findings_and_closes_nothing(self):
        self.existing((5, "fuzz", SEARCH_BEFORE, "OPEN", ""))
        self.fuzz_report([failing(SEARCH_AFTER), passing(SEARCH_BEFORE)])
        self.assert_ok(self.run_script(result="failure"))
        self.assertEqual(self.writes("close"), [])
        titles = [
            call["args"][call["args"].index("--title") + 1]
            for call in self.writes("create")
        ]
        self.assertIn("Contract fuzz: did not finish", titles)
        self.assertEqual(len(titles), 2)

    def test_no_seed_means_the_run_did_not_finish(self):
        self.existing((5, "fuzz", SEARCH_BEFORE, "OPEN", ""))
        self.fuzz_report([passing(SEARCH_BEFORE)], seed=None)
        self.assert_ok(self.run_script())
        self.assertEqual(self.writes("close"), [])
        created = self.writes("create")
        self.assertEqual(len(created), 1)
        self.assertIn("holds no seed", created[0]["stdin"])


class RunIssue(Harness):
    def test_no_report_files_the_run_and_a_finished_run_closes_it(self):
        self.existing((5, "fuzz", SEARCH_AFTER, "OPEN", ""))
        self.assert_ok(self.run_script(result="failure"))
        self.assertEqual(self.writes("close"), [])
        created = self.writes("create")
        self.assertEqual(len(created), 1)
        self.assertIn("<!-- contract-tier-issue:fuzz:run -->", created[0]["stdin"])
        self.assertIn("fuzz.json could not be read", created[0]["stdin"])

        self.log.unlink()
        self.existing((8, "fuzz", tier.RUN_KEY, "OPEN", ""))
        self.fuzz_report([passing(SEARCH_AFTER)])
        self.assert_ok(self.run_script())
        self.assertEqual([call["args"][2] for call in self.writes("close")], ["8"])

    def test_an_empty_report_proves_nothing(self):
        self.existing((5, "fuzz", SEARCH_AFTER, "OPEN", ""))
        (self.reports / "fuzz.json").write_text(
            json.dumps({"summary": {"total": 0}, "results": []})
        )
        (self.reports / "contract-fuzz.log").write_text(
            "seed: 1 (replay with --seed 1)\n"
        )
        self.assert_ok(self.run_script())
        self.assertEqual(self.writes("close"), [])
        self.assertIn("holds no transactions", self.writes("create")[0]["stdin"])

    def test_an_issue_without_the_marker_or_of_the_other_tier_is_not_touched(self):
        rows = [
            {
                "number": 3,
                "state": "OPEN",
                "stateReason": "",
                "body": "written by a person",
            },
            {
                "number": 4,
                "state": "OPEN",
                "stateReason": "",
                "body": marker("faults", SEARCH_AFTER),
            },
        ]
        self.issues_file.write_text(json.dumps(rows))
        self.fuzz_report([passing(SEARCH_AFTER)])
        self.assert_ok(self.run_script())
        self.assertEqual(self.writes(), [])


class WhichRunsCount(Harness):
    def test_only_the_schedule_v_tags_and_main_change_issues(self):
        self.fuzz_report([failing(SEARCH_AFTER)])
        for event, ref, counted in (
            ("schedule", "refs/heads/main", True),
            ("push", "refs/tags/v1.2.3", True),
            ("workflow_dispatch", "refs/heads/main", True),
            ("workflow_dispatch", "refs/heads/feature", False),
            ("push", "refs/heads/main", False),
            ("push", "refs/tags/nightly", False),
            ("pull_request", "refs/pull/1/merge", False),
        ):
            with self.subTest(event=event, ref=ref):
                self.log.unlink(missing_ok=True)
                self.assert_ok(
                    self.run_script(env={"GITHUB_EVENT_NAME": event, "GITHUB_REF": ref})
                )
                self.assertEqual(bool(self.writes("create")), counted)
                if not counted:
                    self.assertEqual(self.calls(), [], "not even a read")

    def test_a_cancelled_or_skipped_suite_says_nothing(self):
        self.existing((5, "fuzz", SEARCH_AFTER, "OPEN", ""))
        self.fuzz_report([passing(SEARCH_AFTER), failing(AUDIT_SINCE)])
        for result in ("cancelled", "skipped"):
            with self.subTest(result=result):
                self.assert_ok(self.run_script(result=result))
                self.assertEqual(self.calls(), [])

    def test_a_dry_run_reads_and_changes_nothing(self):
        self.existing((5, "fuzz", SEARCH_BEFORE, "OPEN", ""))
        self.fuzz_report([failing(SEARCH_AFTER), passing(SEARCH_BEFORE)])
        out = self.run_script(extra=["--dry-run"])
        self.assert_ok(out)
        self.assertEqual(self.writes(), [])
        self.assertFalse(
            any(call["args"][:2] == ["label", "create"] for call in self.calls())
        )
        self.assertIn("would open Contract fuzz", out.stdout)
        self.assertIn("would close #5", out.stdout)

    def test_a_refused_write_fails_the_job(self):
        self.fuzz_report([failing(SEARCH_AFTER)])
        out = self.run_script(env={"GH_FAIL": "issue create"})
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("HTTP 403: refused", out.stderr)


class Faults(Harness):
    BASELINE = ("(none)", "login", "examples", "green")

    def test_an_uncaught_fault_opens_an_issue_and_a_caught_one_closes_its_issue(self):
        self.existing((5, "faults", "health_status_created", "OPEN", ""))
        self.faults_report(
            [
                self.BASELINE,
                (
                    "health_status_created",
                    "getHealth",
                    "examples",
                    "caught: /api/health > 200: got '201'",
                ),
                (
                    "accounts_list_null",
                    "listAccounts",
                    "examples",
                    "not caught: no failed transaction of /api/v1/accounts",
                ),
            ]
        )
        self.assert_ok(self.run_script("faults", "failure"))
        created = self.writes("create")
        self.assertEqual(len(created), 1)
        args = created[0]["args"]
        self.assertEqual(
            args[args.index("--title") + 1],
            "Contract faults: accounts_list_null failed its check",
        )
        self.assertEqual(args[args.index("--label") + 1], "contract-faults")
        self.assertIn("not caught: no failed transaction", created[0]["stdin"])
        self.assertIn("chore contract:faults", created[0]["stdin"])
        self.assertEqual([call["args"][2] for call in self.writes("close")], ["5"])

    def test_a_verdict_holding_a_pipe_stays_whole(self):
        self.faults_report(
            [self.BASELINE, ("f", "op", "examples", "not caught: wanted a | b, got c")]
        )
        self.assert_ok(self.run_script("faults", "failure"))
        self.assertIn(
            "verdict: not caught: wanted a | b, got c",
            self.writes("create")[0]["stdin"],
        )

    def test_a_known_gap_counts_as_passing_and_now_caught_does_not(self):
        self.faults_report(
            [
                self.BASELINE,
                ("gap", "op", "examples", "known gap: #1"),
                ("moved", "op", "examples", "now caught, so remove known_gap #2: x"),
            ]
        )
        self.assert_ok(self.run_script("faults", "failure"))
        created = self.writes("create")
        self.assertEqual(len(created), 1)
        self.assertIn(
            "moved failed its check",
            created[0]["args"][created[0]["args"].index("--title") + 1],
        )

    def test_a_fault_gone_from_the_catalogue_closes_its_issue(self):
        self.existing((5, "faults", "removed_fault", "OPEN", ""))
        self.faults_report([self.BASELINE, ("kept", "op", "examples", "caught: x")])
        self.assert_ok(self.run_script("faults"))
        self.assertEqual([call["args"][2] for call in self.writes("close")], ["5"])

    def test_a_red_baseline_files_the_run_and_closes_nothing(self):
        self.existing((5, "faults", "kept", "OPEN", ""))
        self.faults_report([("(none)", "login", "examples", "red without a fault: x")])
        self.assert_ok(self.run_script("faults", "failure"))
        self.assertEqual(self.writes("close"), [])
        created = self.writes("create")
        self.assertEqual(len(created), 1)
        self.assertIn("so no fault was judged", created[0]["stdin"])

    def test_a_failure_the_report_does_not_explain_files_the_run_and_closes_nothing(
        self,
    ):
        self.existing((5, "faults", "kept", "OPEN", ""))
        self.faults_report([self.BASELINE, ("kept", "op", "examples", "caught: x")])
        self.assert_ok(self.run_script("faults", "failure"))
        self.assertEqual(self.writes("close"), [])
        self.assertIn("its report does not explain", self.writes("create")[0]["stdin"])

    def test_no_report_files_the_run(self):
        self.assert_ok(self.run_script("faults", "failure"))
        self.assertIn("faults.md could not be read", self.writes("create")[0]["stdin"])


class PublicText(Harness):
    def test_secrets_in_a_finding_are_not_copied_into_the_public_issue(self):
        pem = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIJkOazWQ\n-----END PRIVATE KEY-----"
        self.fuzz_report(
            [
                failing(
                    SEARCH_AFTER,
                    "/api/v1/x?token=abc123secret&password=hunter2&limit=20",
                    [
                        f"body held {pem}",
                        'server said {"password": "hunter3", "api_key": "rm_live"}',
                        "Authorization: Bearer eyJhbGciOi.payload.sig",
                    ],
                )
            ]
        )
        self.assert_ok(self.run_script())
        body = self.writes("create")[0]["stdin"]
        for secret in (
            "abc123secret",
            "hunter2",
            "hunter3",
            "rm_live",
            "eyJhbGciOi",
            "MC4CAQAw",
            "BEGIN PRIVATE KEY",
        ):
            self.assertNotIn(secret, body)
        self.assertIn("limit=20", body)

    def test_a_secret_word_in_a_path_does_not_hide_the_value_that_failed(self):
        self.fuzz_report(
            [
                failing(
                    SEARCH_AFTER, "/api/v1/admin/api-keys?last_used_before=a&limit=20"
                )
            ]
        )
        self.assert_ok(self.run_script())
        self.assertIn(
            "/api/v1/admin/api-keys?last_used_before=a&limit=20",
            self.writes("create")[0]["stdin"],
        )

    def test_the_log_is_read_only_for_its_seed(self):
        self.fuzz_report([failing(SEARCH_AFTER)])
        self.assert_ok(self.run_script())
        self.assertNotIn("MC4CAQAw", self.writes("create")[0]["stdin"])

    def test_a_value_cannot_break_out_of_its_code_block(self):
        self.fuzz_report([failing(SEARCH_AFTER, "/x?q=```\n@someone #1 ````")])
        self.assert_ok(self.run_script())
        body = self.writes("create")[0]["stdin"]
        block = re.search(r"^(`{5,})text\n(.*)\n\1$", body, re.MULTILINE | re.DOTALL)
        self.assertIsNotNone(block, body)
        self.assertIn("@someone", block[2])
        self.assertNotIn("@someone", body[: block.start()] + body[block.end() :])

    def test_a_title_is_one_line_and_bounded(self):
        finding = tier.Finding("k", "a\nb " + "c" * 500, "")
        title = tier.title_of("fuzz", finding)
        self.assertNotIn("\n", title)
        self.assertLessEqual(len(title), tier.MAX_TITLE)


class Workflows(unittest.TestCase):
    def test_each_tier_runs_daily_and_on_every_v_tag_and_files_its_findings(self):
        for name, job in (
            ("contract-fuzz.yml", "fuzz"),
            ("contract-faults.yml", "faults"),
        ):
            with self.subTest(workflow=name):
                source = (WORKFLOWS / name).read_text()
                self.assertRegex(source, r"(?m)^  schedule:\n    - cron: ")
                self.assertRegex(source, r'(?m)^  push:\n    tags: \["v\*"\]$')
                self.assertRegex(source, r"(?m)^  workflow_dispatch:$")
                self.assertNotRegex(
                    source, r"(?m)^  pull_request:", "never on a pull request"
                )
                self.assertIn(f"group: contract-{job}-${{{{ github.ref }}}}", source)
                head, issues = source.split("  finding-issues:\n")
                self.assertNotIn(
                    "issues: write", head, "only the issue job can write issues"
                )
                self.assertIn(f"needs: {job}\n", issues)
                self.assertIn("if: always()\n", issues)
                self.assertIn("issues: write\n", issues)
                self.assertIn(f"group: contract-{job}-issues\n", issues)
                self.assertIn("cancel-in-progress: false\n", issues)
                self.assertIn("name: e2e-container-logs\n", issues)
                self.assertIn(f"SUITE_RESULT: ${{{{ needs.{job}.result }}}}", issues)
                self.assertIn(
                    f'--tier {job} --result "$SUITE_RESULT" --reports e2e-container-logs',
                    issues,
                )

    def test_the_tiers_the_script_files_are_the_workflows_named_here(self):
        for name, settings in tier.TIERS.items():
            self.assertEqual(settings["workflow"], f"contract-{name}.yml")
            self.assertTrue((WORKFLOWS / settings["workflow"]).is_file())


if __name__ == "__main__":
    unittest.main()
