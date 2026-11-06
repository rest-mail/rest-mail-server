#!/usr/bin/env python3
"""Fail-fast local CI on the working tree, a PR over the merge queue, or the queue itself.

Exit status: 0 passed, 1 failed or could not run, 3 ended WARN, 4 no verdict because
a time limit was reached. Only 0 counts as passed for enqueue.
"""

import argparse
import fcntl
import hashlib
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import uuid
from contextlib import contextmanager
from pathlib import Path
from typing import NamedTuple

ROOT = Path(__file__).resolve().parents[1]
STEP_NAMES = (
    "Python environment",
    "CI coverage",
    "Go toolchain",
    "scripts/tests",
    "tests/contract",
    "tests/load",
    "Go dependencies",
    "go vet",
    "go build",
    "golangci-lint",
    "Package selection",
    "Loopback aliases",
    "Chart publication",
    "PostgreSQL",
    "Go tests",
    "IMAP module",
    "Empty database fixtures",
    "Contract fault switch",
    "Vertrag examples",
)
# These need the running topology, which only the E2E job brings up.
TOPOLOGY_PACKAGES = ("/tests/e2e",)
# CI runs these against an empty database of their own, in a step of their own.
FIXTURE_PACKAGES = ("/tests/contract/fixtures", "/tests/contract/fixtureplan")
FAILURE_REPORT_LINES = 400
TIMEOUT_REPORT_LINES = 30
# CI splits internal/api four ways on runners it has to itself. A shared Mac at a
# load of 300 cannot finish a quarter of the package inside CI's per-shard limit
# (#1941), so the limit stays and each shard carries less. Tests still run once.
RACE_SHARDS = 8
# CI allows 10m. TestIMAPChecklist is one test and alone took longer than that at
# a load of 300, so shards cannot help it; only the local limit is longer.
IMAP_TIMEOUT = "30m"
# All the *WithVertrag tests took about 32 minutes at a load of 50 to 85, and a 20-minute
# limit cut them off. Each of the three shards carries a third of them, so this leaves
# room for much more load than that without calling a healthy run anything but healthy.
VERTRAG_TIMEOUT = "60m"
TIMEOUT_MARK = "panic: test timed out"
HANDLERS = "internal/api/handlers"
# tests/load/loadgen's TestSourcesAreWhatTheServerSees dials from these. Linux routes
# all of 127/8 to the loopback interface; macOS answers only 127.0.0.1 until each one
# is aliased, and a reboot drops the aliases (#1942).
LOADGEN = "/tests/load/loadgen"
LOOPBACK_ALIASES = ("127.0.1.1", "127.0.1.2", "127.0.1.3")
# Packages whose tests read the source of packages they do not import, so selecting
# changed packages and their importers never selects them: CI failed #1791 on
# internal/testguard's TestNoFreePortProbes after its local gate passed (#1942).
# They run whatever changed. A new test that walks the tree belongs here too.
GUARD_PACKAGES = (
    # TestNoFreePortProbes: every .go file in the repository.
    "internal/testguard",
    # TestEveryStoredMessageNamesItsThread: every message literal under internal and cmd.
    "internal/db/repositories",
    # TestRotateCoversEveryEncryptedColumn: every encrypt call under cmd and internal.
    "cmd/rotate-key",
    # Its catalogue's patches must apply to other packages' source, and it reads
    # internal/api's tests.
    "internal/faultinject",
)

EXIT_PASSED = 0
EXIT_FAILED = 1
EXIT_WARNED = 3
EXIT_NO_VERDICT = 4
VERDICTS = {
    EXIT_PASSED: "Verdict: PASSED.",
    EXIT_FAILED: "Verdict: FAILED or could not run; do not push.",
    EXIT_WARNED: (
        "Verdict: WARN, not passed for enqueue. The checks ran and no test failed, "
        "but a warning above says CI may not agree."
    ),
    EXIT_NO_VERDICT: (
        "Verdict: NONE. A time limit was reached, which is neither a pass nor a failure "
        "of the test it cut off; run it again."
    ),
}
# The queue's merge commits never reach a branch; they only give each prefix a tree.
STACK_IDENTITY = {
    "GIT_AUTHOR_NAME": "chore ci:local",
    "GIT_AUTHOR_EMAIL": "ci-local@localhost",
    "GIT_COMMITTER_NAME": "chore ci:local",
    "GIT_COMMITTER_EMAIL": "ci-local@localhost",
}
QUEUE_QUERY = """
query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    mergeQueue(branch: "main") {
      entries(first: 100) {
        nodes { position pullRequest { number headRefOid } }
      }
    }
  }
}
"""
UNITTEST_RULE = "=" * 70
UNITTEST_FAILURE = re.compile(r"^(?:FAIL|ERROR): (\S+) \(([^)]+)\)")


class CouldNotRun(RuntimeError):
    pass


class NoVerdict(CouldNotRun):
    """A time limit ended the step, so it says nothing about the code."""


class Entry(NamedTuple):
    """Something merged ahead of the head: a merge queue entry, or a named commit."""

    label: str
    commit: str
    number: int | None = None


class Prefix(NamedTuple):
    """The base with the entries up to one of them merged, in order."""

    entry: Entry | None
    commit: str
    name: str


class Stack(NamedTuple):
    prefixes: list
    # (entry, what it was merged over, conflicting paths), left out of later prefixes.
    conflicts: list

    @property
    def tip(self):
        return self.prefixes[-1].commit


def tee(args, root, env, log):
    """Prints a command's output as it comes and keeps all of it in a file.

    The command writes to the file rather than a pipe, so a test's orphan that keeps
    its output open cannot hold this up after the command has exited.
    """

    def copy(reader):
        text = reader.read().decode(errors="replace")
        if text:
            print(text, end="", flush=True)

    with log.open("wb") as written, log.open("rb") as reader:
        process = subprocess.Popen(
            args, cwd=root, env=env, stdout=written, stderr=subprocess.STDOUT
        )
        try:
            while process.poll() is None:
                copy(reader)
                time.sleep(0.5)
            copy(reader)
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
    if process.returncode:
        raise subprocess.CalledProcessError(process.returncode, args)


def command(args, root=ROOT, env=None, capture=False, log=None):
    print("+ " + " ".join(str(arg) for arg in args), flush=True)
    try:
        if log:
            return tee([str(arg) for arg in args], root, env, log)
        return subprocess.run(
            [str(arg) for arg in args],
            cwd=root,
            env=env,
            check=True,
            text=True,
            stdout=subprocess.PIPE if capture else None,
        ).stdout
    except FileNotFoundError as error:
        raise CouldNotRun(f"install {args[0]} and rerun (command not found)") from error


def unwind_on_termination():
    """Let SIGTERM and SIGHUP unwind the stack, so a killed run still cleans up."""

    def interrupt(number, frame):
        raise KeyboardInterrupt

    for number in (signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, interrupt)


@contextmanager
def cache_lock(cache):
    """Serialise installs into the cache, which every worktree on a machine shares."""
    with (cache / ".lock").open("w") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)
        yield


def git(root, *args):
    return command(["git", *args], root, capture=True).strip()


