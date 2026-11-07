#!/usr/bin/env python3
"""Keep one rolling issue per daily contract tier (#626, Tiers).

contract-fuzz.yml and contract-faults.yml run this after their suite. Each fuzz
finding and each fault the run did not catch has one table row in the tier's
issue, found again by hidden tier and row markers:

- a failure adds or refreshes its row's latest failing run and replay seed
  without a nightly notification;
- a later finished run in which that transaction passes, or that fault is caught,
  removes the row. Fuzz draws different values each night, so a transaction it
  did not send this run says nothing and its row stays open;
- a run that did not finish, or that failed for a reason its report does not
  explain, has a row of its own, and removes nothing else;
- a row marked ignored by a person stays silenced, independently of other rows.

Legacy single-finding issues are imported before they are closed with a link to
the rolling issue. Legacy not-planned issues become ignored rows. An empty tier
closes and a new finding reopens it; closing the whole tier is not an ignore list.

Only scheduled runs, v* tags and runs dispatched on main count. A cancelled or
skipped suite changes nothing. An issue is public, so what is copied into one is
scrubbed, and only the seed line is read from the log: the log prints the pinned
fixture private key.

Needs gh with GH_TOKEN and issues: write. Any gh failure fails the job; the next
run repeats whatever was left, because finding the issue again needs only its
marker.
"""

from __future__ import annotations

import argparse
import hashlib
import html
import json
import os
import re
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path

PAUSE_SECONDS = 1.0  # between writes, as GitHub asks of automated clients
MAX_DETAIL = 3000
MAX_TITLE = 180
RUN_KEY = "run"

MARKER = re.compile(r"<!-- contract-tier-issue:(?P<tier>[a-z]+):(?P<key>[a-z0-9]+) -->")
TIER_MARKER = re.compile(r"<!-- contract-tier-issue:(?P<tier>[a-z]+) -->")
ROW_MARKER = re.compile(r"<!-- contract-tier-finding:(?P<tier>[a-z]+):(?P<key>[a-z0-9]+) -->")
MIGRATED = re.compile(r"<!-- contract-tier-migrated:(\d+) -->")
FIRST_SEEN = re.compile(r"^First seen: (?P<seen>.+)$", re.MULTILINE)
SEED = re.compile(r"^(?:\x1b\[[0-9;]*m)*seed: (\d+)", re.MULTILINE)
SCRUBS = (
    (
        re.compile(r"-----BEGIN [A-Z ]+-----.*?(-----END [A-Z ]+-----|\Z)", re.DOTALL),
        "[redacted PEM block]",
    ),
    (re.compile(r"(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+"), "Bearer [redacted]"),
    (
        re.compile(
            r"""(?ix)
        (password|passwd|secret|token|api[_-]?key|authorization|cookie|private[_-]?key)
        ([\w-]*)
        (["']?\s*[=:]\s*["']?)
        [^&\s"',;]+
        """
        ),
        r"\1\2\3[redacted]",
    ),
)

TIERS = {
    "fuzz": {
        "label": "contract-fuzz",
        "color": "D93F0B",
        "description": "A finding of the daily contract fuzz run (contract-fuzz.yml)",
        "workflow": "contract-fuzz.yml",
        "subject": "Contract fuzz",
        "intro": "The contract fuzz run found this. The next run that sends this transaction and "
        "sees it pass closes this issue, and a later failure reopens it. Close it as "
        "not planned to leave it closed.",
    },
    "faults": {
        "label": "contract-faults",
        "color": "D93F0B",
        "description": "A fault the daily contract fault run did not catch (contract-faults.yml)",
        "workflow": "contract-faults.yml",
        "subject": "Contract faults",
        "intro": "The contract fault run did not get what the catalogue (tests/contract/faults.yml) "
        "says it should for this fault. The next run that catches it closes this issue, "
        "and a later failure reopens it. Close it as not planned to leave it closed.",
    },
}


@dataclass
class Finding:
    key: str
    title: str
    detail: str
    replay: str = ""


@dataclass
class Report:
    findings: dict[str, Finding] = field(default_factory=dict)
    passed: set[str] = field(default_factory=set)
    problems: list[str] = field(default_factory=list)
    # Whether a key missing from the report means it no longer exists. The fault
    # catalogue is run whole; fuzz only reports what it drew this run.
    exhaustive: bool = False
    tool: str = ""


@dataclass
class Issue:
    number: int
    key: str
    state: str  # OPEN or CLOSED
    state_reason: str
    body: str


