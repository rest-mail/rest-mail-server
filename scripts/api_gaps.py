#!/usr/bin/env python3
"""Reconcile go-oapifly warnings with operation issues and strict-area PRs (#666).

The input is the complete configuration-union report from TestAPIGapReport.
Dry-run is the default. Only main's api-gaps workflow uses --apply.
"""

import argparse
import base64
import json
import re
import subprocess
import time
from collections import defaultdict
from pathlib import Path

BEGIN = "<!-- restmail-api-gap:begin -->"
END = "<!-- restmail-api-gap:end -->"
IDENTITY = re.compile(r"<!-- restmail-api-gap:operation:([A-Za-z0-9_-]+) -->")
STRICT_PATH = "internal/api/strict_areas.json"
ID = re.compile(r"[A-Za-z][A-Za-z0-9_-]*\Z")

CLEAR = {
    "example-missing": "Add a named request/response pair for this status to openapi-example.json.",
    "example-invalid": "Correct the named pair or the registered schema so the pair validates.",
    "example-orphaned": "Update the named pair to an operation, parameter, status and media type the code registers.",
    "path-parameter-untyped": "Give the path parameter a field in the registered request type.",
    "field-unplaced": "Place the field in the path, query, header or request body with its binding tag.",
    "type-chosen-at-runtime": "Register the concrete wire alternatives instead of an interface chosen at runtime.",
    "unresolved-type": "Register the actual wire type, or include its source in the generator's type directories.",
    "security-scheme-undefined": "Define the authentication scheme the registration actually enforces.",
    "constraint-invalid": "Make the registered constraint valid for the field's Go type.",
}


def text(value):
    """Keep generator values on one Markdown line, including embedded comments."""
    return (
        str(value)
        .replace("`", "\\`")
        .replace("<", "&lt;")
        .replace(">", "&gt;")
        .replace("\n", " ")
    )


def operation_of(issue):
    body = issue.get("body") or ""
    matches = IDENTITY.findall(body)
    if len(matches) > 1:
        raise ValueError(f"issue #{issue['number']} has multiple operation markers")
    if matches:
        return matches[0]
    # Adopt the pre-generator issue loop's title without discarding its prose.
    match = re.fullmatch(r"Type ([A-Za-z][A-Za-z0-9_-]*): \d+ gaps", issue["title"])
    return match[1] if match else None


def generated_body(old, operation, lines):
    """Replace only our bounded block; human text and comments are untouched."""
    old = old or ""
    if old.count(BEGIN) != old.count(END) or old.count(BEGIN) > 1:
        raise ValueError(
            "malformed generated markers; refusing to overwrite commentary"
        )
    identity = f"<!-- restmail-api-gap:operation:{operation} -->"
    block = BEGIN + "\n" + identity + "\n" + "\n".join(lines) + "\n" + END
    if BEGIN in old:
        start, end = old.index(BEGIN), old.index(END) + len(END)
        if start >= end - len(END):
            raise ValueError("reversed generated markers")
        return old[:start] + block + old[end:]
    return old + ("\n\n" if old else "") + block + "\n"


def validate(report, areas):
    if (
        report.get("version") != 1
        or not isinstance(report.get("operations"), list)
        or not isinstance(report.get("warnings"), list)
    ):
        raise ValueError("unsupported or incomplete generator report")
    owners = {}
    for area, ids in areas.items():
        if not area.isdigit() or not 630 <= int(area) <= 652 or not ids:
            raise ValueError(f"invalid area {area}")
        for operation in ids:
            if not ID.fullmatch(operation) or operation in owners:
                raise ValueError(f"ambiguous area for {operation}")
            owners[operation] = area
    operations = {}
    for op in report["operations"]:
        operation = op.get("operationId", "")
        if (
            operation not in owners
            or operation in operations
            or not op.get("file")
            or not op.get("line")
            or not op.get("method")
            or not op.get("path")
        ):
            raise ValueError(
                f"unmapped, duplicate or incomplete operation: {operation}"
            )
        operations[operation] = op
    # An omitted configuration cannot accidentally close its issues or propose
    # strict mode. New operations also require an explicit, unique area owner.
    if set(operations) != set(owners):
        raise ValueError(
            f"report omits mapped operations: {sorted(set(owners) - set(operations))}"
        )
    grouped = defaultdict(dict)
    for warning in report["warnings"]:
        operation = warning.get("operationId")
        if (
            operation not in operations
            or not warning.get("kind")
            or not warning.get("message")
        ):
            raise ValueError(f"unattributed or invalid warning: {warning}")
        grouped[operation][json.dumps(warning, sort_keys=True)] = warning
    return (
        operations,
        owners,
        {op: [items[key] for key in sorted(items)] for op, items in grouped.items()},
    )


