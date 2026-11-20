#!/usr/bin/env python3
"""The readiness evidence register: bounded, and every current claim has a receipt (#2120 AC04, source #1635).

  readiness_register.py check [--today DATE | --now] [--base REV]
  readiness_register.py archive [--today DATE]
  readiness_register.py verify

docs/readiness/evidence.json holds the claims that are current. Each says what was
observed, when, at which commit, with what result, in one of four states (pass, fail,
blocked, unknown: the last three stay visible and are never rounded up), and carries
at least one receipt that exists: a file in the repository (and text it must still
contain) or a tracker URL that `verify` has asked GitHub about.

Two rules bound it. A claim older than `max_age_days` on the register's `as_of` date is
stale, and the register may hold at most `max_current` claims. `archive` moves what
breaks either rule into docs/readiness/evidence-archive.json with the reason and the
date, and never deletes: `check --base REV` fails if an id that was in either file at
REV is in neither now.

`check` measures age against the register's own `as_of` date, so it gives the same
answer on any day, which suits a pull request gate. `check --now` measures against
today's date, and fails while stale claims are still in the register: run `archive`.
"""

import argparse
import json
import re
import subprocess
import sys
from datetime import date, datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
EVIDENCE = ROOT / "docs" / "readiness" / "evidence.json"
ARCHIVE = ROOT / "docs" / "readiness" / "evidence-archive.json"
STATES = ("pass", "fail", "blocked", "unknown")
CLAIM_KEYS = ("id", "source", "claim", "state", "observed_on", "result", "limits", "receipts")


def parse_date(value):
    try:
        return date.fromisoformat(value) if isinstance(value, str) and re.fullmatch(r"\d{4}-\d{2}-\d{2}", value) else None
    except ValueError:
        return None


def receipt_problems(receipt, verified, root):
    """Why a receipt does not point at something that exists, or an empty list."""
    kind = receipt.get("kind")
    if kind == "file":
        path = root / str(receipt.get("path", ""))
        if not receipt.get("path") or not path.is_file():
            return [f"file {receipt.get('path')} does not exist"]
        needle = receipt.get("contains")
        if needle and needle not in path.read_text(errors="replace"):
            return [f"{receipt['path']} no longer contains {needle!r}"]
        return []
    if kind == "url":
        url = receipt.get("url", "")
        if not re.fullmatch(r"https://github\.com/[\w.-]+/[\w.-]+/(issues|pull)/\d+(#issuecomment-\d+)?", url):
            return [f"{url!r} is not an issue, pull request or comment URL"]
        found = verified.get(url)
        if not found:
            return [f"{url} was never verified (run readiness_register.py verify)"]
        return [] if found.get("exists") else [f"{url} does not exist"]
    return [f"receipt kind {kind!r} is neither file nor url"]


def claim_problems(claim, data, verified, root, as_of):
    name = f"{claim.get('id')} ({str(claim.get('claim'))[:60]})"
    problems = []
    for key in CLAIM_KEYS:
        if key not in claim or claim[key] in ("", None, []) and key != "limits":
            problems.append(f"{name}: no {key}")
    if claim.get("state") not in STATES:
        problems.append(f"{name}: state {claim.get('state')!r} is not one of {', '.join(STATES)}")
    observed = parse_date(claim.get("observed_on"))
    if observed is None:
        problems.append(f"{name}: observed_on is not a date")
    elif as_of and observed > as_of:
        problems.append(f"{name}: observed {observed} is after the register's as_of {as_of}")
    if claim.get("state") == "pass":
        if not re.fullmatch(r"[0-9a-f]{40}", str(claim.get("head", ""))):
            problems.append(f"{name}: a pass names the full commit it was observed at (head)")
        if not str(claim.get("tested", "")).strip():
            problems.append(f"{name}: a pass names what was run (tested)")
    receipts = claim.get("receipts") or []
    if not receipts:
        problems.append(f"{name}: no receipt: a claim with nothing behind it is not evidence")
    for receipt in receipts:
        for why in receipt_problems(receipt, verified, root):
            problems.append(f"{name}: receipt {why}")
    return problems


