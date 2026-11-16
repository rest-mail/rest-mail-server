#!/usr/bin/env python3
"""Keep one "cluster-e2e is failing" issue in step with the install check (#1750).

cluster-e2e.yml runs this after the install job. A failed scheduled, `v*` tag
or main-branch run opens the issue (label `cluster-e2e`, made if missing) or,
when one is open, comments on it: the run, the commit, the images, the job
steps that failed and the failing step's receipt lines from
cluster-report/receipts.txt. A passing one closes the open issue with a link
to the run. A dispatched run on any other ref, and a cancelled or skipped
install, change nothing.

It follows scheduled.yml's guide-links issue (find the open issue by label and
title, then create, update or close it with gh), with comments in place of a
rewritten body so each failure keeps its own record.

Needs gh with GH_TOKEN, issues: write, and actions: read for the failed steps.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

TITLE = "cluster-e2e is failing"
LABEL = "cluster-e2e"
LABEL_COLOR = "B60205"
LABEL_DESCRIPTION = "The installed-cluster check (cluster-e2e.yml) is failing"
INSTALL_JOB = "Install in kind"


def watched(event: str, ref: str) -> bool:
    """Runs whose result the issue tracks: the schedule, v* tags and main."""
    if event == "schedule":
        return True
    if event == "push":
        return ref.startswith("refs/tags/v")
    if event == "workflow_dispatch":
        return ref == "refs/heads/main"
    return False


def decide(event: str, ref: str, result: str, open_issue: int | None) -> str:
    """open, comment, close or none, for this run and the open issue."""
    if not watched(event, ref):
        return "none"
    if result == "failure":
        return "comment" if open_issue else "open"
    if result == "success":
        return "close" if open_issue else "none"
    return "none"  # cancelled or skipped: says nothing either way


def receipt_fields(line: str) -> dict[str, str]:
    """A receipt line's key=value tokens (cluster_e2e.receipt keeps one per field)."""
    fields = {}
    for token in line.split()[2:]:
        key, sep, value = token.partition("=")
        if sep:
            fields[key] = value
    return fields


def read_receipts(path: Path) -> list[str]:
    try:
        text = path.read_text()
    except OSError:
        return []
    return [line for line in text.splitlines() if line.startswith("cluster-e2e receipt ")]


def failure_report(run_url: str, event: str, ref: str, commit: str, image_tag: str,
                   receipts: list[str], failed_steps: list[str]) -> str:
    """Markdown for the opening body or a comment: one failed run."""
    failing = [line for line in receipts if receipt_fields(line).get("result") == "fail"]
    images = next((receipt_fields(line).get("images") for line in receipts
                   if receipt_fields(line).get("images") not in (None, "unknown")), None)
    lines = [
        f"[This run]({run_url}) failed ({event}, `{ref}`).",
        "",
        f"- commit: `{commit}`",
        f"- images: `{images}`" if images else f"- images: not read from the pods; built as `{image_tag}`",
        "- failed job steps: " + (", ".join(f"`{step}`" for step in failed_steps) if failed_steps else "not read"),
        "",
    ]
    if failing:
        lines += ["Receipt of the failing step (cluster-report/receipts.txt):", "", "```", *failing, "```"]
    elif receipts:
        lines.append("No receipt failed: the failure came after the stages. The cluster-report artifact has the logs.")
    else:
        lines.append("No receipts: the run failed before the stages started. The cluster-report artifact has the logs.")
    return "\n".join(lines) + "\n"


def opening_body(report: str, workflow_url: str) -> str:
    return (
        f"The installed-cluster check ([cluster-e2e.yml]({workflow_url}), #1750) "
        "failed on a scheduled, `v*` tag or main run. Each further failure comments here; "
        "the next passing run closes this issue.\n\n" + report
    )


def gh(*args: str, input_text: str | None = None) -> str:
    out = subprocess.run(["gh", *args], input=input_text, capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError(f"gh {' '.join(args[:3])}: {out.stderr.strip()}")
    return out.stdout


def open_issue(repo: str) -> int | None:
    issues = json.loads(gh("issue", "list", "--repo", repo, "--state", "open", "--label", LABEL,
                           "--json", "number,title", "--limit", "100") or "[]")
    numbers = sorted(issue["number"] for issue in issues if issue["title"] == TITLE)
    return numbers[0] if numbers else None


def failed_job_steps(repo: str, run_id: str) -> list[str]:
    """The install job's failed steps, or [] when they cannot be read."""
    query = (f'.jobs[] | select(.name == "{INSTALL_JOB}") | .steps[]? '
             '| select(.conclusion == "failure") | .name')
    try:
        out = gh("api", "--paginate", f"repos/{repo}/actions/runs/{run_id}/jobs", "--jq", query)
    except RuntimeError as error:
        print(f"::warning::failed steps not read: {error}", file=sys.stderr)
        return []
    return [line for line in out.splitlines() if line.strip()]


def ensure_label(repo: str) -> None:
    names = json.loads(gh("label", "list", "--repo", repo, "--search", LABEL, "--json", "name") or "[]")
    if not any(label["name"] == LABEL for label in names):
        gh("label", "create", LABEL, "--repo", repo, "--color", LABEL_COLOR, "--description", LABEL_DESCRIPTION)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    env = os.environ
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--result", required=True, help="the install job's result (needs.install.result)")
    parser.add_argument("--receipts", default="cluster-report/receipts.txt")
    parser.add_argument("--image-tag", default="ghcr.io/rest-mail/*:ci", help="what the images were built as")
    parser.add_argument("--event", default=env.get("GITHUB_EVENT_NAME", ""))
    parser.add_argument("--ref", default=env.get("GITHUB_REF", ""))
    parser.add_argument("--repo", default=env.get("GITHUB_REPOSITORY", ""))
    parser.add_argument("--commit", default=env.get("GITHUB_SHA", ""))
    parser.add_argument("--run-id", default=env.get("GITHUB_RUN_ID", ""))
    parser.add_argument("--server-url", default=env.get("GITHUB_SERVER_URL", "https://github.com"))
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    if not watched(args.event, args.ref):
        print(f"{args.event} on {args.ref}: not tracked, the issue is left alone")
        return 0
    number = open_issue(args.repo)
    action = decide(args.event, args.ref, args.result, number)
    run_url = f"{args.server_url}/{args.repo}/actions/runs/{args.run_id}"
    if action in ("open", "comment"):
        report = failure_report(run_url, args.event, args.ref, args.commit, args.image_tag,
                                read_receipts(Path(args.receipts)), failed_job_steps(args.repo, args.run_id))
        if action == "open":
            ensure_label(args.repo)
            gh("issue", "create", "--repo", args.repo, "--title", TITLE, "--label", LABEL,
               "--body-file", "-", input_text=opening_body(report, f"{args.server_url}/{args.repo}/actions/workflows/cluster-e2e.yml"))
        else:
            gh("issue", "comment", str(number), "--repo", args.repo, "--body-file", "-", input_text=report)
    elif action == "close":
        gh("issue", "close", str(number), "--repo", args.repo,
           "--comment", f"[This run]({run_url}) passed (`{args.commit}`), so the install check is green again.")
    print(f"{args.event} on {args.ref}, install {args.result}: {action}" + (f" #{number}" if number else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