def run_steps(steps):
    """Runs steps until one stops the run, and returns an EXIT_ status."""
    started = time.monotonic()
    results = []
    stopped = None
    for name, action in steps:
        if stopped:
            results.append(
                ("COULD-NOT-RUN", name, "not attempted after earlier failure")
            )
            continue
        print(f"\n== {name}", flush=True)
        step_started = time.monotonic()
        try:
            detail = action()
            status = "PASS"
            if isinstance(detail, str) and detail.startswith("NOT REQUIRED"):
                status = "NOT REQUIRED"
            elif isinstance(detail, str) and detail.startswith("WARNING"):
                status = "WARN"
        except NoVerdict as error:
            status, detail, stopped = "NO VERDICT", str(error), EXIT_NO_VERDICT
        except CouldNotRun as error:
            status, detail, stopped = "COULD-NOT-RUN", str(error), EXIT_FAILED
        except (
            RuntimeError,
            OSError,
            ValueError,
            KeyError,
            TypeError,
            subprocess.CalledProcessError,
        ) as error:
            status, detail, stopped = "FAIL", str(error), EXIT_FAILED
        results.append(
            (status, name, f"{time.monotonic() - step_started:.1f}s; {detail or ''}")
        )
        print(f"{status}: {name} ({results[-1][2]})", flush=True)
    print("\nLocal CI summary (fail-fast):", flush=True)
    for status, name, detail in results:
        print(f"  {status}: {name} — {detail}", flush=True)
    print(f"Elapsed: {time.monotonic() - started:.1f}s", flush=True)
    if stopped:
        return stopped
    return EXIT_WARNED if any(row[0] == "WARN" for row in results) else EXIT_PASSED


def carry_up(code):
    """Turns the status of a run of steps into the outcome of the step that ran them."""
    if code == EXIT_FAILED:
        raise RuntimeError("local checks failed; do not push")
    if code == EXIT_NO_VERDICT:
        raise NoVerdict("a time limit was reached; see above")
    if code == EXIT_WARNED:
        return "WARNING: a step ended WARN; see above"
    return None


def check_base(root, allow_stale):
    result = subprocess.run(
        ["git", "merge-base", "--is-ancestor", "origin/main", "HEAD"],
        cwd=root,
        check=False,
    )
    if result.returncode == 1:
        message = "STALE BASE: HEAD does not contain newest origin/main; rebase or use --merge-pr/--merge-ref."
        if not allow_stale:
            raise RuntimeError(
                message + " Only --allow-stale explicitly overrides this refusal."
            )
        print(
            "WARNING: " + message + " --allow-stale was explicitly supplied.",
            flush=True,
        )
    elif result.returncode:
        raise CouldNotRun("cannot establish ancestry of origin/main and HEAD")


def changed_files(root):
    # From the merge base and not origin/main itself: worktrees share that ref, so
    # another worker's fetch can move it mid-run, and its changes are not this head's.
    base = git(root, "merge-base", "origin/main", "HEAD")
    tracked = command(
        ["git", "diff", "--name-only", "--no-renames", "-z", base, "--"],
        root,
        capture=True,
    )
    untracked = command(
        ["git", "ls-files", "--others", "--exclude-standard", "-z"], root, capture=True
    )
    return sorted(set((tracked + untracked).split("\0")) - {""})


def json_stream(text):
    decoder = json.JSONDecoder()
    values = []
    while text.strip():
        value, offset = decoder.raw_decode(text.lstrip())
        values.append(value)
        text = text.lstrip()[offset:]
    return values


def parse_events(text):
    """Read test2json output, keeping any line that is not an event."""
    events, stray = [], []
    for line in text.splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except ValueError:
            stray.append(line)
            continue
        if isinstance(event, dict):
            events.append(event)
        else:
            stray.append(line)
    return events, stray


def failure_report(events):
    """Name the failed tests, with their output; without one, the package's output."""
    failed = sorted(
        {
            event["Test"]
            for event in events
            if event.get("Action") == "fail" and event.get("Test")
        }
    )
    shown = set(failed)
    # Decided once: asked per line, a large log with one failed test took 36
    # minutes of CPU and printed nothing (#1942).
    only_failed = bool(failed) and not timed_out(events)
    lines = [
        event.get("Output", "").rstrip("\n")
        for event in events
        if event.get("Action") == "output"
        and (event.get("Test") in shown if only_failed else True)
    ]
    for index, line in enumerate(lines):
        if "panic: test timed out" in line:
            # What was running comes first; the goroutine dump after it is long.
            return failed, "\n".join(lines[index : index + TIMEOUT_REPORT_LINES])
    return failed, "\n".join(lines[-FAILURE_REPORT_LINES:])


