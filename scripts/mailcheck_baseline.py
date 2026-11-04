#!/usr/bin/env python3
"""Compare an instantmailcheck report with the saved baseline (#1589).

  mailcheck_baseline.py compare BASELINE REPORT
  mailcheck_baseline.py write REPORT BASELINE --tool VERSION --source TEXT

compare exits 0 when no check got worse, 1 when a check got worse or went
missing, and 2 when the baseline or the report holds no usable result, so a run
whose tool broke cannot read as a pass. A check that got better is reported and
does not fail: it is a prompt to save a new baseline.
"""

import argparse
import json
import sys
from pathlib import Path

# Worst to best. An ERROR or a SKIP means the check measured nothing, so neither
# may stand in for a result the baseline holds.
RANK = {"SKIP": 0, "ERROR": 1, "FAIL": 2, "WARN": 3, "PASS": 4}

# Blocklists answer from public services about an address on a private test
# network, so they say nothing about the install and change without it changing.
IGNORED_CATEGORIES = {"reputation"}

EXIT_WORSE = 1
EXIT_NO_RESULT = 2


class NoResult(Exception):
    """The file holds no result that can be compared."""


def read_json(path, what):
    try:
        text = Path(path).read_text()
    except OSError as error:
        raise NoResult(f"{what} {path} cannot be read: {error.strerror}") from error
    if not text.strip():
        raise NoResult(f"{what} {path} is empty")
    try:
        document = json.loads(text)
    except ValueError as error:
        raise NoResult(f"{what} {path} is not JSON: {error}") from error
    if not isinstance(document, dict):
        raise NoResult(f"{what} {path} is not a JSON object")
    return document


def checked_status(status, name, path, what):
    if status not in RANK:
        raise NoResult(f"{what} {path}: check {name!r} has unknown status {status!r}")
    return status


def read_report(path):
    """Return (domain, {check name: check}) from the tool's --json output."""
    document = read_json(path, "report")
    checks = document.get("checks")
    if not isinstance(checks, list) or not checks:
        raise NoResult(f"report {path} holds no checks")
    results = {}
    for check in checks:
        if not isinstance(check, dict) or not check.get("name"):
            raise NoResult(f"report {path} holds a check with no name: {check!r}")
        name = check["name"]
        checked_status(check.get("status"), name, path, "report")
        if name in results:
            raise NoResult(f"report {path} holds check {name!r} twice")
        if check.get("category") not in IGNORED_CATEGORIES:
            results[name] = check
    if not results:
        raise NoResult(f"report {path} holds only ignored checks")
    if not document.get("domain"):
        raise NoResult(f"report {path} names no domain")
    return document["domain"], results


def read_baseline(path):
    """Return (domain, {check name: status}) from a saved baseline."""
    document = read_json(path, "baseline")
    checks = document.get("checks")
    if not isinstance(checks, dict) or not checks:
        raise NoResult(f"baseline {path} holds no checks")
    for name, status in checks.items():
        checked_status(status, name, path, "baseline")
    if not document.get("domain"):
        raise NoResult(f"baseline {path} names no domain")
    return document["domain"], checks


def compare(baseline_path, report_path):
    baseline_domain, baseline = read_baseline(baseline_path)
    report_domain, report = read_report(report_path)
    if baseline_domain != report_domain:
        raise NoResult(f"report is for {report_domain}, the baseline is for {baseline_domain}")

    worse = better = 0
    for name, saved in baseline.items():
        check = report.get(name)
        if check is None:
            worse += 1
            print(f"missing: {name}: {saved} in the baseline, no result in this run")
            continue
        status = check["status"]
        if RANK[status] < RANK[saved]:
            worse += 1
            print(f"worse: {name}: {saved} -> {status}: {check.get('summary', '')}")
        elif RANK[status] > RANK[saved]:
            better += 1
            print(f"better: {name}: {saved} -> {status}: {check.get('summary', '')}")
    for name in sorted(report.keys() - baseline.keys()):
        print(f"new: {name}: {report[name]['status']}, not in the baseline")

    print(f"{baseline_domain}: {len(baseline)} checks compared, {worse} worse or missing, {better} better")
    if better and not worse:
        print("Save a new baseline to hold the better results: mailcheck_baseline.py write")
    return EXIT_WORSE if worse else 0


def write(report_path, baseline_path, tool, source):
    domain, report = read_report(report_path)
    document = {
        "domain": domain,
        "tool": tool,
        "source": source,
        "checks": {name: report[name]["status"] for name in sorted(report)},
    }
    Path(baseline_path).write_text(json.dumps(document, indent=2) + "\n")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    compare_args = commands.add_parser("compare")
    compare_args.add_argument("baseline")
    compare_args.add_argument("report")
    write_args = commands.add_parser("write")
    write_args.add_argument("report")
    write_args.add_argument("baseline")
    write_args.add_argument("--tool", required=True, help="the tool version the report came from")
    write_args.add_argument("--source", required=True, help="where and when the run happened")
    args = parser.parse_args(argv)

    try:
        if args.command == "compare":
            return compare(args.baseline, args.report)
        write(args.report, args.baseline, args.tool, args.source)
        return 0
    except NoResult as error:
        print(f"no result: {error}", file=sys.stderr)
        return EXIT_NO_RESULT


if __name__ == "__main__":
    sys.exit(main())
