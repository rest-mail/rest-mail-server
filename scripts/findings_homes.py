#!/usr/bin/env python3
"""Every retained finding has a verified tracker home (#2120 AC03, source #1563).

On 2026-11-03 the owner had the findings in the review and contract notes moved
into the issue tracker. The reconciliation was written as tables on #1563 and
#1658: one row per finding, and where it went. This keeps those rows, row by
row, in docs/findings-homes.json, each with a typed home, and proves mechanically
that none is without one. The file is a receipt of that migration, not a place to
track work: open work belongs in the tracker, and a test fails on a new markdown
file of findings.

  findings_homes.py check                       offline: every row has a home, verified, and the counts agree
  findings_homes.py verify                      ask GitHub (gh) about each issue, pull request and comment a row names
  findings_homes.py import --first F --second S --minor M   rebuild the rows from the tables as they are on #1563 / #1658

A home is one of:
  tracker  one or more issues or pull requests, each one that exists (verify records its state)
  comment  a tracker comment, which exists
  commit   a commit, for a finding fixed in one
  file     a file in the repository, and for a heading, the heading in it
  code     a file of the current tree named in the outcome, and for a line, a file that long
  disposition  the finding was judged not to be one, or already fixed with nothing to cite,
           with the reason given, verbatim. Never a home for a finding whose outcome was a new
           issue, a comment on one or a row on #1563.

`check` exits 1 and names each finding without a home, and each home that was not
verified or that does not exist.
"""

import argparse
import json
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
HOMES = ROOT / "docs" / "findings-homes.json"
REPO = "rest-mail/rest-mail-server"

# What the reconciliation said about its own first table, on #1563: 147 rows.
FIRST_BATCH_COUNTS = {"fixed": 60, "commented": 12, "new": 41, "moved": 6, "dropped": 26, "in flight": 2}

# An outcome that a tracker must hold: no reason can stand in for the issue.
NEEDS_TRACKER = {"new", "commented", "in flight", "row"}


def classify(outcome):
    """The outcome's class: how the reconciliation says the finding was dealt with."""
    text = outcome.strip().lower()
    for name in ("fixed", "commented", "new", "moved", "dropped"):
        if text.startswith(name):
            return name
    if "in flight" in text:
        return "in flight"
    # The second table (#1658) says it in its own words.
    if re.search(r"\bnew #\d", text):
        return "new"
    if re.search(r"\bcomment on #\d", text):
        return "commented"
    if re.search(r"\brow on #\d", text):
        return "row"
    return "other"


FILE_CITATION = re.compile(r"`([\w./-]+\.(?:go|py|js|mjs|yml|yaml|md|json|ts|tsx|sh|txt))(?::(\d+)(?:-(\d+))?)?`")


def home_of(outcome):
    """The home a row's outcome names, or None. Numbers are issues or pull requests."""
    numbers = sorted({int(n) for n in re.findall(r"#(\d+)", outcome)})
    shas = sorted(set(re.findall(r"`([0-9a-f]{7,40})`", outcome)))
    moved = re.match(r"moved to README, (.+)$", outcome.strip())
    recorded = re.search(r"recorded in `([^`]+)`", outcome)
    cited = [{"name": m.group(1), **({"line": int(m.group(2))} if m.group(2) else {})} for m in FILE_CITATION.finditer(outcome)]
    if numbers:
        return {"kind": "tracker", "numbers": numbers}
    if shas:
        return {"kind": "commit", "shas": shas}
    if moved:
        return {"kind": "file", "path": "tests/contract/README.md", "heading": re.split(r";|, ", moved.group(1))[0].strip()}
    if recorded:
        return {"kind": "file", "path": f"tests/contract/{recorded.group(1)}"}
    if cited:
        return {"kind": "code", "citations": cited}
    return None


def table_rows(text):
    """The cells of each row of each Markdown table in `text`, header and rule rows left out."""
    rows = []
    for line in text.splitlines():
        if not line.startswith("|") or re.match(r"^\|[-| :]+\|$", line.strip()):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split(" | ")]
        rows.append(cells)
    return rows