def problems_in(register, archive, root=ROOT, today=None, base_ids=None):
    """Everything wrong with the register and its archive, one line each, naming the claim."""
    problems = []
    rules = register.get("rules") or {}
    max_age, max_current = rules.get("max_age_days"), rules.get("max_current")
    if not isinstance(max_age, int) or not isinstance(max_current, int) or max_age < 1 or max_current < 1:
        return ["the register has no rules: max_age_days and max_current must be positive whole numbers"]
    as_of = parse_date(register.get("as_of"))
    if as_of is None:
        return ["the register has no as_of date"]
    verified = register.get("verified") or {}
    claims = register.get("claims") or []
    archived = archive.get("archived") or []

    ids = [c.get("id") for c in claims] + [a.get("id") for a in archived]
    for i in sorted({i for i in ids if ids.count(i) > 1}, key=str):
        problems.append(f"{i}: used more than once across the register and its archive")

    if len(claims) > max_current:
        problems.append(f"the register holds {len(claims)} claims, over its limit of {max_current}: run archive")
    measured = today or as_of
    for claim in claims:
        problems += claim_problems(claim, register, verified, root, as_of)
        observed = parse_date(claim.get("observed_on"))
        if observed and (measured - observed).days > max_age:
            on = "the register's as_of date" if today is None else str(today)
            problems.append(f"{claim.get('id')} ({str(claim.get('claim'))[:60]}): stale, observed {observed}, {(measured - observed).days} days before {on} (limit {max_age}): run archive")
    for entry in archived:
        for key in ("archived_on", "archived_reason"):
            if not str(entry.get(key, "")).strip():
                problems.append(f"archived {entry.get('id')}: no {key}")
        if parse_date(entry.get("archived_on")) is None:
            problems.append(f"archived {entry.get('id')}: archived_on is not a date")
        for key in CLAIM_KEYS:
            if key not in entry:
                problems.append(f"archived {entry.get('id')}: lost its {key}")
    for lost in sorted(set(base_ids or []) - {i for i in ids}):
        problems.append(f"{lost}: was in the register, and is in neither it nor its archive now: evidence is archived, not deleted")
    return problems


def archive_stale(register, archive, today):
    """Moves stale claims, then the oldest beyond the count limit, to the archive. Returns the moved ids."""
    rules = register["rules"]
    keep, moved = [], []
    for claim in register["claims"]:
        age = (today - parse_date(claim["observed_on"])).days
        if age > rules["max_age_days"]:
            moved.append((claim, f"observed {age} days before {today}, over the {rules['max_age_days']}-day limit"))
        else:
            keep.append(claim)
    keep.sort(key=lambda c: c["observed_on"])
    while len(keep) > rules["max_current"]:
        oldest = keep.pop(0)
        moved.append((oldest, f"the register is limited to {rules['max_current']} claims, and this was the oldest on {today}"))
    for claim, reason in moved:
        archive.setdefault("archived", []).append({**claim, "archived_on": str(today), "archived_reason": reason})
    register["claims"] = sorted(keep, key=lambda c: c["id"])
    register["as_of"] = str(today)
    return [c["id"] for c, _ in moved]


def verify(register, now):
    """Asks GitHub about each URL a receipt names."""
    for claim in register["claims"]:
        for receipt in claim.get("receipts", []):
            if receipt.get("kind") != "url":
                continue
            url = receipt["url"]
            m = re.fullmatch(r"https://github\.com/([\w.-]+/[\w.-]+)/(?:issues|pull)/(\d+)(?:#issuecomment-(\d+))?", url)
            if not m:
                register.setdefault("verified", {})[url] = {"exists": False, "verified_at": now}
                continue
            api = f"repos/{m.group(1)}/issues/comments/{m.group(3)}" if m.group(3) else f"repos/{m.group(1)}/issues/{m.group(2)}"
            ok = subprocess.run(["gh", "api", api], capture_output=True, text=True).returncode == 0
            register.setdefault("verified", {})[url] = {"exists": ok, "verified_at": now}
    return register


def ids_at(rev):
    ids = []
    for path, key in ((EVIDENCE, "claims"), (ARCHIVE, "archived")):
        out = subprocess.run(["git", "-C", str(ROOT), "show", f"{rev}:{path.relative_to(ROOT)}"], capture_output=True, text=True)
        if out.returncode == 0:
            ids += [c["id"] for c in json.loads(out.stdout).get(key, [])]
    return ids


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("command", choices=["check", "archive", "verify"])
    parser.add_argument("--today")
    parser.add_argument("--now", action="store_true")
    parser.add_argument("--base")
    args = parser.parse_args(argv)
    register = json.loads(EVIDENCE.read_text())
    archive = json.loads(ARCHIVE.read_text()) if ARCHIVE.exists() else {"format": 1, "archived": []}
    today = parse_date(args.today) if args.today else (datetime.now(timezone.utc).date() if args.now or args.command == "archive" else None)
    if args.command == "verify":
        EVIDENCE.write_text(json.dumps(verify(register, datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")), indent=2) + "\n")
        print("verified")
        return 0
    if args.command == "archive":
        moved = archive_stale(register, archive, today)
        EVIDENCE.write_text(json.dumps(register, indent=2) + "\n")
        ARCHIVE.write_text(json.dumps(archive, indent=2) + "\n")
        print(f"archived {len(moved)}: {', '.join(moved) or 'nothing was stale'}")
        return 0
    problems = problems_in(register, archive, today=today, base_ids=ids_at(args.base) if args.base else None)
    for p in problems:
        print(p)
    states = {}
    for c in register["claims"]:
        states[c["state"]] = states.get(c["state"], 0) + 1
    print(f"{len(register['claims'])} current claims ({', '.join(f'{v} {k}' for k, v in sorted(states.items()))}), {len(archive.get('archived', []))} archived: {len(problems)} problems")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