@dataclass
class Action:
    kind: str  # open, reopen, update or close
    number: int | None = None
    finding: Finding | None = None
    issue: Issue | None = None


def watched(event: str, ref: str) -> bool:
    """Runs whose result the issues track: the schedule, v* tags and main."""
    if event == "schedule":
        return True
    if event == "push":
        return ref.startswith("refs/tags/v")
    if event == "workflow_dispatch":
        return ref == "refs/heads/main"
    return False


def scrub(text: str) -> str:
    for pattern, replacement in SCRUBS:
        text = pattern.sub(replacement, text)
    return text


def fenced(text: str) -> str:
    """text as a code block no value in it can break out of."""
    text = scrub(text)[:MAX_DETAIL]
    longest = max((len(run) for run in re.findall(r"`+", text)), default=0)
    fence = "`" * max(3, longest + 1)
    return f"{fence}text\n{text}\n{fence}"


def key_of(name: str) -> str:
    return hashlib.sha256(name.encode()).hexdigest()[:16]


def fuzz_title(name: str, method: str) -> str:
    """fuzz: PATH > SUMMARY > STATUS > MEDIA · LOCATION · CLASS, shortened for a title."""
    parts = name.removeprefix("fuzz: ").split(" > ")
    variation = " · ".join(parts[-1].split(" · ")[1:]) if len(parts) > 1 else ""
    what = " ".join(part for part in (method, parts[0]) if part)
    return f"{what}: {variation}" if variation else what


def read_fuzz(reports: Path) -> Report:
    report = Report()
    try:
        data = json.loads((reports / "fuzz.json").read_text())
        results = data["results"]
        total = data["summary"]["total"]
    except (OSError, ValueError, KeyError, TypeError) as error:
        report.problems.append(f"fuzz.json could not be read: {error}")
        return report
    if not total:
        report.problems.append("fuzz.json holds no transactions")
    try:
        with (reports / "contract-fuzz.log").open(errors="replace") as log:
            seeds = [match[1] for line in log if (match := SEED.match(line))]
    except OSError:
        seeds = []
    if not seeds:
        report.problems.append(
            "contract-fuzz.log holds no seed, so a finding could not be replayed"
        )
    replay = f"chore contract:fuzz -- --seed {seeds[-1]}" if seeds else ""
    report.tool = f"vertrag {data.get('vertrag', {}).get('version', 'unknown')}"
    for result in results:
        name, status = result.get("name", ""), result.get("status", "")
        if not name or status == "skip":
            continue
        key = key_of(name)
        if status == "pass":
            report.passed.add(key)
            continue
        method = result.get("method", "")
        lines = [
            name,
            f"{method} {result.get('uri', '')}".strip(),
            *result.get("errors", []),
        ]
        report.findings.setdefault(
            key, Finding(key, fuzz_title(name, method), "\n".join(lines), replay)
        )
    return report


def read_faults(reports: Path) -> Report:
    report = Report(exhaustive=True)
    pin = (Path(__file__).parent / "vertrag-pin.yml").read_text()
    version = re.search(r"^VERTRAG_VERSION: (\S+)$", pin, re.MULTILINE)
    report.tool = f"vertrag {version[1]}" if version else "vertrag unknown"
    try:
        text = (reports / "faults.md").read_text()
    except OSError as error:
        report.problems.append(f"faults.md could not be read: {error}")
        return report
    rows = 0
    for line in text.splitlines():
        cells = line.split("|", 4)
        if (
            len(cells) < 5
            or cells[1].strip() in ("Fault", "")
            or set(cells[1].strip()) == {"-"}
        ):
            continue
        rows += 1
        fault, operation, phase = (cell.strip() for cell in cells[1:4])
        verdict = cells[4].strip().removesuffix("|").strip()
        if fault == "(none)":
            if verdict != "green":
                report.problems.append(
                    f"baseline {phase} of {operation} is not green, so no fault was judged: {verdict}"
                )
            continue
        key = key_of(fault)
        if verdict.startswith(("caught", "known gap")):
            report.passed.add(key)
        else:
            detail = f"fault: {fault}\noperation: {operation}\nphase: {phase}\nverdict: {verdict}"
            report.findings[key] = Finding(
                key, f"{fault} failed its check", detail, "chore contract:faults"
            )
    if not rows:
        report.problems.append("faults.md holds no rows")
    return report


READERS = {"fuzz": read_fuzz, "faults": read_faults}


