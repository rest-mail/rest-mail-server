#!/usr/bin/env python3
"""The "operations exercised" gate for the contract tests (#376).

Reads openapi.yaml, the operations vertrag.yml excludes, and the JUnit report
of the examples phase. Every operation that is not excluded must have at least
one transaction that passed; anything else fails the job, and every exclusion
is printed with the reason written above it in vertrag.yml.

TODO(antimatter-studios/vertrag#70): once vertrag reports surface coverage
itself, replace this with `vertrag run --require-surface`.

Usage: coverage.py <openapi.yaml> <vertrag.yml> <examples junit.xml>
"""
import re
import sys
import xml.etree.ElementTree as ET

import yaml

METHODS = ("get", "put", "post", "delete", "patch")


def exclusions(config_path):
    """The excluded operationIds, each with the comment block above it."""
    reasons, comment, inside = {}, [], False
    for line in open(config_path):
        stripped = line.strip()
        if re.match(r"^exclude-operation-id:", line):
            inside, comment = True, []
            continue
        if not inside:
            continue
        if line and not line[0].isspace() and stripped:
            break
        if stripped.startswith("#"):
            comment.append(stripped.lstrip("# "))
        elif stripped.startswith("- "):
            reasons[stripped[2:].strip()] = " ".join(comment)
        elif not stripped:
            comment = []
    return reasons


def main():
    spec_path, config_path, report_path = sys.argv[1:4]
    spec = yaml.safe_load(open(spec_path))
    title = spec["info"]["title"]

    operations = {}  # (path, summary) -> operationId
    for path, item in spec["paths"].items():
        for method in METHODS:
            if method in item:
                op = item[method]
                operations[(path, op["summary"])] = op["operationId"]
    all_ids = set(operations.values())

    excluded = exclusions(config_path)
    unknown = sorted(set(excluded) - all_ids)
    if unknown:
        print("vertrag.yml excludes operations the spec does not have: " + ", ".join(unknown))
        return 1

    passed = set()
    for case in ET.parse(report_path).getroot().iter("testcase"):
        if case.find("failure") is not None or case.find("error") is not None or case.find("skipped") is not None:
            continue
        parts = case.get("name").split(" > ")
        if parts[0] != title or len(parts) < 4:
            continue
        op = operations.get((parts[1], parts[2]))
        if op:
            passed.add(op)

    required = all_ids - set(excluded)
    missing = sorted(required - passed)
    exercised = len(required & passed)

    print("Operations in the spec:        %d" % len(all_ids))
    print("Excluded, with their reasons:  %d" % len(excluded))
    for op in sorted(excluded):
        print("  %-28s %s" % (op, excluded[op]))
    print("Exercised by the examples phase: %d of %d (%.1f%%)" % (
        exercised, len(required), 100.0 * exercised / max(len(required), 1)))
    if missing:
        print("Not exercised (no passing transaction):")
        for op in missing:
            print("  " + op)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
