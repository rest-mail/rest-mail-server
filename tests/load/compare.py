#!/usr/bin/env python3
"""Compare two load runs written by tests/load/run.py (#1583).

  python3 tests/load/compare.py before/results.json after/results.json [--threshold 10] [--fail-on-regression]

For each load: achieved rate, p95 and p99 latency, and errors; for each service
in both runs: max CPU and max memory. A load's change worse than --threshold
percent (a lower rate, higher latency) or any new errors is a REGRESSION; a
service using more is marked but not counted, since use moves with the load.
"""

from __future__ import annotations

import argparse
import json
import sys


def pct(old: float | None, new: float | None) -> float | None:
    if old is None or new is None or old == 0:
        return None
    return (new - old) / old * 100


def row(label: str, old, new, higher_is_better: bool, threshold: float, unit: str = "") -> tuple[str, bool]:
    change = pct(old, new)
    worse = False
    if change is not None:
        worse = (-change if higher_is_better else change) > threshold
    shown = "" if change is None else f"{change:+.1f}%"
    flag = "  REGRESSION" if worse else ""
    fmt = lambda v: "-" if v is None else f"{v:g}{unit}"  # noqa: E731
    return f"  {label:22} {fmt(old):>12} -> {fmt(new):>12}  {shown:>8}{flag}", worse


def compare(a: dict, b: dict, threshold: float) -> tuple[list[str], int]:
    out = [f"before: {a['tested'].get('describe') or a['tested'].get('commit')} ({a['started_at']})",
           f"after:  {b['tested'].get('describe') or b['tested'].get('commit')} ({b['started_at']})"]
    if a.get("targets") != b.get("targets") or a.get("duration_s") != b.get("duration_s"):
        out.append("note: the runs had different targets or durations, so rates are not like for like")
    regressions = 0
    for kind in sorted(set(a["loads"]) | set(b["loads"])):
        la, lb = a["loads"].get(kind, {}), b["loads"].get(kind, {})
        out.append(f"\n{kind}")
        if not la or not lb or "failed_to_run" in la or "failed_to_run" in lb:
            out.append("  only in one run, or did not run in one")
            continue
        checks = [
            row("achieved rate", la.get("achieved_rate"), lb.get("achieved_rate"), True, threshold),
            row("p95 latency", la.get("latency_ms", {}).get("p95"), lb.get("latency_ms", {}).get("p95"), False, threshold, "ms"),
            row("p99 latency", la.get("latency_ms", {}).get("p99"), lb.get("latency_ms", {}).get("p99"), False, threshold, "ms"),
        ]
        ea, eb = la.get("errors", 0), lb.get("errors", 0)
        checks.append((f"  {'errors':22} {ea:>12} -> {eb:>12}" + ("            REGRESSION" if eb > ea else ""), eb > ea))
        for line, worse in checks:
            out.append(line)
            regressions += worse
    ra, rb = a.get("resources", {}), b.get("resources", {})
    shared = sorted(set(ra) & set(rb))
    if shared:
        out.append("\nservices (max)")
        for name in shared:
            for label, key, unit in (("cpu", "cpu_pct", "%"), ("mem", "mem_mib", " MiB")):
                line, _ = row(f"{name} {label}", ra[name][key]["max"], rb[name][key]["max"], False, threshold, unit)
                out.append(line.replace("  REGRESSION", "  higher"))
                # Resource use is reported, not judged: it moves with the load achieved.
    out.append(f"\n{regressions} regression(s) beyond {threshold:g}%")
    return out, regressions


def main(argv: list[str]) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("before")
    p.add_argument("after")
    p.add_argument("--threshold", type=float, default=10.0, help="percent change that counts as a regression (default 10)")
    p.add_argument("--fail-on-regression", action="store_true", help="exit 1 when any regression is found")
    args = p.parse_args(argv)
    with open(args.before) as f:
        a = json.load(f)
    with open(args.after) as f:
        b = json.load(f)
    lines, regressions = compare(a, b, args.threshold)
    print("\n".join(lines))
    return 1 if regressions and args.fail_on_regression else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