def finished(tier: str, result: str, report: Report) -> bool:
    """Whether this run proved anything about what it did not find.

    A fuzz run never fails for a finding, so any failed job is something else. A
    fault run fails for an uncaught fault, which its report explains.
    """
    if report.problems:
        return False
    if result == "success":
        return True
    return tier == "faults" and bool(report.findings)


def run_finding(result: str, report: Report) -> Finding:
    reasons = report.problems or [
        f"the job ended {result}, and its report does not explain why"
    ]
    return Finding(RUN_KEY, "did not finish", "\n".join(reasons), "")


def plan(
    tier: str,
    result: str,
    report: Report,
    issues: dict[str, Issue],
) -> list[Action]:
    """Apply the existing finding lifecycle independently to each table row."""
    done = finished(tier, result, report)
    wanted = dict(report.findings)
    if not done:
        wanted[RUN_KEY] = run_finding(result, report)
    actions: list[Action] = []
    for key, finding in sorted(wanted.items()):
        issue = issues.get(key)
        if issue is None:
            actions.append(Action("open", finding=finding))
        elif issue.state == "OPEN":
            actions.append(Action("update", issue.number, finding, issue))
        elif issue.state_reason != "NOT_PLANNED":
            actions.append(Action("reopen", issue.number, finding, issue))
    if done:
        for key, issue in sorted(issues.items()):
            if issue.state != "OPEN" or key in wanted:
                continue
            if key == RUN_KEY or key in report.passed or report.exhaustive:
                actions.append(Action("close", issue.number, issue=issue))
    return actions


@dataclass
class Run:
    tier: str
    repo: str
    event: str
    ref: str
    commit: str
    run_id: str
    server_url: str
    tool: str = ""

    @property
    def url(self) -> str:
        return f"{self.server_url}/{self.repo}/actions/runs/{self.run_id}"

    @property
    def seen(self) -> str:
        return f"[run {self.run_id}]({self.url}), commit `{self.commit}`"


def title_of(tier: str, finding: Finding) -> str:
    title = " ".join(f"{TIERS[tier]['subject']}: {finding.title}".split())
    return title[:MAX_TITLE]


def body_of(finding: Finding, run: Run, first_seen: str | None) -> str:
    settings = TIERS[run.tier]
    lines = [
        f"<!-- contract-tier-issue:{run.tier}:{finding.key} -->",
        settings["intro"]
        if finding.key != RUN_KEY
        else f"The {settings['workflow']} run did not finish, so it proved nothing about what it did "
        "not find. The next run that finishes closes this issue.",
        "",
        f"First seen: {first_seen or run.seen}",
        "",
        "## Latest failing run",
        "",
        f"- {run.seen} ({run.event}, `{run.ref}`)",
    ]
    if run.tool:
        lines.append(f"- tool: {run.tool}")
    if finding.replay:
        lines.append(f"- replay against a topology at that commit: `{finding.replay}`")
    lines += ["", fenced(finding.detail), ""]
    return "\n".join(lines)


def gh(*args: str, input_text: str | None = None) -> str:
    out = subprocess.run(
        ["gh", *args], input=input_text, capture_output=True, text=True, check=False
    )
    if out.returncode != 0:
        raise RuntimeError(f"gh {' '.join(args[:3])}: {out.stderr.strip()}")
    return out.stdout


def known_issues(repo: str, tier: str) -> dict[str, Issue]:
    """Every issue of this tier, open or closed, by the key in its marker."""
    rows = json.loads(
        gh(
            "issue",
            "list",
            "--repo",
            repo,
            "--state",
            "all",
            "--label",
            TIERS[tier]["label"],
            "--json",
            "number,state,stateReason,body",
            "--limit",
            "1000",
        )
        or "[]"
    )
    issues: dict[str, Issue] = {}
    for row in sorted(rows, key=lambda row: row["number"]):
        body = row.get("body") or ""
        match = TIER_MARKER.search(body) or MARKER.search(body)
        if not match or match["tier"] != tier:
            continue
        key = match.groupdict().get("key") or "tier"
        if key in issues:
            key = f"legacy{row['number']}"
        issues[key] = Issue(
            row["number"],
            match.groupdict().get("key") or "tier",
            row.get("state", ""),
            row.get("stateReason") or "",
            row.get("body") or "",
        )
    return issues


