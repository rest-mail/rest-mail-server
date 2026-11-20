#!/usr/bin/env python3
"""Was every commit on main tested by a real E2E run? (#2113)

The full E2E suite runs on the pull request. The merge queue runs only a cheap check
(e2e.yml's `E2E (merge queue delta)`) on each group's commit, which is the commit main
becomes. This audit makes that trade checkable: for each of the last N commits on main
it looks for an E2E run on that exact commit that executed the suite and succeeded.

A run counts only when all of these hold:
  * its head SHA is the commit. A run on any other commit tested another tree.
  * its event is merge_group, push or workflow_dispatch. Those test the commit itself;
    a pull_request run tests GitHub's merge of the pull request head with its base.
  * it is completed, and its required gate job `E2E (full topology)` succeeded. A run
    in progress has proven nothing yet. The run's own conclusion is not used: report-only
    jobs (`E2E (authorization and ownership reports)`, `E2E (live proof reports)`) fail
    a run whose suite and gate passed, and a merge group merges on the gate.
  * one of its suite jobs (`E2E (run full suite)` or `E2E (merge queue delta)`) ran
    and succeeded, and none failed. The gate also posts success when it skipped the
    suite, so a green gate alone is not evidence of a test.
A commit that changes only documentation (the docs-only rule e2e.yml uses) passes on a
successful run that skipped the suite, since e2e.yml skips docs-only changes by design.

  e2e_merge_audit.py [--commits N] [--ref REF] [--repo OWNER/NAME] [--workflow FILE]

Reads the commits from the local clone (first parent, so fetch at least N+1 deep) and
the runs from the GitHub API through `gh api`. Prints one line per commit, writes the
same table to $GITHUB_STEP_SUMMARY when set, and exits 1 when any commit is missing a
run, so the scheduled workflow that runs it fails loudly.
"""

import argparse
import dataclasses
import json
import os
import re
import subprocess
import sys

# The suite jobs in e2e.yml, by the name GitHub reports. A reusable workflow's jobs are
# reported as "<caller name> / <job name>", so these are matched as prefixes.
SUITE_JOBS = ("E2E (run full suite)", "E2E (merge queue delta)")
# The required check e2e.yml's gate job reports.
GATE_JOB = "E2E (full topology)"
# Events whose run tests exactly the commit it names.
COMMIT_EVENTS = {"merge_group", "push", "workflow_dispatch"}
# The docs-only rule of e2e.yml's classify step: these paths never need the suite.
DOCS_ONLY = re.compile(r"(^docs/|\.md$|^LICENSE$|\.txt$)")


@dataclasses.dataclass
class Verdict:
    sha: str
    ok: bool
    reason: str
    found: list


def is_docs_only(paths):
    """True when every path is documentation, as e2e.yml classifies it. No paths is not docs-only."""
    return bool(paths) and all(DOCS_ONLY.search(path) for path in paths)


def suite_jobs(run, conclusion):
    return [job["name"] for job in run.get("jobs", [])
            if job["name"].startswith(SUITE_JOBS) and job.get("conclusion") == conclusion]


def gate(run):
    return next((job.get("conclusion") for job in run.get("jobs", []) if job["name"] == GATE_JOB), None)


def callers(names):
    return ", ".join(sorted(set(name.split(" / ")[0] for name in names)))


def describe(run):
    executed = suite_jobs(run, "success")
    detail = f"suite passed: {callers(executed)}" if executed else "no suite job succeeded"
    if suite_jobs(run, "failure"):
        detail += f"; suite failed: {callers(suite_jobs(run, 'failure'))}"
    state = f"{run.get('status', '?')}/{run.get('conclusion') or '-'}"
    return f"run {run['id']} ({run.get('event', '?')}, {state}, gate {gate(run) or 'absent'}, {detail})"


