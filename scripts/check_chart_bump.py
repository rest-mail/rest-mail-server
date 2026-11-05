#!/usr/bin/env python3
"""Refuse a chart change that would fail publishing after it merges (#1942).

helm.yml runs on main for each push that touches its paths, and it refuses a
version already published. So a change to those paths without a new version in
Chart.yaml passes every check on its pull request and fails on main. This asks the
same question before the merge, with no registry and no credentials: main's own
version is published, or about to be.
"""

import argparse
import fnmatch
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
CHART = "helm/restmail/Chart.yaml"
WORKFLOW = ".github/workflows/helm.yml"


def git(*args):
    return subprocess.run(
        ["git", *args], cwd=ROOT, check=True, capture_output=True, text=True, timeout=600
    ).stdout


def trigger_paths(workflow_text):
    """The paths of the workflow's push trigger, in order; "!" excludes."""
    block = re.search(r"(?m)^  push:\n(?:    .*\n)*?    paths:\n((?:      (?:- |#).*\n)+)", workflow_text)
    if not block:
        raise ValueError(f"no push paths in {WORKFLOW}")
    return [
        line.split("- ", 1)[1].strip().strip("\"'")
        for line in block.group(1).splitlines()
        if line.lstrip().startswith("- ")
    ]


def matches(pattern, path):
    # In GitHub's filter "**" crosses directories and "*" does not, and these paths
    # use "**" only at the end, so a prefix says the same.
    if pattern.endswith("/**"):
        return path.startswith(pattern[:-2])
    return fnmatch.fnmatchcase(path, pattern)


def starts_publishing(paths, patterns):
    """Whether a push of these paths starts the workflow: the last match decides."""
    for path in paths:
        selected = False
        for pattern in patterns:
            if pattern.startswith("!"):
                selected = selected and not matches(pattern[1:], path)
            else:
                selected = selected or matches(pattern, path)
        if selected:
            return path
    return None


def chart_version(chart_text):
    versions = re.findall(r"(?m)^version: (\S+)$", chart_text)
    if len(versions) != 1:
        raise ValueError(f"no unambiguous version in {CHART}")
    return versions[0]


def check(base):
    # Against where this branched, so main moving on since then counts for nothing.
    start = git("merge-base", base, "HEAD").strip()
    changed = git("diff", "--name-only", start, "HEAD").splitlines()
    path = starts_publishing(changed, trigger_paths((ROOT / WORKFLOW).read_text()))
    if path is None:
        print("no change here starts chart publishing")
        return
    before = chart_version(git("show", f"{start}:{CHART}"))
    after = chart_version((ROOT / CHART).read_text())
    if before == after:
        raise ValueError(
            f"{path} starts chart publishing on main, and {after} is already published: "
            f"bump version in {CHART}"
        )
    print(f"{path} starts chart publishing, as {after} (from {before})")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--base", default="origin/main", help="the commit this change goes onto")
    args = parser.parse_args(argv)
    try:
        check(args.base)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        detail = getattr(error, "stderr", "") or ""
        print(f"::error::{error} {detail}".rstrip(), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
