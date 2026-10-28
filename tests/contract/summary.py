#!/usr/bin/env python3
"""Print the contract run's verdict from its JUnit reports (#627).

One line per phase that ran: transactions, passed, failed and skipped, read from
.cache/contract/<phase>.xml; the same table goes to .cache/contract/summary.md
beside the HTML and Markdown reports.

Usage: summary.py [report directory] [examples coverage authorization ownership fuzz ...]
"""

import json
import sys
import xml.etree.ElementTree as ET
from pathlib import Path

PHASES = (
    ("examples", "examples"),
    ("coverage", "coverage"),
    ("authorization", "authorization (report only)"),
    ("ownership", "ownership (report only)"),
    ("ownership-domains", "ownership of domains (report only)"),
    ("ownership-mailboxes", "ownership of mailboxes (report only)"),
    ("fuzz", "fuzz (report only)"),
)


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


def summary(directory, phases=None):
    rows = [
        "| Phase | Transactions | Passed | Failed | Skipped |",
        "|---|---:|---:|---:|---:|",
    ]
    for name, label in PHASES:
        if phases is not None and name not in phases:
            continue
        report = Path(directory) / (name + ".xml")
        if not report.exists():
            continue
        total, failed, skipped = counts(report)
        rows.append(
            f"| {label} | {total} | {total - failed - skipped} | {failed} | {skipped} |"
        )
    operations = (
        operation_summary(directory) if phases is None or "examples" in phases else ""
    )
    return "\n".join(rows) + "\n" + operations


def cell(value):
    return str(value).replace("|", r"\|").replace("\n", " ")


def operation_summary(directory):
    """Use Vertrag's own surface verdict, without deriving a second gate."""
    report = Path(directory) / "examples.json"
    if not report.exists():
        return ""
    surface = json.loads(report.read_text()).get("surface")
    if not surface:
        return ""
    figures = surface["run"]
    excluded = surface.get("excluded") or []
    operations_excluded = len(
        {entry["operation"] for entry in excluded if not entry.get("status")}
    )
    rows = [
        "",
        (
            f"Operations: {figures['operationsReached']} reached, "
            f"{figures['operationsPassed']} passed, {figures['operations']} counted, "
            f"{operations_excluded} excluded."
        ),
    ]
    for key, label in (
        ("missed", "Not reached"),
        ("notPassed", "Not passed"),
        ("excluded", "Excluded"),
    ):
        entries = surface.get(key) or []
        if entries:
            rows.extend(
                [
                    "",
                    "### " + label,
                    "",
                    "| Operation | Status | Reason |",
                    "|---|---|---|",
                ]
            )
            for entry in entries:
                rows.append(
                    "| {} | {} | {} |".format(
                        cell(entry["operation"]),
                        cell(entry.get("status", "")),
                        cell(entry["reason"]),
                    )
                )
    return "\n".join(rows) + "\n"


def main(argv):
    directory = argv[0] if argv else ".cache/contract"
    phases = argv[1:] or None
    if phases and set(phases) - {name for name, _ in PHASES}:
        sys.exit("unknown contract phase in summary")
    table = summary(directory, phases)
    (Path(directory) / "summary.md").write_text("# Contract run\n\n" + table)
    print(table, end="")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