def judge(sha, docs_only, runs):
    """Decide whether `sha` was tested by a real E2E run, from the runs the API listed for it."""
    mine = [run for run in runs if run.get("head_sha") == sha]
    found = [describe(run) for run in mine]
    candidates = [run for run in mine if run.get("event") in COMMIT_EVENTS]
    succeeded = [run for run in candidates if run.get("status") == "completed" and gate(run) == "success"]
    for run in succeeded:
        executed = suite_jobs(run, "success")
        if executed and not suite_jobs(run, "failure"):
            return Verdict(sha, True, f"{run['event']} run {run['id']} executed {callers(executed)}", found)
    if docs_only and succeeded:
        run = succeeded[0]
        return Verdict(sha, True, f"docs-only commit; {run['event']} run {run['id']} skipped the suite by design", found)
    if not mine:
        missing = "no E2E run on this commit"
    elif not candidates:
        missing = "no merge_group, push or dispatch E2E run on this commit"
    elif not succeeded:
        missing = "no completed, successful merge_group, push or dispatch E2E run on this commit"
    else:
        missing = "a successful run on this commit, but none executed the suite"
    return Verdict(sha, False, missing, found)


def git(*args):
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout


def commits(ref, count):
    lines = git("log", "--first-parent", f"--max-count={count}", "--format=%H%x09%s", ref).splitlines()
    return [tuple(line.split("\t", 1)) for line in lines]


def changed_paths(sha):
    return [path for path in git("diff-tree", "--no-commit-id", "--name-only", "-r", "--root", sha).splitlines() if path]


def gh_api(path):
    result = subprocess.run(["gh", "api", path], capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(f"gh api {path} failed: {result.stderr.strip()}")
    return json.loads(result.stdout)


def runs_for(repo, workflow, sha):
    listing = gh_api(f"repos/{repo}/actions/workflows/{workflow}/runs?head_sha={sha}&per_page=100")
    runs = []
    for run in listing.get("workflow_runs", []):
        record = {key: run.get(key) for key in ("id", "head_sha", "event", "status", "conclusion")}
        record["jobs"] = []
        # Jobs are read only where they can decide something: a completed run.
        if record["status"] == "completed":
            jobs = gh_api(f"repos/{repo}/actions/runs/{record['id']}/jobs?filter=latest&per_page=100")
            record["jobs"] = [{"name": job["name"], "conclusion": job.get("conclusion")} for job in jobs.get("jobs", [])]
        runs.append(record)
    return runs


def default_repo():
    if os.environ.get("GITHUB_REPOSITORY"):
        return os.environ["GITHUB_REPOSITORY"]
    url = git("remote", "get-url", "origin").strip()
    match = re.search(r"github\.com[:/](.+?)(?:\.git)?$", url)
    if not match:
        raise SystemExit(f"cannot tell the repository from origin {url!r}; pass --repo")
    return match[1]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--commits", type=int, default=50, help="how many commits of REF to audit (default 50)")
    parser.add_argument("--ref", default="HEAD", help="the main commit to start from (default HEAD)")
    parser.add_argument("--repo", help="OWNER/NAME (default $GITHUB_REPOSITORY, else origin)")
    parser.add_argument("--workflow", default="e2e.yml", help="the E2E workflow file (default e2e.yml)")
    args = parser.parse_args(argv)
    repo = args.repo or default_repo()

    verdicts = []
    for sha, subject in commits(args.ref, args.commits):
        verdict = judge(sha, is_docs_only(changed_paths(sha)), runs_for(repo, args.workflow, sha))
        verdicts.append((verdict, subject))
        line = f"{'PASS' if verdict.ok else 'FAIL'} {sha[:12]} {subject}\n     {verdict.reason}"
        if not verdict.ok:
            line += "\n     found: " + ("; ".join(verdict.found) if verdict.found else "nothing")
        print(line, flush=True)

    failed = [verdict for verdict, _ in verdicts if not verdict.ok]
    summary = f"{len(verdicts) - len(failed)} of {len(verdicts)} commits on {args.ref} were tested by a real E2E run"
    print(summary)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as out:
            out.write(f"### E2E merge audit\n\n{summary}.\n\n| | Commit | Subject | What was found |\n|---|---|---|---|\n")
            for verdict, subject in verdicts:
                cell = verdict.reason if verdict.ok else verdict.reason + ": " + ("; ".join(verdict.found) or "nothing")
                out.write(f"| {'pass' if verdict.ok else '**FAIL**'} | `{verdict.sha[:12]}` | {subject.replace('|', '/')} | {cell.replace('|', '/')} |\n")
    for verdict in failed:
        print(f"::error::{verdict.sha[:12]} on main has no real E2E run: {verdict.reason}", file=sys.stderr)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
