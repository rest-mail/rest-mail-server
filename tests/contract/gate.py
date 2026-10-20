#!/usr/bin/env python3
"""The contract tests' gate (#376), read from vertrag's JUnit report.

Examples phase: vertrag judges it itself. vertrag.yml's surface gate counts an
operation only when every documented transaction of it was sent and passed,
and requires every operation it does not exclude; a shortfall exits 3 and a
failed transaction 1, and the contract step fails on either. What this script
checks of it is the configuration: every operation never sent
(exclude-operation-id) is taken out of the surface with its reason
(surface.exclude), and the other way round, and both name operations the spec
has.

Coverage phase, a ratchet: tests/contract/coverage-baseline.txt lists the
operations whose boundary probes still fail (#388). A failing probe on any
operation not in the baseline fails the gate, and so does an operation in the
baseline whose probes all pass now: the fix takes it out of the baseline in the
same change. So the baseline only shrinks. A probe built from a documented
refusal (a transaction whose status is 4xx) is not judged: its request is the
refused one, so a valid-mode probe built from it is refused too, which says
nothing about the operation's validation (#386).

Usage: gate.py <openapi.yaml> <vertrag.yml> <junit.xml> <coverage-baseline.txt>
       gate.py --failing-probes <openapi.yaml> <junit.xml>
           prints the operations with a failing probe, in baseline form.
"""
import os
import sys
import xml.etree.ElementTree as ET

import yaml

METHODS = ("get", "put", "post", "delete", "patch")
PROBE = "coverage: "


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
    """The operationIds never sent, and those surface.exclude takes out of the
    count whole (an entry naming a status as well takes out only that status)."""
    config = yaml.safe_load(open(config_path))
    skipped = config.get("exclude-operation-id") or []
    uncounted = [entry["operation"] for entry in (config.get("surface") or {}).get("exclude") or []
                 if "operation" in entry and "status" not in entry]
    return skipped, uncounted


def baseline(path):
    """The operationIds in the coverage baseline: one a line, # for comments."""
    ids = []
    for line in open(path):
        line = line.split("#", 1)[0].strip()
        if line:
            ids.append(line)
    return ids


def refusal(status):
    """Whether a documented status is a refusal (4xx): a probe built from its
    transaction starts from a request the operation already refuses."""
    return status[:1] == "4"


def results(report_path, ops):
    """Which operations were probed, and which had a probe fail, counting only
    probes built from a transaction that is not a refusal.

    A probe is named "coverage: <path> > <summary> > <status>[ > <type>] ·
    <where> · <what>", the transaction it was built from first.
    """
    probed, probes_failed = set(), set()
    for case in ET.parse(report_path).getroot().iter("testcase"):
        name = case.get("name")
        if not name.startswith(PROBE):
            continue
        parts = name[len(PROBE):].split(" · ", 1)[0].split(" > ")
        op = ops.get((parts[0], parts[1])) if len(parts) >= 3 else None
        if op is None or refusal(parts[2]):
            continue
        probed.add(op)
        if any(case.find(tag) is not None for tag in ("failure", "error")):
            probes_failed.add(op)
    return probed, probes_failed


def gate(spec_path, config_path, report_path, baseline_path):
    ops = operations(spec_path)
    all_ids = set(ops.values())
    skipped, uncounted = exclusions(config_path)
    listed = baseline(baseline_path)
    ok = True

    for what, ids in (("exclude-operation-id names", skipped),
                      ("surface.exclude names", uncounted),
                      ("the coverage baseline lists", listed)):
        unknown = sorted(set(ids) - all_ids)
        if unknown:
            print("%s operations the spec does not have: %s" % (what, ", ".join(unknown)))
            ok = False
        duplicated = sorted({op for op in ids if ids.count(op) > 1})
        if duplicated:
            print("%s these twice: %s" % (what, ", ".join(duplicated)))
            ok = False
    for what, ids in (
            ("never sent but counted: add them to surface.exclude, with their reason",
             sorted(set(skipped) - set(uncounted))),
            ("not counted but sent: add them to exclude-operation-id, or count them",
             sorted(set(uncounted) - set(skipped)))):
        if ids:
            print("vertrag.yml: %s: %s" % (what, ", ".join(ids)))
            ok = False

    if not os.path.exists(report_path):
        print("no report at %s: vertrag judged nothing (see its log)" % report_path)
        return 1
    try:
        probed, probes_failed = results(report_path, ops)
    except ET.ParseError as err:
        # vertrag exits 1 when it cannot read the spec, leaving an empty report.
        print("the report at %s is unreadable (%s): vertrag judged nothing (see its log)" % (
            report_path, err))
        return 1

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
        for op in sorted(results(report_path, ops)[1]):
            print(op)
        return 0
    return gate(*argv[:4])


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
