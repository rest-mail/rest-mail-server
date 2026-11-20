"""A receipt proves what an acceptance run created and that it removed it.

The harness rule (docs/acceptance-harness.md) is create it, prove it, destroy it, under a
name no other run can have. This module is the mechanical half: it refuses a receipt whose
names escaped the run's prefix, whose created resources were not all removed, and whose
"passed" result observed nothing.

The same prefix is what a sweeper may act on. A sweeper that removes by pattern instead of
by prefix is how an acceptance run deletes someone else's domain.
"""

from __future__ import annotations

import datetime
import json
from typing import Any


REQUIRED = ("criterion", "sha", "run_id", "installation", "prefix", "started", "finished", "result")
RESULTS = ("passed", "failed", "aborted")


def validate(receipt: dict[str, Any]) -> list[str]:
    """Every reason this receipt may not be cited as proof. Empty means it may."""

    problems = [f"missing {field}" for field in REQUIRED if not receipt.get(field)]
    if problems:
        return problems

    prefix = str(receipt["prefix"])
    created = list(receipt.get("created") or [])
    destroyed = list(receipt.get("destroyed") or [])
    observations = list(receipt.get("observations") or [])

    if prefix.endswith((".", "-")) or not prefix.strip(". -"):
        problems.append(f"prefix {prefix!r} is not a usable run label")
    if receipt["result"] not in RESULTS:
        problems.append(f"result {receipt['result']!r} is not one of {', '.join(RESULTS)}")

    for name in created:
        if not str(name).startswith(prefix):
            problems.append(
                f"created {name!r} does not start with the run prefix {prefix!r}: "
                f"a recipe may only create under its own name"
            )
    missing = [name for name in created if name not in destroyed]
    for name in missing:
        problems.append(f"created {name!r} was not destroyed: a run that leaves a resource behind failed")

    unowned = [name for name in destroyed if name not in created]
    for name in unowned:
        problems.append(f"destroyed {name!r} was never created by this run")

    started, finished = _moment(receipt["started"]), _moment(receipt["finished"])
    if started is None:
        problems.append(f"started {receipt['started']!r} is not an RFC 3339 instant")
    if finished is None:
        problems.append(f"finished {receipt['finished']!r} is not an RFC 3339 instant")
    if started and finished and finished < started:
        problems.append("finished is before started")

    if receipt["result"] == "passed" and not observations:
        problems.append("a passed receipt must record what was observed")

    return problems


def sweepable(resources: list[str], prefixes: list[str], max_age_hours: float, now: datetime.datetime) -> list[str]:
    """The resources an orphan sweeper may remove: run-owned and older than the age it states."""

    cutoff = now - datetime.timedelta(hours=max_age_hours)
    due: list[str] = []
    for item in resources:
        name = item["name"] if isinstance(item, dict) else item
        created = item.get("created") if isinstance(item, dict) else None
        if not any(str(name).startswith(prefix) for prefix in prefixes):
            continue
        if created is not None and _moment(created) and _moment(created) > cutoff:
            continue
        due.append(name)
    return due


def load(path) -> dict[str, Any]:
    return json.loads(path.read_text())


def _moment(value) -> datetime.datetime | None:
    if not isinstance(value, str):
        return None
    text = value[:-1] + "+00:00" if value.endswith("Z") else value
    try:
        moment = datetime.datetime.fromisoformat(text)
    except ValueError:
        return None
    return moment if moment.tzinfo else moment.replace(tzinfo=datetime.timezone.utc)
