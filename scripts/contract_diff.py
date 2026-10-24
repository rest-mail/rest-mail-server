#!/usr/bin/env python3
"""Compare generated API descriptions and enforce deliberate-change labels (#653).

oasdiff handles OpenAPI 3.1 normalization and comparison. REST Mail classifies
every security change as security, including a scope removed or anonymous
access added, regardless of oasdiff's compatibility severity. Removing a field
or enum value and changing a type are breaking under the owner's contract policy.
"""

import argparse
import html
import json
import os
import subprocess
import sys
from pathlib import Path


def classification(change):
    rule = change["id"]
    if rule.startswith(("api-security-", "api-global-security-")):
        return "security"
    # A warning means compatibility is uncertain; require deliberate acceptance
    # instead of calling it additive. Security has its own label above.
    if (
        change["level"] >= 2
        or rule.endswith(
            (
                "property-removed",
                "enum-value-removed",
                "header-removed",
                "parameter-removed",
            )
        )
        or ("-type-" in rule and "-media-type-" not in rule)
    ):
        return "breaking"
    return "additive"


def compare(binary, base, revision):
    result = subprocess.run(
        [
            binary,
            "changelog",
            str(base),
            str(revision),
            "--format",
            "json",
            "--allow-external-refs=false",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode:
        raise ValueError(
            result.stderr.strip() or result.stdout.strip() or "oasdiff failed"
        )
    changes = json.loads(result.stdout or "[]") or []
    if not isinstance(changes, list):
        raise TypeError("oasdiff did not return a change list")
    for change in changes:
        if (
            not isinstance(change, dict)
            or not isinstance(change.get("id"), str)
            or change.get("level") not in (1, 2, 3)
        ):
            raise ValueError("oasdiff returned an unsupported change")
        change["classification"] = classification(change)
    return sorted(
        changes,
        key=lambda c: (
            c["classification"],
            c.get("path", ""),
            c.get("operation", ""),
            c["id"],
            c.get("text", ""),
        ),
    )


def required_labels(changes):
    kinds = {c["classification"] for c in changes}
    return {
        label
        for kind, label in (
            ("breaking", "contract-change"),
            ("security", "permission-change"),
        )
        if kind in kinds
    }


def cell(value):
    return (
        html.escape(str(value))
        .replace("|", r"\|")
        .replace("\n", " ")
        .replace("`", r"\`")
    )


def report(changes, labels):
    missing = sorted(required_labels(changes) - set(labels))
    rows = ["# API contract changes", ""]
    breaking, security, additive = (
        sum(c["classification"] == kind for c in changes)
        for kind in ("breaking", "security", "additive")
    )
    rows.append(
        f"{breaking} breaking, {security} security, {additive} additive changes."
    )
    rows.extend(
        [
            "",
            "Missing labels: " + ", ".join(f"`{label}`" for label in missing) + "."
            if missing
            else "Required change labels are satisfied.",
            "",
        ]
    )
    if changes:
        rows.extend(["| Classification | Operation | Change |", "|---|---|---|"])
        for change in changes:
            operation = " ".join(
                filter(
                    None,
                    (
                        change.get("operation"),
                        change.get("path"),
                        change.get("operationId"),
                    ),
                )
            )
            rows.append(
                f"| {change['classification']} | {cell(operation or change.get('section', 'API'))} "
                f"| {cell(change.get('text', ''))} (`{cell(change['id'])}`) |"
            )
    else:
        rows.append("No consumer-visible contract change.")
    return "\n".join(rows) + "\n", missing


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base", type=Path)
    parser.add_argument("revision", type=Path)
    parser.add_argument("--oasdiff", default="oasdiff")
    parser.add_argument(
        "--event", type=Path, help="GitHub pull_request event containing current labels"
    )
    parser.add_argument(
        "--label",
        action="append",
        default=[],
        help="A deliberate-change label, for local replay",
    )
    parser.add_argument("--output", type=Path, default=Path(".cache/contract-diff"))
    args = parser.parse_args(argv)
    try:
        labels = set(args.label)
        if args.event:
            event = json.loads(args.event.read_text())
            labels.update(label["name"] for label in event["pull_request"]["labels"])
        changes = compare(args.oasdiff, args.base, args.revision)
        markdown, missing = report(changes, labels)
        args.output.mkdir(parents=True, exist_ok=True)
        (args.output / "changes.json").write_text(
            json.dumps({"changes": changes, "missing_labels": missing}, indent=2) + "\n"
        )
        (args.output / "changes.md").write_text(markdown)
        print(markdown, end="")
        summary = os.environ.get("GITHUB_STEP_SUMMARY")
        if summary:
            with open(summary, "a") as output:
                output.write(markdown)
        return 1 if missing else 0
    except (OSError, ValueError, KeyError, TypeError) as error:
        message = f"API contract comparison failed: {error}"
        print(message, file=sys.stderr)
        summary = os.environ.get("GITHUB_STEP_SUMMARY")
        if summary:
            with open(summary, "a") as output:
                output.write(
                    "# API contract comparison failed\n\n" + cell(error) + "\n"
                )
        return 2


if __name__ == "__main__":
    sys.exit(main())
