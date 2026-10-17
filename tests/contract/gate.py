#!/usr/bin/env python3
"""The contract tests' gate (#376), read from vertrag's JUnit report.

Examples phase: every operation that vertrag.yml does not exclude must be
exercised, and pass. An operation passes when the run sent at least one of its
transactions and none of them failed, errored or was skipped (a skip means the
step it follows failed, so it was never checked). Every exclusion is printed
with the reason written above it in vertrag.yml.

Coverage phase, a ratchet: tests/contract/coverage-baseline.txt lists the
operations whose boundary probes still fail (#388). A failing probe on any
operation not in the baseline fails the gate, and so does an operation in the
baseline whose probes all pass now: the fix takes it out of the baseline in the
same change. So the baseline only shrinks.

TODO(antimatter-studios/vertrag#70): once vertrag reports surface coverage
itself, the examples half becomes `vertrag run --require-surface 100`.

Usage: gate.py <openapi.yaml> <vertrag.yml> <junit.xml> <coverage-baseline.txt>
       gate.py --failing-probes <openapi.yaml> <junit.xml>
           prints the operations with a failing probe, in baseline form.
"""
import os
import re
import sys
import xml.etree.ElementTree as ET

import yaml

METHODS = ("get", "put", "post", "delete", "patch")
PROBE = "coverage: "
LINK = "links: "


def operations(spec_path):
    """(path, summary) -> operationId, for every operation in the spec."""
    spec = yaml.safe_load(open(spec_path))
    ops = {}
    for path, item in spec["paths"].items():
        for method in METHODS:
            if method in item:
                op = item[method]
                ops[(path, op["summary"])] = op["operationId"]
    return ops


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


def baseline(path):
    """The operationIds in the coverage baseline: one a line, # for comments."""
    ids = []
    for line in open(path):
        line = line.split("#", 1)[0].strip()
        if line:
            ids.append(line)
    return ids


def results(report_path, ops):
    """Per operation: the examples-phase outcomes, which operations were probed,
    and which had a probe fail.

    A transaction is named "<path> > <summary> > <status>[ > <type>]", and a
    probe "coverage: <the transaction's name> · <where> · <what>".
    """
    examples, probed, probes_failed = {}, set(), set()
    for case in ET.parse(report_path).getroot().iter("testcase"):
        name = case.get("name")
        bad = any(case.find(tag) is not None for tag in ("failure", "error"))
        if name.startswith(LINK):
            continue  # a link that could not be followed: its cause fails elsewhere
        probe = name.startswith(PROBE)
        if probe:
            name = name[len(PROBE):].split(" · ", 1)[0]
        parts = name.split(" > ")
        op = ops.get((parts[0], parts[1])) if len(parts) >= 3 else None
        if op is None:
            continue
        if probe:
            probed.add(op)
            if bad:
                probes_failed.add(op)
            continue
        if bad:
            outcome = "failed"
        elif case.find("skipped") is not None:
            outcome = "skipped"
        else:
            outcome = "passed"
        examples.setdefault(op, []).append(outcome)
    return examples, probed, probes_failed


def gate(spec_path, config_path, report_path, baseline_path):
    ops = operations(spec_path)
    all_ids = set(ops.values())
    excluded = exclusions(config_path)
    listed = baseline(baseline_path)
    ok = True

    for what, ids in (("vertrag.yml excludes", excluded), ("the coverage baseline lists", listed)):
        unknown = sorted(set(ids) - all_ids)
        if unknown:
            print("%s operations the spec does not have: %s" % (what, ", ".join(unknown)))
            ok = False
    duplicated = sorted({op for op in listed if listed.count(op) > 1})
    if duplicated:
        print("the coverage baseline lists these twice: " + ", ".join(duplicated))
        ok = False

    if not os.path.exists(report_path):
        print("no report at %s: vertrag judged nothing (see its log)" % report_path)
        return 1
    examples, probed, probes_failed = results(report_path, ops)
    required = sorted(all_ids - set(excluded))
    passing = [op for op in required if examples.get(op) and set(examples[op]) == {"passed"}]
    not_passing = [op for op in required if op not in passing]

    print("Operations in the spec:          %d" % len(all_ids))
    print("Excluded, with their reasons:    %d" % len(excluded))
    for op in sorted(excluded):
        print("  %-28s %s" % (op, excluded[op]))

    print("Examples phase: %d of %d operations exercised and passing (%.1f%%)" % (
        len(passing), len(required), 100.0 * len(passing) / max(len(required), 1)))
    if not_passing:
        ok = False
        print("Not passing (each needs every transaction to pass):")
        for op in not_passing:
            outcomes = examples.get(op)
            print("  %-28s %s" % (op, ", ".join(outcomes) if outcomes else "not sent"))

    new = sorted(probes_failed - set(listed))
    # Only an operation that was probed has passed its probes: one the run never
    # probed stays in the baseline.
    fixed = sorted((set(listed) & probed) - probes_failed)
    print("Coverage phase: %d operations with a failing boundary probe; the baseline holds %d" % (
        len(probes_failed), len(listed)))
    if new:
        ok = False
        print("A probe failed on an operation outside tests/contract/coverage-baseline.txt:")
        for op in new:
            print("  " + op)
    if fixed:
        ok = False
        print("Every probe passes now; take these out of tests/contract/coverage-baseline.txt:")
        for op in fixed:
            print("  " + op)
    return 0 if ok else 1


def main(argv):
    if argv[:1] == ["--failing-probes"]:
        spec_path, report_path = argv[1:3]
        ops = operations(spec_path)
        for op in sorted(results(report_path, ops)[2]):
            print(op)
        return 0
    return gate(*argv[:4])


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