def ensure_label(repo: str, tier: str) -> None:
    settings = TIERS[tier]
    names = json.loads(
        gh(
            "label",
            "list",
            "--repo",
            repo,
            "--search",
            settings["label"],
            "--json",
            "name",
        )
        or "[]"
    )
    if not any(label["name"] == settings["label"] for label in names):
        write(
            "label",
            "create",
            settings["label"],
            "--repo",
            repo,
            "--color",
            settings["color"],
            "--description",
            settings["description"],
        )


def carry_first_seen(issue: Issue | None) -> str | None:
    match = FIRST_SEEN.search(issue.body) if issue else None
    return match["seen"] if match else None


def cell(text: str) -> str:
    return (
        html.escape(scrub(text)[:MAX_DETAIL], quote=False)
        .replace("|", "&#124;")
        .replace("`", "&#96;")
        .replace("@", "&#64;")
        .replace("\n", "<br>")
    )


def row_of(finding: Finding, run: Run, first_seen: str | None = None) -> str:
    title = " ".join(finding.title.split())[:MAX_TITLE]
    return (
        f"| <!-- contract-tier-finding:{run.tier}:{finding.key} --> open "
        f"| <code>{cell(title)}</code><br><code>{cell(finding.detail)}</code> "
        f"| {first_seen or run.seen} | {run.seen} ({run.event}, `{run.ref}`) "
        f"| {cell(run.tool or 'unknown')} | <code>{cell(finding.replay)}</code> |"
    )


def read_rows(issue: Issue | None, tier: str) -> dict[str, Issue]:
    rows = {}
    for line in issue.body.splitlines() if issue else []:
        match = ROW_MARKER.search(line)
        if not match or match["tier"] != tier:
            continue
        cells = line.split("|")
        if len(cells) != 8:
            raise ValueError(f"malformed finding row: {match['key']}")
        status = cells[1].split("-->")[-1].strip().lower()
        if status not in ("open", "ignored"):
            raise ValueError(f"unknown finding status: {status}")
        ignored = status == "ignored"
        rows[match["key"]] = Issue(
            issue.number, match["key"], "CLOSED" if ignored else "OPEN",
            "NOT_PLANNED" if ignored else "", line,
        )
    return rows


def rolling_body(run: Run, rows: dict[str, Issue], migrated: set[str]) -> str:
    lines = [
        f"<!-- contract-tier-issue:{run.tier} -->",
        "One row per finding. A finished run removes only transactions it sent and passed "
        "(or faults it caught or removed from the catalogue). An unfinished or unexplained "
        "failure adds a run row and removes nothing. The issue closes when no open rows remain "
        "and reopens for new findings.",
        "",
        "To silence one finding as **not planned**, edit its Status cell from `open` to "
        "`ignored`, leaving its hidden key marker intact. Ignored rows stay here even after "
        "passes and do not keep the issue open. Change `ignored` back to `open` to track it "
        "again. Closing the whole issue does not silence individual findings.",
        "",
        "| Status | Transaction or fault | First seen | Latest failing run | Tool version | Replay at latest commit |",
        "|---|---|---|---|---|---|",
        *(row.body for _, row in sorted(rows.items())),
        "",
        *(f"<!-- contract-tier-migrated:{number} -->" for number in sorted(migrated)),
        "",
    ]
    return "\n".join(lines)


def write(*args: str, input_text: str | None = None) -> str:
    result = gh(*args, input_text=input_text)
    time.sleep(PAUSE_SECONDS)
    return result


def legacy_row(issue: Issue, run: Run) -> Issue:
    detail = re.search(
        r"^(`{3,})text\n(.*?)\n\1", issue.body, re.MULTILINE | re.DOTALL
    )
    text = detail[2] if detail else f"Imported from #{issue.number}"
    tool = re.search(r"^- tool: (.+)$", issue.body, re.MULTILINE)
    replay = re.search(
        r"^- replay against a topology at that commit: `([^`]+)`",
        issue.body, re.MULTILINE,
    )
    latest = re.search(r"^## Latest failing run\n\n- (.+)$", issue.body, re.MULTILINE)
    finding = Finding(issue.key, text.splitlines()[0], text, replay[1] if replay else "")
    cells = row_of(finding, run, carry_first_seen(issue)).split("|")
    cells[4] = f" {scrub(latest[1])} " if latest else " unknown (legacy issue) "
    cells[5] = f" {cell(tool[1])} " if tool else " unknown "
    row = "|".join(cells)
    if issue.state_reason == "NOT_PLANNED":
        row = row.replace("--> open", "--> ignored", 1)
    return Issue(issue.number, issue.key, issue.state, issue.state_reason, row)


