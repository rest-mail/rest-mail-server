#!/usr/bin/env python3
"""Finds the times written into examples and fixtures that have expired (#1942).

Some request fields are taken only in the future: a send's scheduled_at, a
snooze's snoozed_until, the expires_at of a new API key, app password or grant.
A date written into an example of one of them is valid on the day it is written
and refused, 422, once that day has passed, though no code changed: the
scheduled send's example said 2026-10-27, and the contract run that sends it
began answering 422 on that day.

This lists every such date in the examples (openapi.yaml, openapi-example.json
and the in-process matrix, internal/examplepairs/example-matrix.json) and in
the contract run's own sources (tests/contract), and names each one that is
already past or less than MARGIN ahead. A date is left alone when its example
is the refusal of that very field (422 naming it in details.fields), which is
what an expired date is for, or when the run moves it to a time of its own
before sending (RUN_RELATIVE): a time that must be within a year cannot be
written down at all. test_future_times.py holds the repository to an empty
list, so a date is caught a month before it expires rather than on the day.

Usage: future_times.py   (prints what it finds; exit 1 if anything)
"""

import json
import re
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
# How far ahead a written date must still be: a month's warning.
MARGIN = timedelta(days=30)
# The request body fields each operation takes only in the future. The
# handlers refuse anything else with "must be ... in the future"; the test
# reads those refusals out of internal/api/handlers and fails on a field not
# named here.
FUTURE_FIELDS = {
    "sendMessage": "scheduled_at",
    "snoozeThread": "snoozed_until",
    "createAPIKey": "expires_at",
    "createClientSecret": "expires_at",
    "createAppPassword": "expires_at",
    "createGrant": "expires_at",
}
# The handlers' refusal of a time not in the future, which names the field.
HANDLER_REFUSAL = re.compile(r'"(\w+)"\]? *[:=] *"must be (?:at least a minute )?in the future"')
# The examples whose time the contract run replaces before sending, by
# (operationId, example name): the hook in tests/contract/hooks.py that does it.
# A send must be scheduled a minute to a year ahead, so no date stays valid.
RUN_RELATIVE = {
    ("sendMessage", "scheduled"): "a_scheduled_send_an_hour_ahead",
}
# The contract run's own sources: a date beside one of these names.
SOURCE_FIELD = re.compile(
    r"(?i)(scheduled_?at|snoozed?_?until|expires_?at|EXPIRES_AT|SNOOZE_UNTIL)"
)
DATE = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})")
METHODS = ("get", "put", "post", "delete", "patch")


def parse(value):
    """The moment a date-time string names, or None for anything else."""
    if not isinstance(value, str) or not DATE.fullmatch(value):
        return None
    return datetime.fromisoformat(value.replace("Z", "+00:00")).astimezone(UTC)


def refuses(field, status, body):
    """Whether a response is the refusal of this field: a 4xx naming it."""
    if not isinstance(status, int) or not 400 <= status < 500:
        return False
    details = ((body or {}).get("error") or {}).get("details") or {}
    return field in (details.get("fields") or {})


def pairs_from_example_file(path):
    """(operationId, name, request body, status, response body) for each pair
    of an openapi-example.json-shaped file."""
    for operation, named in json.loads(path.read_text()).items():
        for name, pair in named.items():
            request, response = pair.get("request") or {}, pair.get("response") or {}
            yield operation, name, request.get("body"), response.get("status"), response.get("body")


def pairs_from_description(path):
    """The same for openapi.yaml: a request example pairs with the response
    example of the same name (#386)."""
    spec = yaml.safe_load(path.read_text())
    for item in (spec.get("paths") or {}).values():
        for method in METHODS:
            operation = item.get(method) if isinstance(item, dict) else None
            if not isinstance(operation, dict) or "operationId" not in operation:
                continue
            responses = {}
            for status, response in (operation.get("responses") or {}).items():
                for media in ((response or {}).get("content") or {}).values():
                    for name, example in ((media or {}).get("examples") or {}).items():
                        code = int(status) if str(status).isdigit() else None
                        responses[name] = (code, (example or {}).get("value"))
            content = ((operation.get("requestBody") or {}).get("content")) or {}
            for media in content.values():
                for name, example in ((media or {}).get("examples") or {}).items():
                    status, body = responses.get(name, (None, None))
                    yield operation["operationId"], name, (example or {}).get("value"), status, body


def example_findings(source, pairs, now):
    found = []
    for operation, name, request, status, response in pairs:
        field = FUTURE_FIELDS.get(operation)
        if field is None or not isinstance(request, dict):
            continue
        when = parse(request.get(field))
        if when is None or when >= now + MARGIN:
            continue
        if refuses(field, status, response) or (operation, name) in RUN_RELATIVE:
            continue
        found.append(
            f"{source}: {operation} example {name} sends {field} {request[field]}, "
            f"{'already past' if when <= now else 'less than ' + str(MARGIN.days) + ' days ahead'}"
        )
    return found


def source_findings(root, now):
    """Dates beside a future field in the contract run's own sources; its
    tests are left out, as their dates are the examples they check."""
    found = []
    for path in sorted((root / "tests/contract").rglob("*")):
        if (
            not path.is_file()
            or path.suffix not in (".py", ".go", ".json", ".yml", ".yaml", ".sh")
            or path.name.startswith("test_")
            or path.name.endswith("_test.go")
        ):
            continue
        for number, line in enumerate(path.read_text(errors="replace").splitlines(), 1):
            if not SOURCE_FIELD.search(line):
                continue
            for match in DATE.finditer(line):
                when = parse(match.group(0))
                if when is not None and when < now + MARGIN:
                    found.append(
                        f"{path.relative_to(root)}:{number}: "
                        f"{match.group(0)} is {'already past' if when <= now else 'less than ' + str(MARGIN.days) + ' days ahead'}"
                    )
    return found


def findings(root=ROOT, now=None):
    now = now or datetime.now(UTC)
    found = []
    for relative in ("openapi-example.json", "internal/examplepairs/example-matrix.json"):
        found += example_findings(relative, pairs_from_example_file(root / relative), now)
    found += example_findings("openapi.yaml", pairs_from_description(root / "openapi.yaml"), now)
    found += source_findings(root, now)
    return found


if __name__ == "__main__":
    lines = findings()
    print("\n".join(lines) or "no expired or expiring times in the examples")
    sys.exit(1 if lines else 0)