def import_rows(first, second, minor):
    """Findings from the three places the reconciliation recorded them."""
    rows = []

    def add(batch, source, finding, outcome, home=None):
        home = home or home_of(outcome)
        rows.append({"id": "", "batch": batch, "source": source, "finding": finding, "outcome": outcome, "class": classify(outcome),
                     "home": home or {"kind": "disposition", "reason": outcome}})

    for cells in table_rows(first)[1:]:
        if len(cells) == 3:
            add("1563/first", cells[0].strip("`"), cells[1], cells[2])
    source = None
    after = second.split("## Where each item went", 1)[-1]
    for line in after.splitlines():
        heading = re.match(r"^### `([^`]+)`", line)
        if heading:
            source = heading.group(1)
        elif source and line.startswith("|") and not re.match(r"^\|[-| :]+\|$", line.strip()):
            cells = [c.strip() for c in line.strip().strip("|").split(" | ")]
            if len(cells) == 2 and cells[0] not in ("Item", "Check"):
                add("1658", source, cells[0], cells[1])
    for cells in table_rows(minor):
        if len(cells) == 3 and cells[0] != "Source":
            number, _, item = cells[0].partition(" ")
            review = {"01": "01-ARCHITECTURE-ASSESSMENT.md", "04": "04-UGLY-SIDE.md", "05": "05-SECURITY-AND-COMPLIANCE.md"}[number.strip("`")]
            add("1563/minor", f"docs/review/2026-04-29-deep-review/{review}", f"{item}: {cells[1]}", "recorded as a comment on #1563 (too small for an issue of its own)", {"kind": "comment", "issue": 1563, "comment": 6609081847})
    for i, row in enumerate(rows, 1):
        row["id"] = f"F{i:03d}"
    return rows


def gh_json(*args):
    out = subprocess.run(["gh", "api", *args], capture_output=True, text=True)
    if out.returncode != 0:
        return None, out.stderr.strip().splitlines()[-1] if out.stderr.strip() else "gh failed"
    return json.loads(out.stdout), None


def verify(data, now):
    """Asks GitHub about every number, comment and commit a row names, and records what it found."""
    verified = {}
    numbers = sorted({n for r in data["rows"] if r["home"]["kind"] == "tracker" for n in r["home"]["numbers"]})
    for n in numbers:
        found, error = gh_json(f"repos/{REPO}/issues/{n}")
        if found is None:
            verified[str(n)] = {"exists": False, "error": error, "verified_at": now}
            continue
        merged = bool((found.get("pull_request") or {}).get("merged_at"))
        verified[str(n)] = {"exists": True, "type": "pull" if "pull_request" in found else "issue", "state": "merged" if merged else found["state"], "title": found["title"], "verified_at": now}
    for r in data["rows"]:
        if r["home"]["kind"] == "comment":
            found, error = gh_json(f"repos/{REPO}/issues/comments/{r['home']['comment']}")
            verified[f"comment:{r['home']['comment']}"] = {"exists": found is not None, "verified_at": now, **({"error": error} if error else {})}
        for sha in r["home"].get("shas", []) if r["home"]["kind"] == "commit" else []:
            found, error = gh_json(f"repos/{REPO}/commits/{sha}")
            verified[f"commit:{sha}"] = {"exists": found is not None, "verified_at": now, **({"error": error} if error else {})}
    data["verified"] = verified
    data["verified_at"] = now
    return data


_TREE = {}


def tree(root):
    """The files git tracks under `root`."""
    if root not in _TREE:
        out = subprocess.run(["git", "-C", str(root), "ls-files", "-z"], capture_output=True, text=True, check=True).stdout
        _TREE[root] = [p for p in out.split("\0") if p]
    return _TREE[root]


def line_count(path):
    try:
        return len(path.read_text(errors="replace").splitlines())
    except OSError:
        return 0


def headings_of(text):
    return [re.sub(r"^#+\s*", "", line).strip().lower() for line in text.splitlines() if line.startswith("#")]


