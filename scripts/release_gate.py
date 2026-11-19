#!/usr/bin/env python3
"""Refuse a release promotion that the installed-cluster receipts do not cover (#2104, AC06).

scripts/cluster_e2e.py writes one receipt line per step to receipts.txt:
the commit, the image digests the pods ran (the kubelet's imageID), the step,
its result, the run it belongs to and when it finished. This reads the newest
run in that file and authorizes a promotion only when all of these hold:

  - the run has a receipt for every step in cluster_e2e.STEPS (missing),
  - every one of them passed (failed: a fail or skipped step),
  - every receipt names the commit being released, and the run finished
    within --max-age hours of now (stale),
  - for every image the release publishes (RELEASE_IMAGES, the components
    .github/workflows/docker.yml builds), the run recorded exactly one
    digest, and it is the digest the release names (wrong digest).

Otherwise it prints one "release-gate refused:" line per reason and exits 1.
On a pass it prints one "release-gate authorized" line and exits 0.

    python3 scripts/release_gate.py --receipts cluster-report/receipts.txt \\
        --commit <full 40-character SHA> \\
        --image api=sha256:<hex> --image smtp-gateway=sha256:<hex> ... (all seven)

What this does not do: it does not run the stages, fetch receipts or read the
registry. The digests it compares against are the ones given on the command
line. cluster-e2e.yml today installs images it builds itself (tag ci), not the
published release images, so its receipts carry those local digests and this
refuses them for a published release.
"""

from __future__ import annotations

import argparse
import importlib.util
import re
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

# The components docker.yml builds and pushes for a release; a test keeps the
# two lists the same.
RELEASE_IMAGES = ("api", "smtp-gateway", "imap-gateway", "pop3-gateway", "js-filter", "webmail", "admin")

DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
COMMIT = re.compile(r"[0-9a-f]{40}")
PREFIX = "cluster-e2e receipt "
DEFAULT_MAX_AGE_HOURS = 26.0  # a daily run, plus slack for a slow one


def load_steps() -> tuple[str, ...]:
    """The steps cluster_e2e.py runs, read from it so the two cannot drift."""
    spec = importlib.util.spec_from_file_location("cluster_e2e", ROOT / "scripts/cluster_e2e.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return tuple(module.STEPS)


def fields(line: str) -> dict[str, str]:
    """A receipt line's key=value tokens."""
    out = {}
    for token in line[len(PREFIX):].split():
        key, sep, value = token.partition("=")
        if sep:
            out[key] = value
    return out


def recorded_digests(token: str) -> dict[str, set[str]]:
    """digests=name@sha256:...,... as name -> the set of digests recorded for it."""
    out: dict[str, set[str]] = {}
    for entry in token.split(","):
        name, sep, digest = entry.partition("@")
        if sep and name:
            out.setdefault(name, set()).add(digest)
    return out


def parse_time(value: str) -> datetime | None:
    try:
        return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def check(lines: list[str], commit: str, expected: dict[str, str], steps: tuple[str, ...],
          now: datetime, max_age: timedelta) -> tuple[list[str], str]:
    """The reasons to refuse (empty: authorized) and the run that was judged."""
    receipts = [fields(line) for line in lines if line.startswith(PREFIX)]
    if not receipts:
        return ["missing: no cluster-e2e receipts"], ""
    run = receipts[-1].get("run", "")
    if not run or run == "-":
        return ["missing: the newest receipt names no run (written before receipts carried one)"], ""
    current = [r for r in receipts if r.get("run") == run]
    reasons = []

    by_step: dict[str, dict[str, str]] = {}
    for r in current:
        by_step[r.get("step", "")] = r
    for step in steps:
        r = by_step.get(step)
        if r is None:
            reasons.append(f"missing: run {run} has no receipt for step {step}")
        elif r.get("result") != "pass":
            reasons.append(f"failed: run {run} step {step} result={r.get('result', '-')}")

    seen_commits = {r.get("commit", "-") for r in current}
    if seen_commits != {commit}:
        reasons.append(f"stale: run {run} tested commit {','.join(sorted(seen_commits))}, not {commit}")
    times = [parse_time(r.get("at", "")) for r in current]
    if any(t is None for t in times):
        reasons.append(f"stale: run {run} has a receipt with no time it finished")
    elif now - min(times) > max_age:
        reasons.append(f"stale: run {run} began {min(times):%Y-%m-%dT%H:%M:%SZ}, "
                       f"more than {max_age.total_seconds() / 3600:g}h before {now:%Y-%m-%dT%H:%M:%SZ}")

    tokens = {r.get("digests", "unknown") for r in current}
    if len(tokens) != 1:
        reasons.append(f"wrong digest: run {run} recorded different image digests on different steps")
    recorded = recorded_digests(next(iter(tokens))) if len(tokens) == 1 else {}
    for name in sorted(expected):
        got = recorded.get(name, set())
        if not got:
            reasons.append(f"wrong digest: run {run} recorded no digest for {name}")
        elif got != {expected[name]}:
            reasons.append(f"wrong digest: run {run} ran {name}@{','.join(sorted(got))}, "
                           f"the release names {expected[name]}")
    return reasons, run


def parse_args(argv: list[str] | None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="release_gate.py", description=__doc__.split("\n", 1)[0])
    parser.add_argument("--receipts", required=True, help="cluster-e2e receipts.txt")
    parser.add_argument("--commit", required=True, help="the full commit SHA being released")
    parser.add_argument("--image", action="append", default=[], metavar="NAME=sha256:HEX",
                        help="a released image's digest; one for each of " + ", ".join(RELEASE_IMAGES))
    parser.add_argument("--max-age", type=float, default=DEFAULT_MAX_AGE_HOURS,
                        help="hours a run stays fresh (default %(default)s)")
    parser.add_argument("--now", default="", help=argparse.SUPPRESS)  # tests pin the clock
    args = parser.parse_args(argv)
    if not COMMIT.fullmatch(args.commit):
        parser.error("--commit must be a full 40-character lower-case SHA")
    if args.max_age <= 0:
        parser.error("--max-age must be positive")
    expected = {}
    for item in args.image:
        name, sep, digest = item.partition("=")
        if not sep or name not in RELEASE_IMAGES or not DIGEST.fullmatch(digest):
            parser.error(f"--image {item!r}: want one of {', '.join(RELEASE_IMAGES)}=sha256:<64 hex>")
        if name in expected and expected[name] != digest:
            parser.error(f"--image {name} given twice with different digests")
        expected[name] = digest
    absent = [name for name in RELEASE_IMAGES if name not in expected]
    if absent:
        parser.error("--image is needed for every released image; missing " + ", ".join(absent))
    args.expected = expected
    if args.now:
        args.now = parse_time(args.now)
        if args.now is None:
            parser.error("--now must be YYYY-MM-DDTHH:MM:SSZ")
    else:
        args.now = datetime.now(timezone.utc)
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    path = Path(args.receipts)
    lines = path.read_text().splitlines() if path.is_file() else []
    reasons, run = check(lines, args.commit, args.expected, load_steps(), args.now,
                         timedelta(hours=args.max_age))
    if reasons:
        for reason in reasons:
            print(f"release-gate refused: {reason}", file=sys.stderr)
        return 1
    print(f"release-gate authorized commit={args.commit} run={run} "
          f"images={','.join(f'{n}@{args.expected[n]}' for n in RELEASE_IMAGES)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
