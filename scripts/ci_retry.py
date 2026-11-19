#!/usr/bin/env python3
"""Decide whether the CI retry bot re-runs a failed CI or E2E run (#2113).

The workflow (.github/workflows/ci-retry.yml) gathers the facts with gh and curl; this
script only decides, so every rule is pinned by scripts/tests/test_ci_retry.py without a
network. The rules, first match wins:

  1. CI_RETRY is off                       -> no retry
  2. this run attempt was already retried  -> no retry
  3. a failed job ran on the self-hosted   -> no retry (before the budget: a dead pool
     pool and no runner there is ready         stops at once instead of using attempts).
                                              A job that ran on GitHub's runners skips
                                              this rule: the pool cannot affect it.
  4. a line matches a "never" pattern      -> no retry, a human looks
  5. infra mode and no "retryable" match   -> no retry
  6. all mode                              -> any failure qualifies
  7. attempt N of M used up                -> no retry, a human looks
  otherwise                                -> retry the failed jobs

Standard library plus PyYAML only.
"""

import argparse
from dataclasses import asdict, dataclass
import json
from pathlib import Path
import re
import sys

import yaml

MODES = ("off", "infra", "all")
DEFAULT_MODE = "infra"
DEFAULT_MAX_ATTEMPTS = 5
NEEDS_HUMAN_LABEL = "ci:needs-human"
# Longest excerpt of a matched log line quoted back in a reason or comment.
EXCERPT = 200
# Hidden in every retry comment, so a second event for the same run attempt
# (a redelivery, a manual re-run of this workflow) sees that it was already retried.
MARKER_RE = re.compile(r"<!-- ci-retry: retried run=(\d+) attempt=(\d+) -->")


@dataclass(frozen=True)
class Policy:
    retryable: tuple
    never: tuple


@dataclass(frozen=True)
class Decision:
    retry: bool
    reason: str
    retryable_signature: str | None = None
    # Which rule decided: off, mode, already-retried, no-runners, never, unmatched,
    # budget or retry.
    rule: str = "retry"
    # A refusal a person must act on: a "never" line, or the attempt budget used up.
    needs_human: bool = False
    # Where the failed jobs ran, as the comment states it: self-hosted, or hosted.
    runner: str = "self-hosted"


def parse_policy(data):
    """Return the Policy in a parsed .github/ci-retry.yml, or raise ValueError."""
    if not isinstance(data, dict):
        raise ValueError("the retry policy must be a mapping with retryable and never")
    unknown = sorted(set(data) - {"retryable", "never"})
    if unknown:
        raise ValueError("unknown keys in the retry policy: " + ", ".join(unknown))
    lists = {}
    for key in ("retryable", "never"):
        value = data.get(key)
        if not isinstance(value, list) or not all(isinstance(item, str) and item.strip() for item in value):
            raise ValueError(f"{key} must be a list of non-empty strings")
        lists[key] = tuple(value)
    return Policy(retryable=lists["retryable"], never=lists["never"])


def load_policy(path):
    with open(path, encoding="utf-8") as source:
        return parse_policy(yaml.safe_load(source))


def first_match(patterns, text):
    """Return (pattern, line) for the first line containing any pattern, in line order."""
    for line in text.splitlines():
        for pattern in patterns:
            if pattern in line:
                return pattern, line.strip()[:EXCERPT]
    return None


def decide(policy, mode, attempt, max_attempts, failure_text, ready_runners,
           already_retried_head=False, failed_on_self_hosted=True):
    """Apply the retry rules, in the order of the module docstring.

    failed_on_self_hosted says whether any failed job ran on the self-hosted pool; only
    then can an empty pool stop a retry (rule 3).
    """
    runner = "self-hosted" if failed_on_self_hosted else "hosted"

    def refuse(reason, rule, signature=None, needs_human=False):
        return Decision(False, reason, signature, rule=rule, needs_human=needs_human, runner=runner)

    if mode == "off":
        return refuse("retries are off", "off")
    if mode not in MODES:
        return refuse(f'unknown CI_RETRY mode "{mode}"; expected off, infra or all', "mode")
    if already_retried_head:
        return refuse("this head was already retried", "already-retried")
    if failed_on_self_hosted and ready_runners <= 0:
        return refuse("no runner is running; retrying cannot help", "no-runners")

    never = first_match(policy.never, failure_text)
    if never:
        pattern, line = never
        return refuse(f'a line matches the never pattern "{pattern}": {line}', "never",
                      needs_human=True)

    retryable = first_match(policy.retryable, failure_text)
    signature = retryable[0] if retryable else None
    if mode == "infra" and not retryable:
        considered = ", ".join(f'"{pattern}"' for pattern in policy.retryable)
        return refuse(f"no retryable signature matched; considered {considered}", "unmatched")

    if attempt >= max_attempts:
        return refuse(f"attempt {attempt} of {max_attempts} exhausted", "budget", signature,
                      needs_human=True)

    if mode == "all":
        reason = "CI_RETRY=all retries any failure"
        if signature:
            reason += f'; retryable signature "{signature}" matched'
    else:
        reason = f'retryable signature "{signature}" matched'
    return Decision(True, reason, signature, runner=runner)


