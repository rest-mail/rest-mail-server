#!/usr/bin/env python3
"""Print the contract run's verdict from its JUnit reports (#627).

One line per phase that ran: transactions, passed, failed and skipped, read from
.cache/contract/<phase>.xml; the same table goes to .cache/contract/summary.md
beside the HTML and Markdown reports.

Usage: summary.py [report directory]
"""
from pathlib import Path
import sys
import xml.etree.ElementTree as ET

PHASES = (("examples", "examples"), ("coverage", "coverage"), ("authorization", "authorization (report only)"),
          ("fuzz", "fuzz (report only)"))


def counts(path):
    """(transactions, failed, skipped) in a JUnit report."""
    root = ET.parse(path).getroot()
    cases = root.iter("testcase")
    total = failed = skipped = 0
    for case in cases:
        total += 1
        if case.find("failure") is not None or case.find("error") is not None:
            failed += 1
        elif case.find("skipped") is not None:
            skipped += 1
    return total, failed, skipped


def summary(directory):
    rows = ["| Phase | Transactions | Passed | Failed | Skipped |", "|---|---:|---:|---:|---:|"]
    for name, label in PHASES:
        report = Path(directory) / (name + ".xml")
        if not report.exists():
            continue
        total, failed, skipped = counts(report)
        rows.append("| %s | %d | %d | %d | %d |" % (label, total, total - failed - skipped, failed, skipped))
    return "\n".join(rows) + "\n"


def main(argv):
    directory = argv[0] if argv else ".cache/contract"
    table = summary(directory)
    (Path(directory) / "summary.md").write_text("# Contract run\n\n" + table)
    print(table, end="")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
