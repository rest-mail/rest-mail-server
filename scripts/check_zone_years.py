#!/usr/bin/env python3
"""Fail if a Go test asserts a time zone's clock in the current year or later (#1942).

A machine's tzdata decides when a zone's clock changes, and tzdata releases keep
rewriting the rules for the current year and the years after it: tzdata 2026f
dropped Cuba's return to standard time on 1 November 2026, and a test that
asserted Havana's transition that day failed on a Mac with the new data and passed
on runners with the old. Rules for years that have ended no longer change, so a
test that needs a zone's transitions takes them from an earlier year.

What it looks for, in every tracked *_test.go file, with comments left out:

- a zone name, such as "America/Havana", and a date literal (2026-11-01,
  1-Nov-2026, 1 Nov 2026, 20261101T..., time.Date(2026, ...)) whose year is the
  current year or later;
- on the same line, when the zone's line has dates of its own (a table row is
  judged by its own dates), or else anywhere in the same top-level declaration
  (a test that sets the zone on one line and searches its days on others).

Zones whose clocks are set by a statute in force are allowed, and only those:
see FIXED_BY_LAW. A change to one of those rules would be passed years ahead.
"""

import argparse
import datetime
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

# Rules fixed by statute, so a future date in them is not a guess about tzdata:
# the United States by the Energy Policy Act of 2005, the European Union by
# Directive 2000/84/EC, and Japan, which has had no daylight time since 1952.
FIXED_BY_LAW = frozenset(
    {
        "America/New_York",
        "America/Chicago",
        "America/Denver",
        "America/Los_Angeles",
        "Europe/Berlin",
        "Europe/Paris",
        "Asia/Tokyo",
    }
)

ZONE = re.compile(
    r"\b(?:Africa|America|Antarctica|Arctic|Asia|Atlantic|Australia|Europe|Indian|Pacific"
    r"|US|Canada|Mexico|Brazil|Chile)/[A-Z][A-Za-z_+-]*(?:/[A-Z][A-Za-z_+-]*)?"
)
MONTH = r"(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*"
DATES = (
    re.compile(r"(?<!\d)((?:19|2\d)\d\d)-[01]\d-[0-3]\d(?!\d)"),
    re.compile(r"(?<!\d)((?:19|2\d)\d\d)[01]\d[0-3]\dT\d"),
    re.compile(r"(?<!\d)[0-3]?\d[- ]" + MONTH + r"[- ]((?:19|2\d)\d\d)(?!\d)"),
    re.compile(r"\btime\.Date\(\s*((?:19|2\d)\d\d)\b"),
)
DECLARATION = re.compile(r"^(?:func|var|const|type)\b")


def without_comments(text):
    """The Go source with its comments blanked out, line breaks kept."""
    out = []
    i, length = 0, len(text)
    while i < length:
        char = text[i]
        if text.startswith("//", i):
            end = text.find("\n", i)
            end = length if end < 0 else end
            out.append(" " * (end - i))
            i = end
        elif text.startswith("/*", i):
            end = text.find("*/", i + 2)
            end = length if end < 0 else end + 2
            out.append(re.sub(r"[^\n]", " ", text[i:end]))
            i = end
        elif char in "\"'`":
            j = i + 1
            while j < length and text[j] != char:
                if text[j] == "\\" and char != "`":
                    j += 1
                elif text[j] == "\n" and char != "`":
                    break
                j += 1
            out.append(text[i : j + 1])
            i = j + 1
        else:
            out.append(char)
            i += 1
    return "".join(out)


def years(line):
    return {int(match.group(1)) for pattern in DATES for match in pattern.finditer(line)}


def declarations(lines):
    """(first line number, lines) of each top-level declaration."""
    start = 0
    for number, line in enumerate(lines):
        if number and DECLARATION.match(line):
            yield start, lines[start:number]
            start = number
    yield start, lines[start:]


def findings(text, year):
    """(line number, zone, unsettled years) of each zone a test pins to a date."""
    found = []
    for start, block in declarations(without_comments(text).split("\n")):
        block_years = set().union(*(years(line) for line in block))
        for offset, line in enumerate(block):
            for zone in ZONE.findall(line):
                if zone in FIXED_BY_LAW:
                    continue
                own = years(line)
                late = sorted(y for y in (own or block_years) if y >= year)
                if late:
                    found.append((start + offset + 1, zone, late))
    return found


def tracked_tests(root):
    listing = subprocess.run(
        ["git", "ls-files", "-z", "--", "*_test.go"],
        cwd=root,
        capture_output=True,
        check=True,
    ).stdout.decode()
    return [root / name for name in listing.split("\0") if name]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "paths", nargs="*", type=Path, help="files to check; default: tracked *_test.go"
    )
    parser.add_argument(
        "--year",
        type=int,
        default=datetime.datetime.now(datetime.timezone.utc).year,
        help="the current year (default: this year in UTC)",
    )
    args = parser.parse_args(argv)
    paths = args.paths or tracked_tests(ROOT)
    failed = 0
    for path in paths:
        for number, zone, late in findings(path.read_text(errors="replace"), args.year):
            failed += 1
            shown = path.relative_to(ROOT) if path.is_relative_to(ROOT) else path
            print(
                f"{shown}:{number}: {zone} is asserted in {', '.join(map(str, late))}, "
                f"not before {args.year}"
            )
    if failed:
        print(
            f"\n{failed} zone(s) pinned to a year whose tzdata rules can still change "
            f"(#1942). Take the transition from a year before {args.year}. Only zones "
            "whose rules a statute fixes may use later dates: "
            + ", ".join(sorted(FIXED_BY_LAW))
            + " (FIXED_BY_LAW in scripts/check_zone_years.py)."
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