SELF_HOSTED_MARK = "antimatter"


def runner_of(job):
    """Where one failed job ran: "self-hosted", "hosted", or None when it was not reported.

    Anything naming the antimatter pool, in the runner name or the labels, is
    self-hosted; any other name or label (ubuntu-24.04, "GitHub Actions 7") is hosted.
    """
    names = [job.get("runner_name") or ""] + [str(label) for label in job.get("labels") or []]
    names = [name for name in names if name.strip()]
    if not names:
        return None
    if any(SELF_HOSTED_MARK in name.lower() for name in names):
        return "self-hosted"
    return "hosted"


def failed_runners(jobs):
    """(failed_on_self_hosted, names of jobs whose runner was not reported).

    One self-hosted failed job is enough: re-running the failed jobs re-runs it on the
    pool. A job whose runner was not reported counts as hosted, so a missing field never
    blocks the retry of a job that may well have run on GitHub's runners.
    """
    self_hosted = any(runner_of(job) == "self-hosted" for job in jobs)
    unreported = [str(job.get("name") or job.get("id")) for job in jobs if runner_of(job) is None]
    return self_hosted, unreported


def runner_line(decision, unreported):
    if decision.runner == "self-hosted":
        return "Runner: the self-hosted pool."
    line = "Runner: GitHub-hosted, so the pool check was skipped."
    if unreported:
        line += (" The runner of " + ", ".join(unreported) + " was not reported;"
                 " treated as GitHub-hosted.")
    return line


def count_ready_runners(status, org=None):
    """Count workers whose phase is Running and that are not busy.

    status is the runner gate's /status answer: a list of pools, each with its org and
    workers. With org, only that org's pools count. Raises ValueError on another shape.
    """
    pools = [status] if isinstance(status, dict) else status
    if not isinstance(pools, list):
        raise ValueError("the runner gate status is not a list of pools")
    ready = 0
    for pool in pools:
        if not isinstance(pool, dict):
            raise ValueError("a runner gate pool is not an object")
        if org and str(pool.get("org", "")).lower() != org.lower():
            continue
        workers = pool.get("workers") or []
        if not isinstance(workers, list):
            raise ValueError("a runner gate pool's workers is not a list")
        ready += sum(1 for worker in workers
                     if isinstance(worker, dict) and worker.get("phase") == "Running"
                     and worker.get("busy") is False)
    return ready


def already_retried(run_id, event_attempt, current_attempt, current_status, comments):
    """Whether the failed run attempt has been retried already.

    It has when the run moved on (a newer attempt, or not completed any more) or when a
    retry comment for this run and attempt is on the pull request.
    """
    if current_attempt > event_attempt or current_status != "completed":
        return True
    for body in comments:
        for run, attempt in MARKER_RE.findall(body or ""):
            if int(run) == run_id and int(attempt) == event_attempt:
                return True
    return False


def comment_body(decision, workflow, run_id, run_url, attempt, max_attempts, head_sha,
                 unreported=()):
    """The pull request comment for a retry or a refusal a person must act on, else None."""
    run = f"[{workflow} run {run_id}]({run_url})"
    if decision.retry:
        return (
            f"<!-- ci-retry: retried run={run_id} attempt={attempt} -->\n"
            f"CI retry bot re-ran the failed jobs of {run} on `{head_sha[:12]}`: "
            f"attempt {attempt} failed, attempt {attempt + 1} of {max_attempts} starts now.\n\n"
            f"Matched signature: `{decision.retryable_signature or 'none (CI_RETRY=all)'}`\n"
            f"Reason: {decision.reason}\n"
            f"{runner_line(decision, unreported)}"
        )
    if decision.needs_human:
        return (
            f"CI retry bot did not re-run {run} on `{head_sha[:12]}` "
            f"(attempt {attempt} of {max_attempts}).\n\n"
            f"Reason: {decision.reason}\n"
            f"{runner_line(decision, unreported)}\n\n"
            f"Labelled `{NEEDS_HUMAN_LABEL}`: a person needs to look at this failure."
        )
    return None


def read_failure_text(paths):
    parts = []
    for path in paths:
        path = Path(path)
        files = sorted(p for p in path.rglob("*") if p.is_file()) if path.is_dir() else [path]
        for file in files:
            if file.exists():
                parts.append(file.read_text(encoding="utf-8", errors="replace"))
    return "\n".join(parts)


