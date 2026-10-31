#!/usr/bin/env python3
"""The documented statuses the examples phase does not prove yet, by operation (#1334).

tests/contract/status-baseline.txt lists them, one `operationId STATUS` a line:
each a documented response with no example the live run can send, kept back by
an exclude-matching pattern. The run's configuration takes exactly these out of
the surface it is held to (expand), in place of exclusions of a status on every
operation. It is a ratchet, like coverage-baseline.txt (check): a listed status
that the examples phase sent and passed, or that the description no longer
documents, fails the gate until it is taken out of the list. So the list only
shrinks. An unlisted status that is not proven is a surface shortfall, as before.

Usage: status_baseline.py expand <vertrag-run.yml> <status-baseline.txt>
       status_baseline.py check <openapi.json> <examples.xml> <status-baseline.txt>
"""

import sys
import xml.etree.ElementTree as ET

import yaml

METHODS = ("get", "put", "post", "delete", "patch")
REASON = "not proven yet: tests/contract/status-baseline.txt, which only shrinks (#1334)"


def read(baseline_path):
    entries = []
    with open(baseline_path) as stream:
        for line in stream:
            line = line.split("#", 1)[0].strip()
            if line:
                operation, status = line.split()
                entries.append((operation, status))
    return entries


def expand(config_path, baseline_path):
    with open(config_path) as stream:
        config = yaml.safe_load(stream)
    surface = config.setdefault("surface", {})
    exclude = surface.setdefault("exclude", [])
    for operation, status in read(baseline_path):
        exclude.append({"operation": operation, "status": status, "reason": REASON})
    with open(config_path, "w") as stream:
        yaml.safe_dump(config, stream, sort_keys=False, allow_unicode=True)
    return 0


def operations(spec_path):
    """operationId -> (method, path, summary, documented statuses)."""
    with open(spec_path) as stream:
        spec = yaml.safe_load(stream)
    ops = {}
    for path, item in spec["paths"].items():
        for method in METHODS:
            if method in item:
                op = item[method]
                ops[op["operationId"]] = (method.upper(), path, op.get("summary", ""),
                                          set(map(str, op.get("responses", {}))))
    return ops


def passed(report_path, ops):
    """(operationId, status) of every examples-phase transaction that passed, and
    of every one that did not."""
    by_name = {(path, summary): op for op, (_, path, summary, _) in ops.items()}
    ok, failed = set(), set()
    for case in ET.parse(report_path).getroot().iter("testcase"):
        # "<path> > <summary> > <status>[ > <type>][ · <detail>]", as gate.py reads it.
        parts = case.get("name", "").split(" · ", 1)[0].split(" > ")
        if len(parts) < 3 or (parts[0], parts[1]) not in by_name:
            continue
        key = (by_name[(parts[0], parts[1])], parts[2])
        if case.find("failure") is not None or case.find("error") is not None or case.find("skipped") is not None:
            failed.add(key)
        else:
            ok.add(key)
    return ok, failed


def check(spec_path, report_path, baseline_path):
    ops = operations(spec_path)
    entries = read(baseline_path)
    good = True
    if len(set(entries)) != len(entries) or entries != sorted(entries):
        print("tests/contract/status-baseline.txt must be sorted, with no line twice")
        good = False
    undocumented = [e for e in entries if e[0] not in ops or e[1] not in ops[e[0]][3]]
    ok, failed = passed(report_path, ops)
    proven = [e for e in entries if e in ok and e not in failed]
    if undocumented:
        good = False
        print("Not documented: take these out of tests/contract/status-baseline.txt:")
        for operation, status in undocumented:
            print("  %s %s" % (operation, status))
    if proven:
        good = False
        print("Proven now: take these out of tests/contract/status-baseline.txt:")
        for operation, status in proven:
            print("  %s %s" % (operation, status))
    print("Status baseline: %d documented statuses not proven yet" % len(entries))
    return 0 if good else 1


def main(argv):
    command, args = argv[0], argv[1:]
    if command == "expand" and len(args) == 2:
        return expand(*args)
    if command == "check" and len(args) == 3:
        return check(*args)
    sys.exit(__doc__)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
