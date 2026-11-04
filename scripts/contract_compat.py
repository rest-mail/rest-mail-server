#!/usr/bin/env python3
"""Check the merge queue's prospective result against main's newest API (#1774).

`API contract changes` (contract_diff.py) reports what a pull request's own
commits change, measured from where it branched. It does not see what main
gained since then, so a branch that was compatible where it started can still
break the API once it lands on the newest main: a stale refactor that drops a
route main added, for example.

This check compares a different pair: main's tip (the merge group's base) and
the merge queue's prospective merged result (the group's head). It uses the same
comparator and classification as the contract diff, and reports under its own
name, because a change it lists may come from main and the branch together
rather than from the pull request alone.

A breaking or security change here needs the same deliberate label as in the
contract diff, on one of the pull requests in the merge group.
"""

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from contract_diff import cell, compare, required_labels  # noqa: E402

TITLE = "API compatibility with newest main"
SQUASH_SUBJECT = re.compile(r"\(#(\d+)\)\s*$")
QUEUE_REF = re.compile(r"/pr-(\d+)-[0-9a-f]+$")


def group_pull_requests(base, head, head_ref="", cwd=None):
    """The pull requests a merge group holds: its squash commits and its ref.

    The queue squashes each entry onto the one ahead of it, so every commit from
    the base to the head is one pull request whose subject ends in `(#N)`. The
    head ref names the entry the group was built for.
    """
    subjects = subprocess.run(
        ["git", "log", "--format=%s", f"{base}..{head}"],
        capture_output=True,
        text=True,
        check=True,
        cwd=cwd,
    ).stdout.splitlines()
    numbers = {int(m.group(1)) for s in subjects if (m := SQUASH_SUBJECT.search(s))}
    if m := QUEUE_REF.search(head_ref):
        numbers.add(int(m.group(1)))
    return sorted(numbers)


def pull_request_labels(repository, number):
    result = subprocess.run(
        ["gh", "api", f"repos/{repository}/pulls/{number}", "--jq", ".labels[].name"],
        capture_output=True,
        text=True,
        check=True,
    )
    return set(result.stdout.splitlines())


def report(changes, labels, pull_requests, base, head):
    missing = sorted(required_labels(changes) - set(labels))
    breaking, security, additive = (
        sum(c["classification"] == kind for c in changes)
        for kind in ("breaking", "security", "additive")
    )
    rows = [
        f"# {TITLE}",
        "",
        f"Compares main's tip `{cell(base)}` with the merge queue's prospective "
        f"result `{cell(head)}`. These are not the pull request's own changes, "
        "which `API contract changes` reports from where it branched: a change "
        "listed here can come from main and the branch together.",
        "",
        "Pull requests in this merge group: "
        + (", ".join(f"#{n}" for n in pull_requests) or "none found")
        + ".",
        "",
        f"{breaking} breaking, {security} security, {additive} additive changes "
        "against newest main.",
        "",
        "Missing labels on the merge group: "
        + ", ".join(f"`{label}`" for label in missing)
        + "."
        if missing
        else "Required change labels are satisfied.",
        "",
    ]
    if changes:
        rows.extend(["| Classification | Operation | Change |", "|---|---|---|"])
        for change in changes:
            operation = " ".join(
                filter(
                    None,
                    (
                        change.get("operation"),
                        change.get("path"),
                        change.get("operationId"),
                    ),
                )
            )
            rows.append(
                f"| {change['classification']} | {cell(operation or change.get('section', 'API'))} "
                f"| {cell(change.get('text', ''))} (`{cell(change['id'])}`) |"
            )
    else:
        rows.append("No consumer-visible change against newest main.")
    return "\n".join(rows) + "\n", missing


def write_summary(text):
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a") as output:
            output.write(text)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base", type=Path, help="description at main's tip")
    parser.add_argument("merged", type=Path, help="description of the merged result")
    parser.add_argument("--oasdiff", default="oasdiff")
    parser.add_argument("--base-sha", default="", help="main's tip, for the group")
    parser.add_argument("--head-sha", default="", help="the merge group's head")
    parser.add_argument("--head-ref", default="", help="the merge group's head ref")
    parser.add_argument(
        "--checkout", type=Path, default=Path("."), help="git checkout holding both"
    )
    parser.add_argument(
        "--repository", help="owner/name; read the group's labels from GitHub"
    )
    parser.add_argument(
        "--label",
        action="append",
        default=[],
        help="A deliberate-change label, for local replay",
    )
    parser.add_argument("--output", type=Path, default=Path(".cache/contract-compat"))
    args = parser.parse_args(argv)
    try:
        labels = set(args.label)
        pull_requests = []
        if args.base_sha and args.head_sha:
            pull_requests = group_pull_requests(
                args.base_sha, args.head_sha, args.head_ref, cwd=args.checkout
            )
        if args.repository:
            for number in pull_requests:
                labels |= pull_request_labels(args.repository, number)
        changes = compare(args.oasdiff, args.base, args.merged)
        markdown, missing = report(
            changes, labels, pull_requests, args.base_sha or "base", args.head_sha or "merged"
        )
        args.output.mkdir(parents=True, exist_ok=True)
        (args.output / "compatibility.json").write_text(
            json.dumps(
                {
                    "base": args.base_sha,
                    "merged": args.head_sha,
                    "pull_requests": pull_requests,
                    "changes": changes,
                    "missing_labels": missing,
                },
                indent=2,
            )
            + "\n"
        )
        (args.output / "compatibility.md").write_text(markdown)
        print(markdown, end="")
        write_summary(markdown)
        return 1 if missing else 0
    except (
        OSError,
        ValueError,
        KeyError,
        TypeError,
        subprocess.CalledProcessError,
    ) as error:
        detail = getattr(error, "stderr", "") or error
        print(f"API compatibility check failed: {detail}", file=sys.stderr)
        write_summary("# API compatibility check failed\n\n" + cell(detail) + "\n")
        return 2


if __name__ == "__main__":
    sys.exit(main())