def warning_lines(op, area, warnings):
    lines = [
        f"Area #{area}; `{op['method']} {op['path']}`.",
        "",
        "Generated from the registered API and its example pairs on main.",
        "",
    ]
    for warning in warnings:
        source = warning.get("file") or op["file"]
        line = warning.get("line") or (1 if warning.get("file") else op["line"])
        facts = "; ".join(
            f"{text(fact['source'])}: {text(fact['value'])}"
            for fact in warning.get("facts", [])
        )
        if not facts:
            facts = "registration/example report: " + text(warning["message"])
        clear = CLEAR.get(
            warning["kind"],
            f"Correct {warning['kind']} at the source until the generator no longer reports it.",
        )
        lines.append(
            f"- **{text(warning['kind'])}** — {text(warning['message'])}; facts: {facts}; source: `{text(source)}:{line}`. Clears when: {text(clear)}"
        )
    lines += [
        "",
        "Done when this operation has no generator warnings. This issue closes automatically and reopens if a warning returns.",
    ]
    return lines


def plan_issues(report, areas, issues):
    operations, owners, grouped = validate(report, areas)
    existing = defaultdict(list)
    # Validate every managed body before performing any mutation.
    for issue in issues:
        operation = operation_of(issue)
        if operation:
            generated_body(issue.get("body"), operation, [])
            existing[operation].append(issue)
    changes = []
    for operation in sorted(set(existing) | set(grouped)):
        copies = sorted(existing[operation], key=lambda issue: issue["number"])
        canonical = copies[0] if copies else None
        warnings = grouped.get(operation, [])
        if warnings:
            lines = warning_lines(operations[operation], owners[operation], warnings)
        else:
            lines = ["No generator warnings remain for this operation on main."]
            if operation not in operations:
                lines = ["This operation is no longer registered on main."]
        change = {
            "operation": operation,
            "issue": canonical,
            "area": owners.get(operation),
            "title": f"Type {operation}: {len(warnings)} gaps",
            "body": generated_body(
                canonical.get("body") if canonical else "", operation, lines
            ),
            "state": "open" if warnings else "closed",
        }
        if canonical or warnings:
            changes.append(change)
        for duplicate in copies[1:]:
            changes.append(
                {
                    "operation": operation,
                    "issue": duplicate,
                    "area": None,
                    "title": duplicate["title"],
                    "body": generated_body(
                        duplicate.get("body"),
                        operation,
                        [
                            f"Duplicate tracking consolidated into #{canonical['number']}; all warnings are kept there."
                        ],
                    ),
                    "state": "closed",
                }
            )
    clean = {
        area: sorted(ids)
        for area, ids in areas.items()
        if not any(op in grouped for op in ids)
    }
    return changes, clean


class GitHub:
    def __init__(self, repo):
        self.repo = repo
        self.last_write = 0.0

    def api(self, path, method="GET", data=None):
        if method != "GET":
            # Initial reconciliation can create many operation issues. Pace
            # content writes instead of retrying secondary-rate-limit errors.
            time.sleep(max(0.0, 1.0 - (time.monotonic() - self.last_write)))
            self.last_write = time.monotonic()
        command = ["gh", "api", f"repos/{self.repo}/{path}", "--method", method]
        payload = None
        if data is not None:
            command += ["--input", "-"]
            payload = json.dumps(data)
        result = subprocess.run(
            command, input=payload, capture_output=True, text=True, check=False
        )
        if result.returncode:
            # No retry, including on a rate limit. gh redacts credentials itself.
            raise RuntimeError(result.stderr.strip() or "GitHub request failed")
        return json.loads(result.stdout) if result.stdout.strip() else None

    def pages(self, path):
        values = []
        for page in range(1, 101):
            separator = "&" if "?" in path else "?"
            batch = self.api(f"{path}{separator}per_page=100&page={page}")
            values.extend(batch)
            if len(batch) < 100:
                return values
        raise ValueError("pagination limit exceeded; refusing partial reconciliation")


def sync_issues(github, changes):
    parents = {}
    for change in changes:
        issue = change["issue"]
        values = {key: change[key] for key in ("title", "body", "state")}
        if issue is None:
            issue = github.api(
                "issues",
                "POST",
                {
                    "title": values["title"],
                    "body": values["body"],
                    "labels": ["api-gap"],
                },
            )
        elif any((issue.get(key) or "") != value for key, value in values.items()):
            issue = github.api(f"issues/{issue['number']}", "PATCH", values)
        area = change["area"]
        if area and change["state"] == "open":
            if area not in parents:
                parents[area] = {
                    child["id"] for child in github.pages(f"issues/{area}/sub_issues")
                }
            if issue["id"] not in parents[area]:
                github.api(
                    f"issues/{area}/sub_issues", "POST", {"sub_issue_id": issue["id"]}
                )
                parents[area].add(issue["id"])
        print(f"{change['operation']}: #{issue['number']} {change['state']}")


