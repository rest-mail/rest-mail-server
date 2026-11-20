"""The decision behind the daily audit that every commit on main had a real E2E run (#2113)."""

import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import e2e_merge_audit as audit  # noqa: E402

A = "a" * 40
B = "b" * 40
DELTA = [{"name": "E2E (merge queue delta) / topology", "conclusion": "success"},
         {"name": "E2E (full topology)", "conclusion": "success"}]
FULL = [{"name": "E2E (run full suite) / topology", "conclusion": "success"},
        {"name": "E2E (run full suite) / JS filter isolation", "conclusion": "success"},
        {"name": "E2E (full topology)", "conclusion": "success"}]
SKIPPED = [{"name": "E2E (merge queue delta) / topology", "conclusion": "skipped"},
           {"name": "E2E (run full suite) / topology", "conclusion": "skipped"},
           {"name": "E2E (full topology)", "conclusion": "success"}]


def run(sha=A, event="merge_group", status="completed", conclusion="success", jobs=DELTA, run_id=1):
    return {"id": run_id, "head_sha": sha, "event": event, "status": status, "conclusion": conclusion, "jobs": jobs}


class Judge(unittest.TestCase):
    def test_a_merge_with_its_successful_delta_run_passes(self):
        verdict = audit.judge(A, False, [run()])
        self.assertTrue(verdict.ok, verdict)
        self.assertIn("merge_group run 1 executed E2E (merge queue delta)", verdict.reason)

    def test_a_merge_with_its_successful_full_run_passes(self):
        self.assertTrue(audit.judge(A, False, [run(jobs=FULL)]).ok)
        self.assertTrue(audit.judge(A, False, [run(event="workflow_dispatch", jobs=FULL)]).ok)

    def test_a_merge_without_any_run_fails(self):
        verdict = audit.judge(A, False, [])
        self.assertFalse(verdict.ok)
        self.assertEqual(verdict.reason, "no E2E run on this commit")
        self.assertEqual(verdict.found, [])

    def test_a_run_that_succeeded_on_another_sha_does_not_count(self):
        verdict = audit.judge(A, False, [run(sha=B)])
        self.assertFalse(verdict.ok)
        self.assertEqual(verdict.reason, "no E2E run on this commit")
        # Nor for a docs-only commit.
        self.assertFalse(audit.judge(A, True, [run(sha=B, jobs=SKIPPED)]).ok)

    def test_an_in_progress_run_is_not_a_success(self):
        for status in ("in_progress", "queued", "waiting"):
            with self.subTest(status=status):
                verdict = audit.judge(A, False, [run(status=status, conclusion=None)])
                self.assertFalse(verdict.ok)
                self.assertIn("no completed, successful", verdict.reason)
                self.assertIn(status, verdict.found[0])
        self.assertFalse(audit.judge(A, True, [run(status="in_progress", conclusion=None, jobs=[])]).ok)

    def test_a_run_whose_gate_did_not_pass_is_not_a_success(self):
        for conclusion in ("failure", "cancelled", "timed_out", "skipped"):
            with self.subTest(conclusion=conclusion):
                jobs = DELTA[:1] + [{"name": "E2E (full topology)", "conclusion": conclusion}]
                self.assertFalse(audit.judge(A, False, [run(conclusion="failure", jobs=jobs)]).ok)
        self.assertFalse(audit.judge(A, False, [run(jobs=DELTA[:1])]).ok, "a run with no gate job")

    def test_a_report_only_failure_does_not_undo_a_passed_suite_and_gate(self):
        # The run concludes failure when a report-only job fails, yet the queue merges on the gate.
        jobs = FULL + [{"name": "E2E (live proof reports) / topology", "conclusion": "failure"}]
        verdict = audit.judge(A, False, [run(conclusion="failure", jobs=jobs)])
        self.assertTrue(verdict.ok, verdict)

    def test_a_failed_suite_job_is_not_outweighed_by_a_passing_one(self):
        jobs = FULL[:1] + [{"name": "E2E (run full suite) / JS filter isolation", "conclusion": "failure"}] + FULL[2:]
        verdict = audit.judge(A, False, [run(jobs=jobs)])
        self.assertFalse(verdict.ok)
        self.assertIn("suite failed: E2E (run full suite)", verdict.found[0])

    def test_a_green_gate_that_skipped_the_suite_is_not_a_test_of_code(self):
        # The push to main posts success without running anything.
        verdict = audit.judge(A, False, [run(event="push", jobs=SKIPPED)])
        self.assertFalse(verdict.ok)
        self.assertEqual(verdict.reason, "a successful run on this commit, but none executed the suite")
        self.assertIn("no suite job succeeded", verdict.found[0])

    def test_a_docs_only_commit_passes_on_a_successful_run_that_skipped_the_suite(self):
        verdict = audit.judge(A, True, [run(jobs=SKIPPED)])
        self.assertTrue(verdict.ok)
        self.assertIn("docs-only", verdict.reason)

    def test_a_pull_request_run_does_not_test_the_commit_it_names(self):
        verdict = audit.judge(A, False, [run(event="pull_request", jobs=FULL)])
        self.assertFalse(verdict.ok)
        self.assertEqual(verdict.reason, "no merge_group, push or dispatch E2E run on this commit")

    def test_one_good_run_among_bad_ones_is_enough(self):
        failed = [{"name": "E2E (merge queue delta) / topology", "conclusion": "failure"},
                  {"name": "E2E (full topology)", "conclusion": "failure"}]
        runs = [run(conclusion="failure", jobs=failed, run_id=1), run(event="push", jobs=SKIPPED, run_id=2), run(run_id=3)]
        verdict = audit.judge(A, False, runs)
        self.assertTrue(verdict.ok)
        self.assertIn("run 3", verdict.reason)
        self.assertEqual(len(verdict.found), 3)

    def test_the_docs_only_rule_matches_e2e_yml(self):
        self.assertTrue(audit.is_docs_only(["docs/a.md", "README.md", "LICENSE", "notes/x.txt"]))
        self.assertFalse(audit.is_docs_only(["docs/a.md", "internal/api/server.go"]))
        self.assertFalse(audit.is_docs_only([]))
        workflow = (Path(__file__).resolve().parents[2] / ".github/workflows/e2e.yml").read_text()
        self.assertIn(f"grep -vE '{audit.DOCS_ONLY.pattern}'", workflow)

    def test_the_suite_job_names_are_the_ones_e2e_yml_gives(self):
        workflow = (Path(__file__).resolve().parents[2] / ".github/workflows/e2e.yml").read_text()
        for name in audit.SUITE_JOBS:
            self.assertIn(f"    name: {name}\n", workflow)


