#!/usr/bin/env python3
"""Holds the authorization report's 404 refusals to what the owner decided (#672).

A refusal says only what the caller may already know: someone else's account or
message, with no link to the caller, answers the same 404 as one that does not
exist. So authorization.py lists 404 under `authorization.refuse`. Vertrag has
one such list for the whole run, and with it a 404 would pass wherever any
refusal is required, including where the same decision wants something else: an
admin route the caller lacks the role for is 403, and no credential is 401.

This reads the finished report and names every 404 that stood in for a required
refusal anywhere but on an object the path names, on a route that is not an
admin route and whose description documents the 404. Like the matrix itself it
is a report, not a gate.

Usage: authorization_refusals.py <openapi.json> <authorization.json>
"""

import json
import re
import sys

import yaml

ADMIN_ROUTES = "/api/v1/admin/"
# Vertrag names an operation with its query parameters: GET /x{?limit*,offset*}.
QUERY_TEMPLATE = re.compile(r"\{\?[^}]*\}")


def may_hide(spec, operation):
    """Whether a signed-in caller refused this operation may be answered 404."""
    method, _, template = operation.partition(" ")
    path = QUERY_TEMPLATE.sub("", template)
    if "{" not in path or path.startswith(ADMIN_ROUTES):
        return False
    item = spec.get("paths", {}).get(path, {})
    return "404" in item.get(method.lower(), {}).get("responses", {})


def findings(spec, report):
    """One line for each 404 that stood in for a refusal it may not replace."""
    found = []
    for cell in report.get("authorization", {}).get("cells", []):
        required = str(cell.get("expected"))
        if str(cell.get("actual")) != "404" or required not in ("401", "403"):
            continue
        if required == "403" and may_hide(spec, cell["operation"]):
            continue
        found.append(
            f"{cell['operation']} as {cell['principal']}: answered 404 where "
            f"{required} is the refusal; only a signed-in caller's refusal of an "
            "object the path names, on a route that is not an admin route and "
            "documents 404, may be 404"
        )
    return found


def main(spec_path, report_path):
    with open(spec_path) as source:
        spec = yaml.safe_load(source)
    with open(report_path) as source:
        report = json.load(source)
    found = findings(spec, report)
    for line in found:
        print(line)
    return 1 if found else 0


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: authorization_refusals.py <spec> <report.json>")
    sys.exit(main(*sys.argv[1:]))