def reconcile(
    run: Run, result: str, report: Report, issues: dict[str, Issue], dry_run: bool
) -> None:
    rolling = issues.get("tier")
    rows = read_rows(rolling, run.tier)
    migrated = set(MIGRATED.findall(rolling.body)) if rolling else set()
    legacy = [issue for issue in issues.values() if issue.key != "tier"]
    for issue in legacy:
        if str(issue.number) in migrated:
            continue
        if issue.state != "OPEN" and issue.state_reason != "NOT_PLANNED":
            continue
        if issue.key not in rows:
            rows[issue.key] = legacy_row(issue, run)
        migrated.add(str(issue.number))
    actions = plan(run.tier, result, report, rows)
    for action in actions:
        key = action.finding.key if action.finding else action.issue.key
        if action.kind == "close":
            del rows[key]
        else:
            first_seen = rows[key].body.split("|")[3].strip() if key in rows else None
            rows[key] = Issue(
                rolling.number if rolling else 0, key, "OPEN", "",
                row_of(action.finding, run, first_seen),
            )
    if not rows and not rolling and not migrated:
        return
    body = rolling_body(run, rows, migrated)
    active = any(row.state == "OPEN" for row in rows.values())
    pending = [issue for issue in legacy if issue.state == "OPEN"]
    if dry_run:
        print(f"would {'update' if rolling else 'open'} {TIERS[run.tier]['subject']} findings")
        if rolling and rolling.state == "OPEN" and not active:
            print(f"would close #{rolling.number}")
        for issue in pending:
            print(f"would migrate #{issue.number}")
        return
    if not rolling:
        ensure_label(run.repo, run.tier)
        url = write(
            "issue", "create", "--repo", run.repo, "--title",
            f"{TIERS[run.tier]['subject']} findings"[:MAX_TITLE], "--label",
            TIERS[run.tier]["label"], "--body-file", "-", input_text=body,
        ).strip()
        rolling = Issue(int(url.rsplit("/", 1)[-1]), "tier", "OPEN", "", body)
    elif body != rolling.body:
        write(
            "issue", "edit", str(rolling.number), "--repo", run.repo,
            "--body-file", "-", input_text=body,
        )
    if active and rolling.state == "CLOSED":
        write("issue", "reopen", str(rolling.number), "--repo", run.repo)
    elif not active and rolling.state == "OPEN":
        write(
            "issue", "close", str(rolling.number), "--repo", run.repo,
            "--reason", "completed", "--comment",
            f"{run.seen}: no open findings remain. A later finding reopens this issue.",
        )
    url = f"{run.server_url}/{run.repo}/issues/{rolling.number}"
    for issue in pending:
        write(
            "issue", "close", str(issue.number), "--repo", run.repo,
            "--reason", "completed", "--comment",
            f"Migrated to the rolling tier issue: {url}. Its table now tracks this finding.",
        )


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    env = os.environ
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--tier", required=True, choices=sorted(TIERS))
    parser.add_argument("--result", required=True, help="the suite job's result")
    parser.add_argument(
        "--reports",
        required=True,
        type=Path,
        help="the downloaded e2e-container-logs artifact",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="print what would change, and change nothing",
    )
    parser.add_argument("--event", default=env.get("GITHUB_EVENT_NAME", ""))
    parser.add_argument("--ref", default=env.get("GITHUB_REF", ""))
    parser.add_argument("--repo", default=env.get("GITHUB_REPOSITORY", ""))
    parser.add_argument("--commit", default=env.get("GITHUB_SHA", ""))
    parser.add_argument("--run-id", default=env.get("GITHUB_RUN_ID", ""))
    parser.add_argument(
        "--server-url", default=env.get("GITHUB_SERVER_URL", "https://github.com")
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    if not watched(args.event, args.ref):
        print(f"{args.event} on {args.ref}: not tracked, the issues are left alone")
        return 0
    if args.result not in ("success", "failure"):
        print(f"the suite ended {args.result}: says nothing either way")
        return 0
    report = READERS[args.tier](args.reports)
    run = Run(
        args.tier,
        args.repo,
        args.event,
        args.ref,
        args.commit,
        args.run_id,
        args.server_url,
        report.tool,
    )
    reconcile(run, args.result, report, known_issues(args.repo, args.tier), args.dry_run)
    return 0


if __name__ == "__main__":
    sys.exit(main())