def write_outputs(path, values):
    with open(path, "a", encoding="utf-8") as out:
        for key, value in values.items():
            out.write(f"{key}<<CI_RETRY_EOF\n{value}\nCI_RETRY_EOF\n")


def parse_int(name, value, default):
    value = (value or "").strip()
    if not value:
        return default
    try:
        return int(value)
    except ValueError:
        raise SystemExit(f"{name} must be a whole number, not {value!r}")


def command_decide(args):
    policy = load_policy(args.policy)
    mode = (args.mode or "").strip().lower() or DEFAULT_MODE
    attempt = parse_int("--attempt", args.attempt, 1)
    max_attempts = parse_int("--max-attempts", args.max_attempts, DEFAULT_MAX_ATTEMPTS)
    ready = parse_int("--ready-runners", args.ready_runners, 0)
    jobs = []
    if args.failed_jobs:
        try:
            jobs = json.loads(Path(args.failed_jobs).read_text(encoding="utf-8")) or []
        except (OSError, ValueError) as error:
            print(f"Cannot read the failed jobs ({error}); treating them as GitHub-hosted.",
                  file=sys.stderr)
        if not isinstance(jobs, list):
            jobs = []
    self_hosted, unreported = failed_runners(jobs)
    if not jobs:
        unreported = ["the failed jobs"]
    if unreported:
        print("No runner reported for " + ", ".join(unreported) + "; treating it as GitHub-hosted.",
              file=sys.stderr)
    decision = decide(policy, mode, attempt, max_attempts, read_failure_text(args.failure_text),
                      ready, already_retried_head=args.already_retried,
                      failed_on_self_hosted=self_hosted)
    body = comment_body(decision, args.workflow, args.run_id, args.run_url, attempt,
                        max_attempts, args.head_sha, unreported)
    if args.comment_out and body:
        Path(args.comment_out).write_text(body + "\n", encoding="utf-8")
    print(json.dumps(asdict(decision), indent=2))
    if args.github_output:
        write_outputs(args.github_output, {
            "retry": str(decision.retry).lower(),
            "needs-human": str(decision.needs_human).lower(),
            "rule": decision.rule,
            "reason": decision.reason,
            "signature": decision.retryable_signature or "",
            "runner": decision.runner,
            "comment": "true" if body else "false",
        })
    return 0


def command_ready_runners(args):
    try:
        status = json.loads(Path(args.status_file).read_text(encoding="utf-8"))
        ready = count_ready_runners(status, args.org)
    except (OSError, ValueError) as error:
        print(f"Cannot read the runner gate status ({error}); counting 0 ready runners.",
              file=sys.stderr)
        ready = 0
    print(ready)
    return 0


def command_already_retried(args):
    comments = json.loads(Path(args.comments_file).read_text(encoding="utf-8")) if args.comments_file else []
    retried = already_retried(args.run_id, args.event_attempt, args.current_attempt,
                              args.current_status, comments)
    print("true" if retried else "false")
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)

    run = commands.add_parser("decide", help="decide whether to re-run a failed run")
    run.add_argument("--policy", default=".github/ci-retry.yml")
    run.add_argument("--mode", default=DEFAULT_MODE)
    run.add_argument("--attempt", default="1")
    run.add_argument("--max-attempts", default=str(DEFAULT_MAX_ATTEMPTS))
    run.add_argument("--ready-runners", default="0")
    run.add_argument("--already-retried", action="store_true")
    run.add_argument("--failure-text", action="append", default=[],
                     help="a file or directory of failed job logs and annotations")
    run.add_argument("--failed-jobs",
                     help="a JSON list of the failed jobs: id, name, runner_name and labels;"
                          " without it every failed job counts as GitHub-hosted")
    run.add_argument("--workflow", default="CI")
    run.add_argument("--run-id", type=int, default=0)
    run.add_argument("--run-url", default="")
    run.add_argument("--head-sha", default="")
    run.add_argument("--comment-out")
    run.add_argument("--github-output")
    run.set_defaults(handler=command_decide)

    ready = commands.add_parser("ready-runners", help="count ready runners in a gate status")
    ready.add_argument("--status-file", required=True)
    ready.add_argument("--org")
    ready.set_defaults(handler=command_ready_runners)

    retried = commands.add_parser("already-retried", help="whether a run attempt was retried")
    retried.add_argument("--run-id", type=int, required=True)
    retried.add_argument("--event-attempt", type=int, required=True)
    retried.add_argument("--current-attempt", type=int, required=True)
    retried.add_argument("--current-status", required=True)
    retried.add_argument("--comments-file", help="a JSON list of the pull request's comment bodies")
    retried.set_defaults(handler=command_already_retried)

    args = parser.parse_args(argv)
    return args.handler(args)


if __name__ == "__main__":
    sys.exit(main())