class Main(unittest.TestCase):
    """The command over a real repository, with the API answered from a table."""

    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.repo = Path(self.directory.name)
        self.git("init", "-q")
        self.shas = {}
        for name, path in (("code", "internal/x.go"), ("docs", "docs/x.md"), ("untested", "cmd/y.go")):
            (self.repo / path).parent.mkdir(parents=True, exist_ok=True)
            (self.repo / path).write_text(name)
            self.git("add", "-A")
            self.git("-c", "user.name=t", "-c", "user.email=t@t.test", "commit", "-q", "-m", name)
            self.shas[name] = self.git("rev-parse", "HEAD").strip()
        cwd = os.getcwd()
        os.chdir(self.repo)
        self.addCleanup(os.chdir, cwd)

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True, text=True).stdout

    def main(self, runs, *args):
        def api(path):
            if "/jobs?" in path:
                run_id = int(path.split("/runs/")[1].split("/")[0])
                return {"jobs": next(r["jobs"] for rs in runs.values() for r in rs if r["id"] == run_id)}
            sha = path.split("head_sha=")[1].split("&")[0]
            return {"workflow_runs": [{k: v for k, v in r.items() if k != "jobs"} for r in runs.get(sha, [])]}

        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(audit, "gh_api", side_effect=api), redirect_stdout(out), redirect_stderr(err), \
                mock.patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": ""}):
            code = audit.main(["--repo", "o/r", *args])
        return code, out.getvalue(), err.getvalue()

    def test_every_commit_tested_passes(self):
        runs = {self.shas["code"]: [run(sha=self.shas["code"], run_id=1)],
                self.shas["docs"]: [run(sha=self.shas["docs"], jobs=SKIPPED, run_id=2)],
                self.shas["untested"]: [run(sha=self.shas["untested"], jobs=FULL, event="workflow_dispatch", run_id=3)]}
        code, out, _ = self.main(runs)
        self.assertEqual(code, 0, out)
        self.assertIn("3 of 3 commits", out)

    def test_a_commit_without_a_real_run_fails_and_names_what_was_found(self):
        untested = self.shas["untested"]
        runs = {self.shas["code"]: [run(sha=self.shas["code"], run_id=1)],
                self.shas["docs"]: [run(sha=self.shas["docs"], jobs=SKIPPED, run_id=2)],
                untested: [run(sha=untested, event="push", jobs=SKIPPED, run_id=3)]}
        code, out, err = self.main(runs)
        self.assertEqual(code, 1, out)
        self.assertIn(f"FAIL {untested[:12]} untested", out)
        self.assertIn("run 3 (push, completed/success, gate success, no suite job succeeded)", out)
        self.assertIn("2 of 3 commits", out)
        self.assertIn(f"::error::{untested[:12]}", err)

    def test_it_audits_only_the_commits_asked_for(self):
        runs = {self.shas["untested"]: [run(sha=self.shas["untested"], run_id=1)]}
        code, out, _ = self.main(runs, "--commits", "1")
        self.assertEqual(code, 0, out)
        self.assertIn("1 of 1 commits", out)

    def test_the_summary_table_is_written_for_the_run_page(self):
        summary = self.repo / "summary.md"
        runs = {sha: [run(sha=sha, run_id=i)] for i, sha in enumerate(self.shas.values())}

        def api(path):
            if "/jobs?" in path:
                return {"jobs": DELTA}
            sha = path.split("head_sha=")[1].split("&")[0]
            return {"workflow_runs": [{k: v for k, v in r.items() if k != "jobs"} for r in runs[sha]]}

        with mock.patch.object(audit, "gh_api", side_effect=api), redirect_stdout(io.StringIO()), \
                mock.patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": str(summary)}):
            self.assertEqual(audit.main(["--repo", "o/r"]), 0)
        text = summary.read_text()
        self.assertIn("### E2E merge audit", text)
        self.assertEqual(text.count("| pass |"), 3)

    def test_jobs_are_read_only_for_completed_runs(self):
        calls = []

        def api(path):
            calls.append(path)
            return {"workflow_runs": [{"id": 9, "head_sha": A, "event": "merge_group", "status": "in_progress", "conclusion": None}]}

        with mock.patch.object(audit, "gh_api", side_effect=api):
            runs = audit.runs_for("o/r", "e2e.yml", A)
        self.assertEqual(calls, [f"repos/o/r/actions/workflows/e2e.yml/runs?head_sha={A}&per_page=100"])
        self.assertEqual(runs[0]["jobs"], [])
        self.assertEqual(json.loads(json.dumps(runs))[0]["status"], "in_progress")


if __name__ == "__main__":
    unittest.main()
