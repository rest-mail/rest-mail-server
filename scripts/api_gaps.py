#!/usr/bin/env python3
"""Keep API warnings together in their substantial topic issues (#666).

The input is the complete configuration-union report from TestAPIGapReport.
Dry-run is the default. Only main's api-gaps workflow uses --apply.
"""

import argparse
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


def plan_area_issues(report, areas, issues, parents, topics=None):
    """Preserve topic decisions and retire operation duplicates without claiming completion."""
    operations, owners, grouped = validate(report, areas)
    legacy, clean = plan_issues(report, areas, issues)
    topics = topics if topics is not None else {area: [area] for area in areas}
    assigned = [area for members in topics.values() for area in members]
    if (
        len(assigned) != len(set(assigned))
        or set(assigned) != set(areas)
        or any(not members for members in topics.values())
    ):
        raise ValueError("every area must belong to exactly one nonempty topic")
    topic_of = {area: topic for topic, members in topics.items() for area in members}
    parents = {str(issue["number"]): issue for issue in parents}
    if set(parents) != set(topics):
        raise ValueError("every mapped topic issue must be read before consolidation")
    related = defaultdict(list)
    for issue in issues:
        operation = operation_of(issue)
        if operation in owners:
            related[topic_of[owners[operation]]].append(issue["number"])
    changes = []
    for area, members in sorted(topics.items()):
        ids = [operation for member in members for operation in areas[member]]
        parent = parents[area]
        lines = [
            "## Consolidated API contract work",
            "",
            "This topic owns the complete operation work below. Closed operation issues were consolidated, not declared implemented. Closing those subissues does not fulfill this topic's acceptance criteria. Human decisions and broader acceptance criteria elsewhere in this issue still apply.",
            "",
        ]
        if related[area]:
            lines += [
                "Previous operation records: "
                + ", ".join(f"#{n}" for n in sorted(set(related[area]))),
                "",
            ]
        for operation in sorted(ids):
            op = operations[operation]
            warnings = grouped.get(operation, [])
            lines.append(
                f"### `{operation}` — `{op['method']} {op['path']}` (`{text(op['file'])}:{op['line']}`)"
            )
            if warnings:
                missing = []
                other = []
                for warning in warnings:
                    status = re.search(r"its (\d{3}) response", warning["message"])
                    if warning["kind"] == "example-missing" and status:
                        missing.append(status[1])
                    else:
                        other.append(warning)
                if missing:
                    lines.append(
                        "- [ ] Add named request/response pairs and prove these responses through the real route: "
                        + ", ".join(sorted(set(missing)))
                        + "."
                    )
                for warning in other:
                    lines.append(
                        f"- [ ] **{text(warning['kind'])}** — {text(warning['message'])}. {text(CLEAR.get(warning['kind'], 'Correct the registered source until the warning clears.'))}"
                    )
            else:
                lines.append(
                    "No generator warnings on main. This does not establish tier-test or strict-mode acceptance."
                )
            lines.append("")
        lines += [
            "Keep implementation, examples, authorization/limits/fault coverage and strict activation together in topic-sized PRs. Do not create one issue or PR per warning or operation.",
            "This generated section never closes or reopens the topic issue; its full acceptance criteria and owner decisions govern completion.",
        ]
        # Reuse the bounded-block writer, with an area identity distinct from
        # legacy operation identities. Existing human prose is untouched.
        body = generated_body(parent.get("body"), "area-" + area, lines)
        body = body.replace(
            f"<!-- restmail-api-gap:operation:area-{area} -->",
            f"<!-- restmail-api-gap:area:{area} -->",
        )
        if len(body.encode("utf-8")) > 60000:
            raise ValueError(f"topic #{area} would exceed the bounded issue size")
        changes.append(
            {
                "operation": "area-" + area,
                "issue": parent,
                "area": None,
                "title": parent["title"],
                "body": body,
                "state": parent["state"],
            }
        )
    # Existing operation records keep their entire original requirements and
    # comments. A closure means moved-to-topic, never work completed.
    for change in legacy:
        issue = change["issue"]
        if issue is None or issue["state"] == "closed":
            continue
        owner = owners.get(change["operation"])
        if owner is None:
            continue
        area = topic_of[owner]
        footer = f"<!-- restmail-api-gap:consolidated:{area} -->\nConsolidated into #{area}. Requirements and evidence remain here for reference; this closure does not mean the work is complete. Track remaining work and related implementation together in the topic issue."
        body = issue.get("body") or ""
        if f"<!-- restmail-api-gap:consolidated:{area} -->" not in body:
            body += "\n\n" + footer + "\n"
        changes.append(
            {
                "operation": change["operation"],
                "issue": issue,
                "area": None,
                "title": issue["title"],
                "body": body,
                "state": "closed",
                "state_reason": "not_planned",
            }
        )
    return changes, clean


class GitHub:
    def __init__(self, repo):
        self.repo = repo
        self.last_write = 0.0

    def api(self, path, method="GET", data=None):
        if method != "GET":
            # Consolidation updates topics and closes legacy duplicates. Pace
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
        if change.get("state_reason"):
            values["state_reason"] = change["state_reason"]
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
    """Hand off all pending strict activation together; never fan out micro PRs."""
    pending = {
        area: ids for area, ids in sorted(clean.items()) if strict.get(area) != ids
    }
    print(
        json.dumps(
            {
                "source_main": base_sha,
                "strict_activation_topics": pending,
                "instruction": "Enable these areas together in a substantial topic PR; area issues remain open until their full acceptance criteria pass.",
            },
            sort_keys=True,
        )
    )
    return pending


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path)
    parser.add_argument(
        "--areas", type=Path, default=Path("tests/contract/api-areas.json")
    )
    parser.add_argument("--strict", type=Path, default=Path(STRICT_PATH))
    parser.add_argument(
        "--topics", type=Path, default=Path("tests/contract/api-topics.json")
    )
    parser.add_argument("--repo", default="rest-mail/rest-mail-server")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    report, areas, strict = (
        json.loads(path.read_text()) for path in (args.report, args.areas, args.strict)
    )
    topics = json.loads(args.topics.read_text())
    for path in sorted(args.strict.with_suffix("").glob("*.json")):
        for area, ids in json.loads(path.read_text()).items():
            if area in strict:
                raise ValueError(f"duplicate strict area {area}")
            strict[area] = ids
    # Fail before even reading GitHub if the export is incomplete.
    validate(report, areas)
    github = GitHub(args.repo)
    if not args.apply:
        parents = [
            {
                "number": int(area),
                "title": f"API contract topic #{area}",
                "body": "",
                "state": "open",
            }
            for area in topics
        ]
        changes, clean = plan_area_issues(report, areas, [], parents, topics)
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
    parents = [github.api(f"issues/{area}") for area in sorted(topics)]
    changes, clean = plan_area_issues(report, areas, issues, parents, topics)
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