def problems_in(data, root=ROOT):
    """Everything that stops every retained finding having a verified home, one line each, naming the finding."""
    problems = []
    rows = data.get("rows") or []
    verified = data.get("verified") or {}
    if not rows:
        return ["the inventory has no findings"]
    seen = set()
    for r in rows:
        name = f"{r.get('id')} ({r.get('source')}: {str(r.get('finding'))[:70]})"
        if r.get("id") in seen:
            problems.append(f"{name}: id used twice")
        seen.add(r.get("id"))
        home = r.get("home") or {}
        kind = home.get("kind")
        cls = classify(r.get("outcome", ""))
        if r.get("class") != cls:
            problems.append(f"{name}: recorded as {r.get('class')}, but its outcome reads {cls}")
        if kind in (None, "disposition"):
            if cls in NEEDS_TRACKER:
                problems.append(f"{name}: no tracker home, and an outcome of '{cls}' names one")
            elif not str(home.get("reason", "")).strip():
                problems.append(f"{name}: no home and no reason")
            elif cls == "other":
                problems.append(f"{name}: no home and an outcome that is none of the reconciliation's: {r.get('outcome')}")
        elif kind == "tracker":
            if not home.get("numbers"):
                problems.append(f"{name}: a tracker home with no issue or pull request")
            for n in home.get("numbers", []):
                v = verified.get(str(n))
                if not v:
                    problems.append(f"{name}: #{n} was never verified (run findings_homes.py verify)")
                elif not v.get("exists"):
                    problems.append(f"{name}: #{n} does not exist: {v.get('error')}")
            if cls == "fixed" and home.get("numbers") and not any((verified.get(str(n)) or {}).get("state") in ("merged", "closed") for n in home["numbers"]):
                problems.append(f"{name}: 'fixed by' names only open work: {home['numbers']}")
        elif kind == "comment":
            v = verified.get(f"comment:{home.get('comment')}")
            if not v or not v.get("exists"):
                problems.append(f"{name}: comment {home.get('comment')} on #{home.get('issue')} was not found")
        elif kind == "commit":
            for sha in home.get("shas", []):
                v = verified.get(f"commit:{sha}")
                if not v or not v.get("exists"):
                    problems.append(f"{name}: commit {sha} was not found")
        elif kind == "code":
            for cite in home.get("citations", []):
                found = [p for p in tree(root) if p == cite["name"] or p.endswith("/" + cite["name"])]
                if not found:
                    problems.append(f"{name}: {cite['name']} is no file in the current tree")
                elif cite.get("line") and not any(line_count(root / p) >= cite["line"] for p in found):
                    problems.append(f"{name}: {cite['name']}:{cite['line']} is past the end of the file ({', '.join(found[:2])})")
            if not home.get("citations"):
                problems.append(f"{name}: a code home with no file")
        elif kind == "file":
            path = root / home.get("path", "")
            if not path.is_file():
                problems.append(f"{name}: {home.get('path')} does not exist")
            elif home.get("heading") and not any(home["heading"].lower() in h for h in headings_of(path.read_text())):
                problems.append(f"{name}: {home['path']} has no heading '{home['heading']}'")
        else:
            problems.append(f"{name}: unknown kind of home {kind!r}")

    first = [r for r in rows if r.get("batch") == "1563/first"]
    counts = {k: sum(1 for r in first if r["class"] == k) for k in FIRST_BATCH_COUNTS}
    if len(first) != sum(FIRST_BATCH_COUNTS.values()):
        problems.append(f"the first table on #1563 has {sum(FIRST_BATCH_COUNTS.values())} rows, and the inventory has {len(first)}")
    for k, want in FIRST_BATCH_COUNTS.items():
        # The reconciliation counted one row as 'in flight' that it also listed as fixed work.
        if k != "in flight" and counts[k] != want and not (k == "fixed" and counts[k] + 1 == want):
            problems.append(f"#1563 counts {want} '{k}' rows, the inventory has {counts[k]}")
    # The second batch's new issues, #1641 to #1652, are the ones the owner was to read first.
    named = {n for r in rows if r["home"]["kind"] == "tracker" for n in r["home"]["numbers"]}
    for n in range(1641, 1653):
        if n not in named:
            problems.append(f"#{n} was filed for the second batch, and no finding in the inventory names it")
    return problems


def read():
    return json.loads(HOMES.read_text())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("check")
    sub.add_parser("verify")
    imp = sub.add_parser("import")
    for name in ("first", "second", "minor"):
        imp.add_argument(f"--{name}", required=True)
    args = parser.parse_args(argv)
    now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    if args.command == "check":
        data = read()
        problems = problems_in(data)
        for p in problems:
            print(p)
        homes = {}
        for r in data["rows"]:
            homes[r["home"]["kind"]] = homes.get(r["home"]["kind"], 0) + 1
        print(f"{len(data['rows'])} findings: {', '.join(f'{v} {k}' for k, v in sorted(homes.items()))}; {len(problems)} without a verified home")
        return 1 if problems else 0
    if args.command == "verify":
        data = verify(read(), now)
        HOMES.write_text(json.dumps(data, indent=2) + "\n")
        bad = [k for k, v in data["verified"].items() if not v["exists"]]
        print(f"verified {len(data['verified'])} references at {now}; {len(bad)} not found{': ' + ', '.join(bad) if bad else ''}")
        return 1 if bad else 0
    rows = import_rows(Path(args.first).read_text(), Path(args.second).read_text(), Path(args.minor).read_text())
    data = {
        "format": 1,
        "about": "A receipt of the 2026-11-03 migration of findings from markdown files to the issue tracker (#1563, #1658), one row per finding with where it went. Not a tracker: open work is in the issues it names. Rebuilt by scripts/findings_homes.py import; checked by scripts/tests/test_findings_homes.py.",
        "sources": {
            "first": f"https://github.com/{REPO}/issues/1563#issuecomment-6605149054",
            "second": f"https://github.com/{REPO}/pull/1658",
            "minor": f"https://github.com/{REPO}/issues/1563#issuecomment-6609081847",
        },
        "rows": rows,
        "verified": {},
    }
    HOMES.write_text(json.dumps(data, indent=2) + "\n")
    print(f"{HOMES.relative_to(ROOT)}: {len(rows)} findings")
    return 0


if __name__ == "__main__":
    sys.exit(main())
