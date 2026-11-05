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

Skipped valid probes are Vertrag's to fail: run.sh passes
--fail-on-skipped-probes, which also exempts login's generated-credential probes,
and treats its exit 2 as final. A skipped result in a form Vertrag's flag does not
produce still fails here.

Probes a hook ended (lost_probes.py) appear in no report, so the hook worker
counts them and lost-probes-baseline.txt holds them, per operation. A new
operation or a larger count fails; a smaller count is noted, because only a
whole-API topology run can measure it.

Usage: gate.py <openapi.yaml> <vertrag.yml> <junit.xml> <coverage-baseline.txt>
           [--lost-probes lost-probes-baseline.txt lost-probes.txt]
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
SKIPPED_PROBES = re.compile(r"([1-9][0-9]*) valid probe\(s\) skipped: .+")


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
        if case.find("skipped") is not None:
            continue
        probed.add(op)
        if any(case.find(tag) is not None for tag in ("failure", "error")):
            probes_failed.add(op)
    return probed, probes_failed


def skipped_counts(report_path, ops):
    counts = {}
    for case in ET.parse(report_path).getroot().iter("testcase"):
        name = case.get("name", "")
        skipped = case.find("skipped")
        if not name.startswith(PROBE) or skipped is None:
            continue
        parts = name[len(PROBE):].split(" · ", 1)[0].split(" > ")
        operation = ops.get(tuple(parts[:2]))
        match = SKIPPED_PROBES.fullmatch(skipped.get("message", ""))
        if operation is None or len(parts) < 3 or not match or not name.endswith(" · valid probes skipped"):
            raise ValueError("unrecognized skipped coverage result: " + name)
        if not refusal(parts[2]):
            counts[operation] = counts.get(operation, 0) + int(match[1])
    return counts


def skipped_baseline(path, all_ids):
    counts = {}
    if path is None:
        return counts
    with open(path) as source:
        for line in source:
            fields = line.split("#", 1)[0].split()
            if not fields:
                continue
            if (len(fields) != 2 or fields[0] not in all_ids or fields[0] in counts
                    or not re.fullmatch(r"[1-9][0-9]*", fields[1])):
                raise ValueError("invalid skipped-probe baseline entry: " + line.strip())
            counts[fields[0]] = int(fields[1])
    return counts


def lost_counts(path):
    """Probes a hook ended, per operation: one operationId a line (lost_probes.py)."""
    counts = {}
    with open(path) as source:
        for line in source:
            operation = line.strip()
            if operation:
                counts[operation] = counts.get(operation, 0) + 1
    return counts


def check_lost_probes(baseline_path, report_path, all_ids):
    """Print what the count says and answer whether it may pass."""
    if not os.path.exists(report_path):
        print("No count of the probes hooks ended: %s is missing" % report_path)
        return False
    counts = lost_counts(report_path)
    try:
        allowed = skipped_baseline(baseline_path, all_ids)
    except ValueError as err:
        print(str(err))
        return False
    print("Coverage phase: %d probes ended by a hook, unsent and in no report; the baseline holds %d" % (
        sum(counts.values()), sum(allowed.values())))
    held = True
    for operation in sorted(set(counts) | set(allowed)):
        count, limit = counts.get(operation, 0), allowed.get(operation, 0)
        if count > limit:
            print("Probes ended by a hook increased: %s: %d > %d" % (operation, count, limit))
            held = False
        elif count < limit:
            print("Probes ended by a hook decreased: %s: %d < %d; shrink tests/contract/lost-probes-baseline.txt" % (
                operation, count, limit))
    return held


def gate(spec_path, config_path, report_path, baseline_path, lost_baseline_path=None, lost_report_path=None):
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
        counts = skipped_counts(report_path, ops)
    except ET.ParseError as err:
        # vertrag exits 1 when it cannot read the spec, leaving an empty report.
        print("the report at %s is unreadable (%s): vertrag judged nothing (see its log)" % (
            report_path, err))
        return 1
    except ValueError as err:
        print(str(err))
        return 1

    print("Coverage phase: %d valid probes skipped; --fail-on-skipped-probes decides (login probes are exempt)" % (
        sum(counts.values())))

    if lost_baseline_path is not None and not check_lost_probes(lost_baseline_path, lost_report_path, all_ids):
        ok = False

    new = sorted(probes_failed - set(listed))
    # Only an operation that was probed has passed its probes: one the run never
    # probed stays in the baseline.
    fixed = sorted((set(listed) & probed) - probes_failed - counts.keys())
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
    if "--lost-probes" in argv:
        at = argv.index("--lost-probes")
        if at != 4 or len(argv) != 7:
            print(__doc__, file=sys.stderr)
            return 2
        return gate(*argv[:at], *argv[at + 1:])
    if len(argv) != 4:
        print(__doc__, file=sys.stderr)
        return 2
    return gate(*argv)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