def strict_proposals(github, clean, strict, base_sha):
    """Open one ready, single-file PR per clean area; never push a checkout."""
    proposals = github.pages("pulls?state=open")
    for area, ids in sorted(clean.items()):
        if strict.get(area) == ids:
            continue
        stem = f"chore/api-strict-area-{area}"
        branch = stem + "-" + base_sha[:12]
        active = [
            pr
            for pr in proposals
            if (pr["head"]["ref"] == stem or pr["head"]["ref"].startswith(stem + "-"))
            and (pr["head"].get("repo") or {}).get("full_name") == github.repo
        ]
        if active:
            # Its PR coordinator owns subsequent rebases and checks.
            continue
        refs = github.api(f"git/matching-refs/heads/{branch}")
        ref = next((ref for ref in refs if ref["ref"] == f"refs/heads/{branch}"), None)
        if ref:
            # Respect a human closure; a later main commit can propose a new
            # branch. Otherwise resume an interrupted run without rewriting it.
            if github.pages(
                f"pulls?state=closed&head={github.repo.split('/')[0]}:{branch}"
            ):
                continue
        else:
            github.api(
                "git/refs", "POST", {"ref": f"refs/heads/{branch}", "sha": base_sha}
            )
        path = f"internal/api/strict_areas/{area}.json"
        updated = {area: ids}
        content = (json.dumps(updated, indent=2, sort_keys=True) + "\n").encode()
        values = {
            "branch": branch,
            "message": f"test(api): area {area} rejects contract warnings (#{area})",
            "content": base64.b64encode(content).decode(),
        }
        if area in strict:
            values["sha"] = github.api(f"contents/{path}?ref={base_sha}")["sha"]
        if ref and ref["object"]["sha"] != base_sha:
            comparison = github.api(f"compare/{base_sha}...{branch}")
            current = github.api(f"contents/{path}?ref={branch}")
            if [file["filename"] for file in comparison["files"]] != [
                path
            ] or base64.b64decode(current["content"]) != content:
                raise ValueError(
                    f"strict proposal branch {branch} has other changes; left untouched"
                )
        else:
            github.api(f"contents/{path}", "PUT", values)
        pr = github.api(
            "pulls",
            "POST",
            {
                "title": f"test(api): area {area} rejects contract warnings",
                "head": branch,
                "base": "main",
                "draft": False,
                "body": f"All registered operations in area #{area} have no generator warnings, including their named examples. Enable go-oapifly strict mode for these {len(ids)} operations so TestStrictAPIAreas rejects regressions.\n\nPart of #{area}. Generated by the warning synchronization requested in #666. PR coordination owns validation and merging.",
            },
        )
        print(f"strict area {area}: {pr['html_url']}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path)
    parser.add_argument(
        "--areas", type=Path, default=Path("tests/contract/api-areas.json")
    )
    parser.add_argument("--strict", type=Path, default=Path(STRICT_PATH))
    parser.add_argument("--repo", default="rest-mail/rest-mail-server")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    report, areas, strict = (
        json.loads(path.read_text()) for path in (args.report, args.areas, args.strict)
    )
    for path in sorted(args.strict.with_suffix("").glob("*.json")):
        for area, ids in json.loads(path.read_text()).items():
            if area in strict:
                raise ValueError(f"duplicate strict area {area}")
            strict[area] = ids
    # Fail before even reading GitHub if the export is incomplete.
    validate(report, areas)
    github = GitHub(args.repo)
    if not args.apply:
        changes, clean = plan_issues(report, areas, [])
        print(
            json.dumps(
                {
                    "issues": [
                        {
                            "operation": item["operation"],
                            "area": item["area"],
                            "title": item["title"],
                        }
                        for item in changes
                    ],
                    "clean_areas": clean,
                },
                indent=2,
            )
        )
        return
    base_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    if github.api("git/ref/heads/main")["object"]["sha"] != base_sha:
        raise ValueError("report is not from current main; no issues changed")
    issues = [
        issue
        for issue in github.pages("issues?state=all&labels=api-gap")
        if "pull_request" not in issue
    ]
    changes, clean = plan_issues(report, areas, issues)
    labels = github.pages("labels")
    if not any(label["name"] == "api-gap" for label in labels):
        github.api(
            "labels",
            "POST",
            {
                "name": "api-gap",
                "color": "d4c5f9",
                "description": "A generator-reported API contract gap",
            },
        )
    sync_issues(github, changes)
    strict_proposals(github, clean, strict, base_sha)


if __name__ == "__main__":
    main()