def plan_shards(names, seconds, count):
    """Splits tests into at most count shards that should take about as long.

    The sorted round robin CI uses put five of internal/api's slowest tests in one
    of eight shards: 250 s against a mean of 154 s, so at the load several gates
    bring, that shard alone passed the 20 minute limit (#1942). Here the longest
    test goes first, each to the shard with the least time so far, so no shard
    passes the mean by more than its longest test. A test with no time on record
    counts as the median of those with one; with none on record every test weighs
    the same and the tests are dealt out in turn, as CI deals them.
    """
    known = sorted(seconds[name] for name in names if name in seconds)
    default = known[len(known) // 2] if known else 1.0
    weight = {name: seconds.get(name, default) for name in names}
    shards = [[] for _ in range(min(count, len(names)))]
    totals = [0.0] * len(shards)
    for name in sorted(names, key=lambda name: -weight[name]):
        lightest = totals.index(min(totals))
        shards[lightest].append(name)
        totals[lightest] += weight[name]
    return [sorted(shard) for shard in shards]


def timed_out(events):
    return any(TIMEOUT_MARK in event.get("Output", "") for event in events)


def split_at_limit(events):
    """Tells tests that failed before a shard's limit from those the limit cut off."""
    before, after = set(), set()
    reached = False
    for event in events:
        if TIMEOUT_MARK in event.get("Output", ""):
            reached = True
        elif event.get("Action") == "fail" and event.get("Test"):
            (after if reached else before).add(event["Test"])
    return sorted(before), sorted(after)


def unittest_failures(output):
    """The ids of what a unittest run reports failing, each once, in order.

    A report line follows a rule of equals signs. The rule keeps out what a test
    printed that merely looks like one.
    """
    failures = []
    previous = ""
    for line in output.splitlines():
        match = UNITTEST_FAILURE.match(line) if previous == UNITTEST_RULE else None
        previous = line
        if not match:
            continue
        name, identifier = match.groups()
        # A module that cannot import is reported under the loader, by its own name.
        test = name if "_FailedTest" in identifier else identifier
        if test not in failures:
            failures.append(test)
    return failures


def test_exists(directory, test):
    """Whether a tree has this test: a test a later entry brings cannot fail without it."""
    parts = test.split(".")
    for length in range(len(parts), 0, -1):
        module = directory.joinpath(*parts[:length]).with_suffix(".py")
        if module.exists():
            source = module.read_text(errors="replace")
            return all(
                re.search(
                    rf"^\s*(?:class|(?:async )?def) {re.escape(part)}\b",
                    source,
                    re.MULTILINE,
                )
                for part in parts[length:]
            )
    return False


def still_failing(requested, reported):
    """Which requested tests a run reports, counting a report for their class or module."""
    return [
        test
        for test in requested
        if any(test == name or test.startswith(name + ".") for name in reported)
    ]


def queue_entries(reply):
    queue = reply["data"]["repository"]["mergeQueue"]
    if queue is None:
        raise CouldNotRun("this repository has no merge queue on main")
    nodes = sorted(queue["entries"]["nodes"], key=lambda node: node["position"])
    return [
        Entry(
            "#" + str(node["pullRequest"]["number"]),
            node["pullRequest"]["headRefOid"],
            node["pullRequest"]["number"],
        )
        for node in nodes
    ]


def read_merge_queue(root):
    """The queue, in order. Not knowing it stops the run: it is not an empty queue."""
    try:
        reply = subprocess.run(
            [
                "gh",
                "api",
                "graphql",
                "-f",
                "query=" + QUEUE_QUERY,
                "-F",
                "owner={owner}",
                "-F",
                "name={repo}",
            ],
            cwd=root,
            check=True,
            text=True,
            capture_output=True,
        ).stdout
    except FileNotFoundError as error:
        raise CouldNotRun(
            "install gh; the merge queue cannot be read without it"
        ) from error
    except subprocess.CalledProcessError as error:
        raise CouldNotRun(f"cannot read the merge queue: {error.stderr}") from error
    return queue_entries(json.loads(reply))


def entries_ahead(entries, number):
    """A queued PR goes over what is ahead of it; any other PR goes over all of it."""
    for index, entry in enumerate(entries):
        if entry.number == number:
            return entries[:index]
    return entries


def fetch_commits(root, entries):
    missing = [
        entry.commit
        for entry in entries
        if subprocess.run(
            ["git", "cat-file", "-e", entry.commit + "^{commit}"],
            cwd=root,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        ).returncode
    ]
    if not missing:
        return
    try:
        git(root, "fetch", "--no-tags", "--no-write-fetch-head", "origin", *missing)
    except subprocess.CalledProcessError as error:
        raise CouldNotRun(
            f"cannot fetch the queue's heads {missing}; check the connection"
        ) from error


def merge_commit(root, ours, theirs):
    """Merges two commits with no checkout and no hooks: (commit, None) or (None, paths)."""
    merged = subprocess.run(
        ["git", "merge-tree", "--write-tree", "--name-only", "--no-messages"]
        + [ours, theirs],
        cwd=root,
        text=True,
        capture_output=True,
        check=False,
    )
    if merged.returncode == 1:
        return None, sorted(set(merged.stdout.splitlines()[1:]) - {""})
    if merged.returncode:
        raise RuntimeError(f"git merge-tree {ours} {theirs}: {merged.stderr.strip()}")
    commit = subprocess.run(
        ["git", "commit-tree", merged.stdout.split()[0]]
        + ["-p", ours, "-p", theirs, "-m", "chore ci:local stack"],
        cwd=root,
        env=dict(os.environ, **STACK_IDENTITY),
        text=True,
        capture_output=True,
        check=True,
    )
    return commit.stdout.strip(), None


def build_stack(root, base, entries, base_name="main"):
    """Merges entries over base in order. One that conflicts is left out, as the queue would."""
    prefix = Prefix(
        None, git(root, "rev-parse", "--verify", base + "^{commit}"), base_name
    )
    stack = Stack([prefix], [])
    for entry in entries:
        ahead = stack.prefixes[-1]
        commit, paths = merge_commit(root, ahead.commit, entry.commit)
        if commit:
            stack.prefixes.append(
                Prefix(entry, commit, f"{ahead.name} + {entry.label}")
            )
        else:
            stack.conflicts.append((entry, ahead.name, paths))
    return stack


def merge_over_stack(root, stack, head, label):
    """The head over the whole stack. A conflict names the entry that first causes it."""
    commit, paths = merge_commit(root, stack.tip, head)
    if commit:
        return commit
    culprit = stack.prefixes[0].name
    for prefix in stack.prefixes:
        _, paths = merge_commit(root, prefix.commit, head)
        if paths:
            culprit = prefix.entry.label if prefix.entry else prefix.name
            break
    raise RuntimeError(f"{label} conflicts with {culprit}: {', '.join(paths)}")


def run_parent(scratch):
    """Inside the wrapper's own directory when there is one, so a SIGKILL takes it too."""
    return Path(os.environ.get("RESTMAIL_SCRATCH_RUN") or scratch)


def default_scratch():
    return Path(
        os.environ.get("RESTMAIL_SCRATCH") or Path.home() / "tmp/rest-mail-ci-local"
    )


def loop_label(scratch=None):
    """The id this gate's containers carry, in their rest-mail-loop label and in
    their restmail-<id>-postgres name, so that gates sharing the machine can be
    told apart (#1942): RESTMAIL_LOOP_LABEL when it is set, otherwise the name of
    the scratch directory. Docker names allow letters, digits, _ . and -, and so
    does the id."""
    value = os.environ.get("RESTMAIL_LOOP_LABEL") or Path(
        scratch or default_scratch()
    ).name
    return re.sub(r"[^A-Za-z0-9_.-]+", "-", value).strip("-_.") or "local"


def select_packages(packages, files, root):
    paths = {package["ImportPath"] for package in packages}
    if any(
        name in {"go.mod", "go.sum", "go.work", "go.work.sum"}
        or name.startswith("third_party/")
        for name in files
    ):
        return sorted(paths)
    selected = set()
    for name in files:
        owners = [
            package
            for package in packages
            if (root / name).is_relative_to(Path(package["Dir"]))
            and (
                Path(package["Dir"]) != root
                or (
                    (root / name).parent == root
                    and Path(name).suffix
                    in {
                        ".go",
                        ".c",
                        ".h",
                        ".s",
                        ".cc",
                        ".cpp",
                        ".m",
                        ".f",
                        ".syso",
                        ".swig",
                        ".swigcxx",
                    }
                )
                or name.startswith("testdata/")
                or name in package.get("EmbedFiles", [])
                or name in package.get("TestEmbedFiles", [])
                or name in package.get("XTestEmbedFiles", [])
            )
        ]
        if owners:
            selected.add(
                max(owners, key=lambda package: len(package["Dir"]))["ImportPath"]
            )
        elif name.endswith(".go"):
            return sorted(paths)
    while True:
        added = {
            package["ImportPath"]
            for package in packages
            if selected.intersection(
                package.get("Imports", [])
                + package.get("TestImports", [])
                + package.get("XTestImports", [])
            )
        }
        if added <= selected:
            return sorted(selected)
        selected.update(added)


def step_digest(step):
    return hashlib.sha256(
        json.dumps(step, sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest()


def unbindable_loopbacks(addresses=LOOPBACK_ALIASES, open_socket=socket.socket):
    """The addresses a TCP socket cannot bind on this machine, in order."""
    missing = []
    for address in addresses:
        with open_socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
            try:
                probe.bind((address, 0))
            except OSError:
                missing.append(address)
    return missing


def check_coverage(workflow, coverage):
    for job in ("lint", "test", "test-api"):
        observed = set()
        for step in workflow["jobs"][job]["steps"]:
            name = step.get("name", step.get("uses", "unnamed step"))
            entry = coverage[job].get(name)
            if (
                name in observed
                or not entry
                or entry.get("sha256") != step_digest(step)
            ):
                raise RuntimeError(
                    f"CI drift: {job}/{name}; cover the step locally or record a named exception with a reason in scripts/ci-local-coverage.json"
                )
            observed.add(name)
            if "exception" in entry:
                if not entry["exception"] or "local" in entry:
                    raise RuntimeError(
                        f"CI exception needs a reason and no local mapping: {job}/{name}"
                    )
                print(f"EXCEPTION: {job}/{name}: {entry['exception']}")
            elif entry.get("local") not in STEP_NAMES:
                raise RuntimeError(f"Unknown local coverage: {job}/{name}")
        if observed != set(coverage[job]):
            raise RuntimeError(
                f"CI drift: removed/renamed steps in {job}: {set(coverage[job]) - observed}"
            )


@contextmanager
def detached_worktree(root, commit, parent):
    work = Path(tempfile.mkdtemp(prefix="merge-", dir=parent))
    added = False
    try:
        git(root, "worktree", "add", "--detach", str(work), commit)
        added = True
        yield work
    finally:
        if added:
            git(root, "worktree", "remove", "--force", str(work))
        else:
            shutil.rmtree(work)


@contextmanager
def merged_worktree(root, head, parent, stack=None, label=None):
    stack = stack or build_stack(root, "origin/main", ())
    commit = merge_over_stack(root, stack, head, label or head)
    with detached_worktree(root, commit, parent) as work:
        yield work


def verify_vertrag(events, names):
    passed = {event.get("Test") for event in events if event.get("Action") == "pass"}
    skipped = {event.get("Test") for event in events if event.get("Action") == "skip"}
    if not names or skipped & set(names) or set(names) - passed:
        raise RuntimeError(
            f"Vertrag did not prove every selected test: skipped={sorted(skipped & set(names))}, missing={sorted(set(names) - passed)}"
        )
    for name in sorted(skipped):
        print(f"Subtest skipped by its own test: {name}")


class Gate:
    def __init__(self, root, cache, work, all_packages, loop=None):
        self.root, self.cache, self.work = root, cache, work
        self.loop = loop or loop_label()
        self.all_packages = all_packages
        self.python = cache / "venv/bin/python"
        self.env = dict(os.environ)
        self.env.update(
            {"TMPDIR": str(work), "PYTHONDONTWRITEBYTECODE": "1", "ENVIRONMENT": "test"}
        )
        self.env.setdefault("GOMAXPROCS", str(min(4, os.cpu_count() or 1)))
        self.env["PATH"] = str(self.python.parent) + os.pathsep + self.env["PATH"]
        self.env.pop("RESTMAIL_EXAMPLE_PROOFS", None)
        self.env.pop("RESTMAIL_VERTRAG_BINARY", None)
        self.packages = []
        self.guards = []
        self.files = []
        self.api_changed = False
        # This run's own: a gate finds and removes only the container it made,
        # by its id, never by a name pattern or a label other gates share (#1942).
        self.run_id = uuid.uuid4().hex[:10]
        self.container = None
        self.container_id = None
        self.image = None
        self.created_image = False
        self.workflow = None
        self.limited = []
        self.vet_output = []

    def warning(self):
        if not self.limited:
            return None
        return (
            "WARNING: reached a shard's time limit, then passed in a new process; "
            "CI may reach its limit on these too (#1942): " + "; ".join(self.limited)
        )

    def run(self, args, **kwargs):
        return command(
            args, kwargs.pop("root", self.root), kwargs.pop("env", self.env), **kwargs
        )

    def python_environment(self):
        # The system Python often has no PyYAML, and the Python suites then fail with
        # import errors that say nothing about the code. So they run from this venv,
        # and a venv that cannot have it stops the gate here under its own name.
        try:
            with cache_lock(self.cache):
                if not self.python.exists():
                    self.run([sys.executable, "-m", "venv", self.cache / "venv"])
                available = subprocess.run(
                    [str(self.python), "-c", "import yaml"],
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    check=False,
                )
                if available.returncode:
                    self.run([self.python, "-m", "pip", "install", "PyYAML==6.0.3"])
            self.run(
                [self.python, "-c", "import yaml; print('PyYAML', yaml.__version__)"]
            )
        except (subprocess.CalledProcessError, OSError) as error:
            raise CouldNotRun(
                f"PyYAML is not available in {self.cache / 'venv'}: {error}. "
                "scripts/tests and tests/contract cannot run without it; repair with: "
                f"{self.python} -m pip install PyYAML==6.0.3, or delete that venv and rerun"
            ) from error

    def load_workflow(self):
        self.workflow = json.loads(
            self.run(
                [
                    self.python,
                    "-c",
                    "import json,sys,yaml; print(json.dumps(yaml.safe_load(open(sys.argv[1]))))",
                    self.root / ".github/workflows/ci.yml",
                ],
                capture=True,
            )
        )

    def coverage(self):
        self.load_workflow()
        check_coverage(
            self.workflow,
            json.loads((ROOT / "scripts/ci-local-coverage.json").read_text()),
        )

    def go_toolchain(self):
        expected = next(
            step["with"]["go-version"]
            for step in self.workflow["jobs"]["lint"]["steps"]
            if step["name"] == "Set up Go"
        )
        actual = self.run(["go", "env", "GOVERSION"], capture=True).strip()
        if actual != "go" + expected:
            raise CouldNotRun(
                f"CI needs go{expected}, found {actual}; install that Go toolchain before rerunning"
            )
        self.env["GOTOOLCHAIN"] = "go" + expected

    def python_suite(self, directory):
        self.run(
            [
                self.python,
                "-B",
                "-m",
                "unittest",
                "discover",
                "-s",
                directory,
                "-p",
                "test_*.py",
            ]
        )

    def log_file(self, label):
        return self.work / f"{label}-{uuid.uuid4().hex[:8]}.log"

    def run_go_test(self, args, **kwargs):
        """`go test`, where reaching its -timeout says nothing about the code."""
        log = self.log_file("go-test")
        try:
            self.run(args, log=log, **kwargs)
        except subprocess.CalledProcessError:
            output = log.read_text(errors="replace") if log.exists() else ""
            if TIMEOUT_MARK not in output or "--- FAIL:" in output:
                raise
            running = re.findall(r"^\s+(\w+(?:/\S+)?) \(\S+\)$", output, re.MULTILINE)
            raise NoVerdict(
                f"{' '.join(str(arg) for arg in args)} reached its time limit at load "
                f"{os.getloadavg()[0]:.0f}; running then: {running}"
            ) from None

    def checkout(self, commit):
        git(self.root, "checkout", "--force", "--detach", "--quiet", commit)
        git(self.root, "clean", "-fdq")

    def vet_failures(self, tests=None):
        """Whether go vet fails here, as a list of one in the shape suites report."""
        log = self.log_file("vet")
        try:
            self.run(["go", "vet", "./..."], log=log)
        except subprocess.CalledProcessError:
            self.vet_output = log.read_text(errors="replace").splitlines()[:8]
            return ["go vet ./..."]
        return []

    def suite_failures(self, directory, tests=None):
        """Runs a Python suite, or only some of its tests, and names what fails."""
        log = self.log_file("suite")
        arguments = tests or ["discover", "-s", directory, "-p", "test_*.py"]
        # `discover` puts the directory on the path itself; naming tests does not.
        env = dict(self.env, PYTHONPATH=str(self.root / directory))
        try:
            self.run(
                [self.python, "-B", "-m", "unittest", *arguments], log=log, env=env
            )
        except subprocess.CalledProcessError as error:
            failures = unittest_failures(log.read_text(errors="replace"))
            if not failures:
                raise RuntimeError(
                    f"{directory} exited {error.returncode} and named no failing test"
                ) from error
            return failures
        return []

    def alone_over_base(self, stack, entry, directory, check, test):
        """How one test does with only its entry over the base, to tell what it needs."""
        commit, paths = merge_commit(self.root, stack.prefixes[0].commit, entry.commit)
        where = f"{entry.label} alone over {stack.prefixes[0].name}"
        if not commit:
            return f"conflicts with {where}: {', '.join(paths)}"
        self.checkout(commit)
        if directory and not test_exists(self.root / directory, test):
            return f"does not exist with {where}"
        return f"fails with {where}" if check([test]) else f"passes with {where}"

    def name_entries(self, name, directory, check, failing, stack):
        """Repeats what failed on each prefix, from the shortest, to find what started it."""
        findings = []
        remaining = list(failing)
        passed_on = {test: None for test in remaining}
        for prefix in stack.prefixes:
            if not remaining:
                break
            self.checkout(prefix.commit)
            present = [
                test
                for test in remaining
                if not directory or test_exists(self.root / directory, test)
            ]
            failed_here = still_failing(present, check(present)) if present else []
            for test in failed_here:
                remaining.remove(test)
                findings.append(
                    self.blame(name, directory, check, test, prefix, stack, passed_on)
                )
            for test in present:
                if test not in failed_here:
                    passed_on[test] = prefix.name
        for test in remaining:
            findings.append(
                f"{name}: {test}: did not repeat on any prefix, including the whole stack; "
                "the first run's failure stands and no entry is named"
            )
        return findings

    def blame(self, name, directory, check, test, prefix, stack, passed_on):
        subject = f"{name}: {test}" if directory else name
        if prefix.entry is None:
            return f"{subject}: fails on {prefix.name} itself, with no queue entry"
        finding = f"{subject}: first fails with {prefix.entry.label} ({prefix.name})"
        if passed_on[test]:
            finding += f"; passes on {passed_on[test]}"
        if prefix is not stack.prefixes[1]:
            alone = self.alone_over_base(stack, prefix.entry, directory, check, test)
            finding += f"; {alone}"
        return finding

    def stack_checks(self, stack):
        """go vet and the two suites on the whole stack; what fails is traced to an entry."""
        findings = [
            f"{entry.label} conflicts with {ahead}: {', '.join(paths)}"
            for entry, ahead, paths in stack.conflicts
        ]
        for name, directory, check in (
            ("go vet ./...", None, self.vet_failures),
            (
                "scripts/tests",
                "scripts/tests",
                lambda tests=None: self.suite_failures("scripts/tests", tests),
            ),
            (
                "tests/contract",
                "tests/contract",
                lambda tests=None: self.suite_failures("tests/contract", tests),
            ),
        ):
            print(f"\n== {name} on {stack.prefixes[-1].name}", flush=True)
            self.checkout(stack.tip)
            failing = check()
            if not failing:
                continue
            print(f"\n== {name} failed; repeating on each prefix", flush=True)
            if not directory:
                findings.extend(self.vet_output)
            findings.extend(self.name_entries(name, directory, check, failing, stack))
        if findings:
            print("\nWhat the stack found:", flush=True)
            for finding in findings:
                print("  " + finding, flush=True)
            raise RuntimeError("; ".join(findings))

    def stack_steps(self, stack):
        return [
            ("Python environment", self.python_environment),
            ("CI workflow", self.load_workflow),
            ("Go toolchain", self.go_toolchain),
            ("Queue stack", lambda: self.stack_checks(stack)),
        ]

    def linter(self, name):
        """The linter a Lint step pins, installed if need be, and that step."""
        step = next(
            step
            for step in self.workflow["jobs"]["lint"]["steps"]
            if step["name"] == name
        )
        version = step["with"]["version"]
        directory = self.cache / ("golangci-lint-" + version)
        binary = directory / "golangci-lint"
        candidate = shutil.which("golangci-lint")
        if candidate and re.search(
            r"\bversion " + re.escape(version.lstrip("v")) + r"\b",
            self.run([candidate, "version"], capture=True),
        ):
            binary = Path(candidate)
        with cache_lock(self.cache):
            if not binary.exists():
                directory.mkdir(parents=True, exist_ok=True)
                install = [
                    "go",
                    "install",
                    "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@" + version,
                ]
                try:
                    self.run(install, env=dict(self.env, GOBIN=str(directory)))
                except subprocess.CalledProcessError as error:
                    raise CouldNotRun(
                        f"install the CI linter with: GOBIN={directory} {' '.join(install)}"
                    ) from error
        actual = self.run([binary, "version"], capture=True)
        if not re.search(
            r"\bversion " + re.escape(version.lstrip("v")) + r"\b", actual
        ):
            raise CouldNotRun(f"{binary} is not pinned {version}: {actual.strip()}")
        return binary, step

    def lint(self):
        binary, step = self.linter("Run golangci-lint")
        self.run([binary, "run", *step["with"]["args"].split()])

    def selection(self):
        packages = json_stream(self.run(["go", "list", "-json", "./..."], capture=True))
        files = changed_files(self.root)
        self.files = files
        selected = (
            sorted(package["ImportPath"] for package in packages)
            if self.all_packages
            else select_packages(packages, files, self.root)
        )
        self.packages = [
            name for name in selected if not name.endswith(TOPOLOGY_PACKAGES)
        ]
        for name in sorted(set(selected) - set(self.packages)):
            print(f"Left to the E2E job, which starts the topology: {name}")
        api = next(
            package["ImportPath"]
            for package in packages
            if package["Dir"] == str(self.root / "internal/api")
        )
        self.api_changed = (
            self.all_packages
            or api in self.packages
            or any(name.startswith("openapi") for name in files)
        )
        if self.api_changed and api not in self.packages:
            self.packages.append(api)
        for directory in GUARD_PACKAGES:
            guard = next(
                (
                    package["ImportPath"]
                    for package in packages
                    if package["Dir"] == str(self.root / directory)
                ),
                None,
            )
            if guard is None:
                print(f"No guard package {directory} on this tree")
                continue
            self.guards.append(guard)
            if guard not in self.packages:
                self.packages.append(guard)
        print("Changed paths:\n" + "\n".join(files))
        print("Go test packages:\n" + "\n".join(self.packages))

    def loopback_aliases(self):
        # Checked here, not by the test 30 minutes into Go tests (#1942).
        if not any(name.endswith(LOADGEN) for name in self.packages):
            return "NOT REQUIRED: tests/load/loadgen is not among the Go test packages"
        missing = unbindable_loopbacks()
        if missing:
            raise CouldNotRun(
                f"cannot bind {', '.join(missing)}, which tests/load/loadgen needs; "
                "macOS drops loopback aliases on reboot. Add them with: "
                + "; ".join(f"sudo ifconfig lo0 alias {address} up" for address in missing)
            )
        print("Can bind " + ", ".join(LOOPBACK_ALIASES))

    def chart(self):
        if not any(name.startswith("helm/restmail/") for name in self.files):
            return "NOT REQUIRED: the packaged chart is unchanged"
        if not (self.root / "scripts/check_chart_publish.py").exists():
            return "NOT REQUIRED: chart changed, but scripts/check_chart_publish.py is not on this base (#1945 adds it)"
        if not shutil.which("helm"):
            raise CouldNotRun("install helm to check the chart version is unpublished")
        self.run([self.python, "-B", "scripts/check_chart_publish.py"])

    def postgres(self):
        if not self.packages and not self.api_changed:
            return "NOT REQUIRED: no selected database-backed Go packages"
        try:
            self.run(["docker", "info"], capture=True)
        except subprocess.CalledProcessError as error:
            raise CouldNotRun("start Docker; the Go tests need PostgreSQL") from error
        service = self.workflow["jobs"]["test"]["services"]["postgres"]
        self.image = service["image"]
        existed = (
            subprocess.run(
                ["docker", "image", "inspect", self.image],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                check=False,
            ).returncode
            == 0
        )
        if not existed:
            self.run(["docker", "pull", self.image])
            self.created_image = True
        self.container = f"restmail-{self.loop}-postgres-{self.run_id}"
        created = self.run(
            [
                "docker",
                "run",
                "-d",
                "--name",
                self.container,
                "--label",
                "rest-mail-loop=" + self.loop,
                "--label",
                "rest-mail.gate=" + self.run_id,
                # So that `chore clean` finds it after a killed run (#1963).
                "--label",
                os.environ.get("RESTMAIL_LABEL") or "rest-mail.run=local",
                "--tmpfs",
                "/var/lib/postgresql/data",
                "-p",
                "127.0.0.1::5432",
                "-e",
                "POSTGRES_DB=restmail_test",
                "-e",
                "POSTGRES_USER=restmail",
                "-e",
                "POSTGRES_PASSWORD=restmail",
                self.image,
                "-c",
                "max_connections=400",
            ],
            capture=True,
        )
        self.container_id = (created or "").strip() or None
        print(self.container_id or "", flush=True)
        port = (
            self.run(["docker", "port", self.container, "5432/tcp"], capture=True)
            .strip()
            .rsplit(":", 1)[1]
        )
        self.env.update(
            DB_HOST="127.0.0.1",
            DB_PORT=port,
            DB_NAME="restmail_test",
            DB_USER="restmail",
            DB_PASS="restmail",
            RESTMAIL_IMPORT_LONG_PROOF="1",
        )
        for attempt in range(60):
            if (
                subprocess.run(
                    [
                        "docker",
                        "exec",
                        self.container,
                        "pg_isready",
                        "-h",
                        "127.0.0.1",
                        "-U",
                        "restmail",
                        "-d",
                        "restmail_test",
                    ],
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    check=False,
                ).returncode
                == 0
            ):
                self.run(
                    [
                        "go",
                        "build",
                        "-o",
                        self.work / "testdb-exec",
                        "./tools/testdb-exec",
                    ]
                )
                return
            time.sleep(1)
        raise CouldNotRun("isolated PostgreSQL was not ready within 60 seconds")

    def go_tests(self):
        remaining = [
            name for name in self.packages if not name.endswith(FIXTURE_PACKAGES)
        ]
        if not remaining:
            return "NOT REQUIRED: no changed Go packages or importers"
        guards = [name for name in remaining if name in self.guards]
        if guards:
            # Seconds, where the race shards take half an hour: a guard that any
            # change can break fails the step before them.
            remaining = [name for name in remaining if name not in guards]
            self.go_test_packages(guards)
        for directory in ("internal/api", "internal/api/handlers"):
            package = next(
                (name for name in remaining if name.endswith("/" + directory)), None
            )
            if not package:
                continue
            remaining.remove(package)
            listing = self.run(
                ["go", "test", "-race", "-list", ".*", "./" + directory], capture=True
            )
            names = sorted(
                name
                for name in listing.splitlines()
                if re.fullmatch(r"(?:Test|Example|Fuzz)\w*", name)
                and not name.endswith("WithVertrag")
            )
            if not names:
                raise RuntimeError(f"no race tests discovered for {directory}")
            label = directory.replace("/", "-") + "-race"
            binary = self.work / (label + ".test")
            self.run(["go", "test", "-c", "-race", "-o", binary, "./" + directory])
            events = self.shards(names, binary, label, RACE_SHARDS, "20m", directory)
            finished = {
                event.get("Test")
                for event in events
                if event.get("Action") in {"pass", "skip"}
            }
            if set(names) - finished:
                raise RuntimeError(
                    f"race shards did not execute every selected test: {sorted(set(names) - finished)}"
                )
            print(
                f"All {len(names)} selected tests in {directory} completed across {RACE_SHARDS} race shards."
            )
        if not remaining:
            return self.warning()
        self.go_test_packages(remaining)
        return self.warning()

    def go_test_packages(self, packages):
        self.run_go_test(
            [
                "go",
                "test",
                "-race",
                "-count=1",
                "-timeout=20m",
                "-skip",
                "WithVertrag$",
                "-exec",
                self.work / "testdb-exec",
                *packages,
            ]
        )

    def imap(self):
        if not self.all_packages and not any(
            name.startswith("third_party/go-imap/")
            or name
            in {
                "go.mod",
                "go.sum",
                ".github/workflows/ci.yml",
                "scripts/ci/go-imap-changed",
            }
            for name in self.files
        ):
            return "NOT REQUIRED: nested IMAP module and its toolchain are unchanged"
        module = self.root / "third_party/go-imap"
        self.run(
            [self.python, "-B", "-m", "unittest", "discover", "-s", ".github/tests"],
            root=module,
        )
        self.run(["go", "vet", "./..."], root=module)
        binary, step = self.linter("Run golangci-lint on the IMAP module")
        self.run([binary, "run", *step["with"]["args"].split()], root=module)
        # CI runs these wire proofs on linux/386. A Mac has no 386 at all, so here they
        # can only be shown to build; see the exception in ci-local-coverage.json.
        self.run(
            ["go", "vet", "./pkg/imap/internal/wire", "./pkg/imap/rfctest"],
            root=module,
            env=dict(self.env, GOOS="linux", GOARCH="386", CGO_ENABLED="0"),
        )
        self.run(["go", "run", "./internal/checklist", "pkg/imap/rfctest"], root=module)
        # Not in the module: CI's checkout is thrown away, and this may be a working tree.
        profile = self.work / "go-imap-coverage.out"
        self.run_go_test(
            [
                "go",
                "test",
                "-race",
                "-count=1",
                "-timeout=" + IMAP_TIMEOUT,
                "-covermode=atomic",
                "-coverpkg=./...",
                f"-coverprofile={profile}",
                "./...",
            ],
            root=module,
        )
        self.run(
            [
                "bash",
                "-c",
                'set -euo pipefail; go tool cover -func="$1" | python3 -B .github/scripts/check_coverage.py',
                "bash",
                profile,
            ],
            root=module,
        )

    def fixtures(self):
        if not self.all_packages and not any(
            package.endswith(
                ("/tests/contract/fixtures", "/tests/contract/fixtureplan")
            )
            for package in self.packages
        ):
            return "NOT REQUIRED: contract fixture packages and their dependencies are unchanged"
        self.run(
            [
                "docker",
                "exec",
                self.container,
                "createdb",
                "-U",
                "restmail",
                "restmail_contract_fixture_ci",
            ]
        )
        self.run_go_test(
            [
                "go",
                "test",
                "-race",
                "-count=1",
                "-timeout=20m",
                "./tests/contract/fixtures",
                "./tests/contract/fixtureplan",
            ],
            env=dict(self.env, DB_NAME="restmail_contract_fixture_ci"),
        )

    def faults(self):
        tags = self.run(
            [self.python, "-B", "tests/contract/faults.py", "tags"], capture=True
        ).strip()
        self.run(["go", "vet", "-tags", tags, "./cmd/api", "./internal/faultinject"])
        self.run_go_test(
            ["go", "test", "-race", "-count=1", "-tags", tags, "./internal/faultinject"]
        )

    def vertrag(self):
        if not self.api_changed:
            return "NOT REQUIRED: no API changes or selected API importers (use --all to force)"
        with cache_lock(self.cache):
            binary = self.run(
                [
                    self.python,
                    "-B",
                    "scripts/vertrag_pin.py",
                    "ensure",
                    self.cache / "vertrag",
                ],
                capture=True,
            ).strip()
        self.env["RESTMAIL_VERTRAG_BINARY"] = binary
        listing = self.run(
            ["go", "test", "-list", "WithVertrag$", "./internal/api"], capture=True
        )
        names = [
            name
            for name in listing.splitlines()
            if re.fullmatch(r"Test\w*WithVertrag", name)
        ]
        if not names:
            raise RuntimeError("no *WithVertrag tests discovered")
        self.run(["go", "test", "-c", "-o", self.work / "api.test", "./internal/api"])
        events = self.shards(
            names, self.work / "api.test", "Vertrag", 3, VERTRAG_TIMEOUT
        )
        verify_vertrag(events, names)
        print(
            f"All {len(names)} discovered *WithVertrag tests passed; none skipped whole."
        )
        return self.warning()

    def shards(
        self, names, binary, label, count, timeout, directory="internal/api", again=True
    ):
        if not names:
            raise RuntimeError(f"no tests selected for {label}")
        processes, outputs, logs = [], [], []
        try:
            seconds = self.test_seconds(label)
            for shard, selected in enumerate(plan_shards(names, seconds, count)):
                pattern = "^(" + "|".join(selected) + ")$"
                log = self.work / f"{label}-{shard}.json"
                output = log.open("w")
                outputs.append(output)
                logs.append(log)
                processes.append(
                    subprocess.Popen(
                        [
                            "go",
                            "tool",
                            "test2json",
                            "-t",
                            "-p",
                            f"{label}-{shard}",
                            str(self.work / "testdb-exec"),
                            str(binary),
                            "-test.v=test2json",
                            "-test.timeout=" + timeout,
                            "-test.run",
                            pattern,
                        ],
                        cwd=self.root / directory,
                        env=self.env,
                        stdout=output,
                        stderr=subprocess.STDOUT,
                        start_new_session=True,
                    )
                )
            started = time.monotonic()
            elapsed = {}
            while len(elapsed) < len(processes):
                for index, process in enumerate(processes):
                    if index not in elapsed and process.poll() is not None:
                        elapsed[index] = time.monotonic() - started
                if len(elapsed) < len(processes):
                    time.sleep(1)
            for index in sorted(elapsed):
                print(
                    f"{label} shard {index + 1} of {len(processes)}: "
                    f"{elapsed[index]:.0f}s wall, limit {timeout}"
                )
            for output in outputs:
                output.close()
            shard_events = []
            for log in logs:
                parsed, stray = parse_events(log.read_text(errors="replace"))
                shard_events.append(parsed)
                for line in stray[:20]:
                    print(f"{log.name}: not a test event: {line[:500]}")
            events = [event for parsed in shard_events for event in parsed]
            self.record_test_seconds(label, events)
            failed = [
                index for index, process in enumerate(processes) if process.poll()
            ]
            if failed:
                tests, running, limited = [], [], []
                for index in failed:
                    names_here, report = failure_report(shard_events[index])
                    print(report)
                    if timed_out(shard_events[index]):
                        limited.append(index)
                        failed_first, cut_off = split_at_limit(shard_events[index])
                        tests.extend(failed_first)
                        running.extend(cut_off)
                    else:
                        tests.extend(names_here or [f"shard {index}: none named"])
                finished = {
                    event.get("Test")
                    for event in events
                    if event.get("Action") in {"pass", "skip"}
                } & set(names)
                if limited and not tests and again:
                    # A wall-clock limit at a load of 300 says little about a test,
                    # so what it cut off gets one new process under the same limit.
                    # A test that hangs reaches the limit again: the step has no verdict.
                    unfinished = [name for name in names if name not in finished]
                    print(
                        f"{label}: shards {limited} hit the {timeout} limit at load "
                        f"{os.getloadavg()[0]:.0f}; running {unfinished} once more"
                    )
                    self.limited.append(f"{label}: {running}")
                    return events + self.shards(
                        unfinished,
                        binary,
                        label + "-again",
                        count,
                        timeout,
                        directory,
                        False,
                    )
                message = f"{label} shards failed: {failed}; failed tests: {tests}; "
                if limited:
                    load = os.getloadavg()[0]
                    message += (
                        f"shards {limited} hit the {timeout} limit at load {load:.0f}, "
                        f"which does not show a running test at fault; running then: {running}; "
                    )
                message += f"{len(finished)} of {len(names)} selected tests finished"
                if limited and not tests:
                    raise NoVerdict(message)
                if directory == HANDLERS:
                    # Round-robin shard 4 of 4 failed 44 tests where the whole package
                    # passed 910 of 910, so a failure here does not yet blame the test.
                    message += (
                        f"; {HANDLERS} depends on test order, so this may reflect the split: "
                        f"confirm with go test -race ./{HANDLERS}"
                    )
                raise RuntimeError(message)
            return events
        finally:
            for process in processes:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGTERM)
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait()
            for output in outputs:
                output.close()

    def seconds_file(self, label):
        # A rerun of what a limit cut off is the same tests, so it shares their times.
        while label.endswith("-again"):
            label = label[: -len("-again")]
        return self.cache / "go-test-seconds" / (label + ".json")

    def test_seconds(self, label):
        """What each test took when it last passed on this machine, by name."""
        try:
            seconds = json.loads(self.seconds_file(label).read_text())
        except (OSError, ValueError):
            return {}
        if not isinstance(seconds, dict):
            return {}
        return {
            name: value
            for name, value in seconds.items()
            if isinstance(value, (int, float)) and value >= 0
        }

    def record_test_seconds(self, label, events):
        """Keeps each passed top-level test's time for the next run's shard plan.

        Every worktree on the machine shares the file. It is replaced whole, so a
        reader never sees half of it; of two runs writing at once, one's times win.
        """
        passed = {
            event["Test"]: round(float(event["Elapsed"]), 1)
            for event in events
            if event.get("Action") == "pass"
            and "/" not in event.get("Test", "/")
            and isinstance(event.get("Elapsed"), (int, float))
        }
        if not passed:
            return
        path = self.seconds_file(label)
        try:
            path.parent.mkdir(parents=True, exist_ok=True)
            seconds = {**self.test_seconds(label), **passed}
            with tempfile.NamedTemporaryFile(
                "w", dir=path.parent, prefix=path.name, suffix=".tmp", delete=False
            ) as handle:
                json.dump(seconds, handle, indent=0, sort_keys=True)
            os.replace(handle.name, path)
        except OSError as error:
            print(f"WARNING: could not keep test times in {path}: {error}")

    def cleanup(self):
        failures = []
        # Its own container alone, by the id `docker run` printed (or its unique
        # name when that never arrived), so a gate ending cannot reach another
        # gate's database on the same machine (#1942).
        own = self.container_id or self.container
        if own and subprocess.run(["docker", "rm", "-fv", own], check=False).returncode:
            failures.append("docker rm -fv " + own)
        if (
            self.created_image
            and subprocess.run(
                ["docker", "image", "rm", self.image], check=False
            ).returncode
        ):
            # Another run on this machine may be using the image it found here.
            print(f"WARNING: left image {self.image}: docker image rm refused")
        if failures:
            raise RuntimeError("cleanup failed: " + "; ".join(failures))

    def steps(self):
        actions = (
            self.python_environment,
            self.coverage,
            self.go_toolchain,
            lambda: self.python_suite("scripts/tests"),
            lambda: self.python_suite("tests/contract"),
            lambda: self.python_suite("tests/load"),
            lambda: self.run(["go", "mod", "download"]),
            lambda: self.run(["go", "vet", "./..."]),
            lambda: self.run(["go", "build", "./..."]),
            self.lint,
            self.selection,
            self.loopback_aliases,
            self.chart,
            self.postgres,
            self.go_tests,
            self.imap,
            self.fixtures,
            self.faults,
            self.vertrag,
        )
        return list(zip(STEP_NAMES, actions))


def main(argv=None):
    unwind_on_termination()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--all",
        action="store_true",
        help="test every Go package, including all API Vertrag examples",
    )
    parser.add_argument(
        "--allow-stale",
        action="store_true",
        help="explicitly accept a stale base (warning still printed)",
    )
    merge = parser.add_mutually_exclusive_group()
    merge.add_argument(
        "--merge-pr",
        type=int,
        help="fetch PR head and test it over newest main and every merge queue entry ahead of it, in a temporary worktree",
    )
    merge.add_argument(
        "--merge-ref",
        help="test a local commit/ref merged with newest main without touching this checkout",
    )
    merge.add_argument(
        "--stack",
        action="store_true",
        help="merge every merge queue entry in order over newest main, run go vet, scripts/tests and tests/contract, and name the entry behind each failure",
    )
    parser.add_argument(
        "--base",
        help="merge over this commit rather than newest main, to replay a past stack",
    )
    parser.add_argument(
        "--entry",
        action="append",
        default=[],
        help="merge this commit ahead of the head rather than reading the queue, to replay a past stack; repeat in queue order",
    )
    parser.add_argument(
        "--cache",
        type=Path,
        default=Path(os.environ.get("XDG_CACHE_HOME", Path.home() / "Library/Caches"))
        / "rest-mail/ci-local",
    )
    parser.add_argument("--scratch", type=Path, default=default_scratch())
    args = parser.parse_args(argv)
    merging = args.merge_pr or args.merge_ref or args.stack
    if (args.base or args.entry) and not merging:
        parser.error("--base and --entry need --stack, --merge-pr or --merge-ref")
    root = Path(git(ROOT, "rev-parse", "--show-toplevel")).resolve()
    cache, scratch = (
        args.cache.expanduser().resolve(),
        args.scratch.expanduser().resolve(),
    )
    for path in (cache, scratch):
        if path.is_relative_to(root) or str(path).startswith("/Volumes/sdcard256gb"):
            parser.error(
                "cache and scratch must be outside the repository and off the SD card"
            )
        path.mkdir(parents=True, exist_ok=True)
    parent = run_parent(scratch)

    def execute(target, steps=lambda gate: gate.steps()):
        with tempfile.TemporaryDirectory(prefix="run-", dir=parent) as temporary:
            gate = Gate(target, cache, Path(temporary), args.all, loop_label(scratch))
            try:
                code = run_steps(steps(gate))
            finally:
                gate.cleanup()
        return carry_up(code)

    def freshness():
        # A killed run cannot remove its worktree, and leaves an entry for a missing one.
        git(root, "worktree", "prune")
        try:
            git(
                root,
                "fetch",
                "--no-tags",
                "origin",
                "+refs/heads/main:refs/remotes/origin/main",
            )
        except subprocess.CalledProcessError as error:
            raise CouldNotRun(
                "cannot fetch newest main; check the connection and SSH key"
            ) from error
        if not merging:
            check_base(root, args.allow_stale)

    def ahead_of_head():
        """What goes between the base and the head: nothing for --merge-ref alone."""
        if args.entry:
            entries = [
                Entry(ref, git(root, "rev-parse", "--verify", ref + "^{commit}"))
                for ref in args.entry
            ]
        elif args.merge_ref:
            entries = []
        else:
            entries = read_merge_queue(root)
            if args.merge_pr:
                entries = entries_ahead(entries, args.merge_pr)
        fetch_commits(root, entries)
        print(
            "Ahead of the head, in order: "
            + (", ".join(entry.label for entry in entries) or "nothing"),
            flush=True,
        )
        return build_stack(
            root, args.base or "origin/main", entries, args.base or "main"
        )

    def queue_stack():
        print(
            "SCOPE: go vet, scripts/tests and tests/contract on the merge queue stacked over main; not the other local checks."
        )
        stack = ahead_of_head()
        if len(stack.prefixes) == 1 and not stack.conflicts:
            return "NOT REQUIRED: the merge queue is empty"
        with detached_worktree(root, stack.tip, parent) as target:
            return execute(target, lambda gate: gate.stack_steps(stack))

    def checks():
        if args.stack:
            return queue_stack()
        print(
            "SCOPE: local Lint/Test checks and native Go build; not Helm, cross-builds, frontend builds, image smoke tests, proof-union reports or full-topology E2E."
        )
        if args.merge_pr or args.merge_ref:
            head = args.merge_ref
            if args.merge_pr:
                git(
                    root,
                    "fetch",
                    "--no-tags",
                    "origin",
                    f"refs/pull/{args.merge_pr}/head",
                )
                head = git(root, "rev-parse", "FETCH_HEAD")
            head = git(root, "rev-parse", "--verify", head + "^{commit}")
            stack = ahead_of_head()
            label = f"#{args.merge_pr}" if args.merge_pr else args.merge_ref
            with merged_worktree(root, head, parent, stack, label) as target:
                outcome = execute(target)
            if stack.conflicts:
                left_out = [
                    f"{entry.label} conflicts with {ahead}: {', '.join(paths)}"
                    for entry, ahead, paths in stack.conflicts
                ]
                print("Left out of the stack: " + "; ".join(left_out), flush=True)
                return "WARNING: tested without what conflicts ahead: " + "; ".join(
                    left_out
                )
            return outcome
        return execute(root)

    code = run_steps(
        [("Newest main / ancestry", freshness), ("Local checks / cleanup", checks)]
    )
    print(VERDICTS[code], flush=True)
    return code


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("COULD-NOT-RUN: interrupted; cleanup attempted", file=sys.stderr)
        sys.exit(130)
