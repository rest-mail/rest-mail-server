#!/usr/bin/env python3
"""The testbed and reference-mailserver commits every E2E and contract run uses (#1751).

The pins live in scripts/substrate-pins.yml, one `name: commit` line per repository.
CI and a person's checkout read the same file through this script, so the same
server commit runs against the same substrate everywhere.

  substrate_pins.py ref NAME           print NAME's pinned commit
  substrate_pins.py sync NAME...       clone NAME into .workspace/ when absent, check out
                                       its pin and apply this platform's preparation
  substrate_pins.py preflight          print the version receipt (server commit and each
                                       substrate commit); fail when a clone is not at its
                                       pin, not prepared, or has uncommitted changes
  substrate_pins.py preflight --report print the receipt and never fail

`chore testbed:init` and `chore mailserver:init` run sync; CI runs those tasks and
then preflight. RESTMAIL_SUBSTRATE=unpinned leaves an existing clone where it is (to
run a substrate branch) and turns the preflight's failures into warnings.

The reference PostgreSQL health check runs every second on both platforms.
The platform preparation is for native-Linux Docker: reference-mailserver's
dkim:provision chmods a container-root-owned key from the host, which only Docker
Desktop's host-owned bind mounts allow. On Linux the chmod runs in a container.
"""

import os
import platform
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PIN_FILE = os.path.join(ROOT, "scripts", "substrate-pins.yml")
WORKSPACE = os.path.join(ROOT, ".workspace")

REPOS = {
    "testbed": "git@github.com:rest-mail/testbed.git",
    "reference-mailserver": "git@github.com:rest-mail/reference-mailserver.git",
}
INIT = {
    "testbed": "chore testbed:init",
    "reference-mailserver": "chore mailserver:init",
}

PIN_LINE = re.compile(r"^([A-Za-z0-9_-]+):\s*(\S+)$")
COMMIT = re.compile(r"^[0-9a-f]{40}$")

TASK_FILES = ("chores.yml", "Taskfile.yml")
DKIM_HOST_CHMOD = 'chmod 644 "$KEYDIR/$DOMAIN.$SEL.key"'
DKIM_CONTAINER_CHMOD = (
    'docker run --rm -v "$(pwd)/$KEYDIR:/keys" alpine:3.20 '
    'chmod 644 "/keys/$DOMAIN.$SEL.key"'
)


def pins(text):
    """Each repository's pinned commit, from the manifest's text."""
    found = {}
    for number, line in enumerate(text.splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        match = PIN_LINE.match(line)
        if not match:
            raise ValueError(f"substrate-pins.yml line {number} is not `name: commit`")
        name, commit = match.groups()
        if name not in REPOS:
            raise ValueError(f"substrate-pins.yml line {number} names no substrate: {name}")
        if name in found:
            raise ValueError(f"substrate-pins.yml pins {name} twice")
        if not COMMIT.match(commit):
            raise ValueError(
                f"substrate-pins.yml pins {name} to {commit}, not a full 40-character commit"
            )
        found[name] = commit
    missing = [name for name in REPOS if name not in found]
    if missing:
        raise ValueError("substrate-pins.yml has no pin for " + ", ".join(missing))
    return found


def read_pins(path=None):
    with open(path or PIN_FILE, encoding="utf-8") as handle:
        return pins(handle.read())


def unpinned():
    return os.environ.get("RESTMAIL_SUBSTRATE", "") == "unpinned"


def git(repo, *args):
    return subprocess.run(
        ["git", "-C", repo, *args], capture_output=True, text=True, check=False
    )


def git_out(repo, *args):
    result = git(repo, *args)
    if result.returncode != 0:
        raise RuntimeError(
            f"git {' '.join(args)} failed in {repo}: {result.stderr.strip()}"
        )
    return result.stdout


def has_commit(repo, commit):
    return git(repo, "cat-file", "-e", commit + "^{commit}").returncode == 0


def changed_paths(repo):
    """Tracked files that differ from HEAD, staged or not."""
    return set(git_out(repo, "diff", "--name-only", "HEAD").splitlines())


def needs_preparation(name, system):
    return name == "reference-mailserver" and system == "Linux"


def prepared_text(text):
    """reference-mailserver's task file with dkim:provision's host chmod in a container."""
    if DKIM_HOST_CHMOD in text:
        return text.replace(DKIM_HOST_CHMOD, DKIM_CONTAINER_CHMOD)
    if DKIM_CONTAINER_CHMOD in text:
        return text
    raise ValueError(
        "reference-mailserver's dkim:provision has no host chmod to prepare: "
        "its task file changed upstream, so this preparation needs updating"
    )


def task_file(repo, commit="HEAD"):
    """The task file reference-mailserver ships at commit; it is moving to chores.yml."""
    for name in TASK_FILES:
        if git(repo, "cat-file", "-e", f"{commit}:{name}").returncode == 0:
            return name
    raise ValueError(f"no {' or '.join(TASK_FILES)} in {repo} at {commit}")


def read_file(path):
    try:
        with open(path, encoding="utf-8") as handle:
            return handle.read()
    except FileNotFoundError:
        return None


def is_prepared(repo):
    name = task_file(repo)
    expected = prepared_text(git_out(repo, "show", "HEAD:" + name))
    return name, read_file(os.path.join(repo, name)) == expected


def prepared_files(name, repo, system):
    if name != "reference-mailserver":
        return {}
    postgres = "tasks/postgres.yml"
    text = git_out(repo, "show", "HEAD:" + postgres)
    if "--health-interval 10s" not in text and "--health-interval 1s" not in text:
        raise ValueError("reference-mailserver's PostgreSQL health check changed upstream")
    prepared = {postgres: text.replace("--health-interval 10s", "--health-interval 1s")}
    if needs_preparation(name, system):
        task = task_file(repo)
        prepared[task] = prepared_text(git_out(repo, "show", "HEAD:" + task))
    return prepared


def sync(name, commit, workspace=None, remote=None, system=None, out=sys.stdout):
    """Clone name when absent, check out its pin and prepare it for this platform."""
    workspace = workspace or WORKSPACE
    system = system or platform.system()
    repo = os.path.join(workspace, name)
    if not os.path.isdir(os.path.join(repo, ".git")):
        os.makedirs(workspace, exist_ok=True)
        result = subprocess.run(
            ["git", "clone", "--quiet", remote or REPOS[name], repo],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            raise RuntimeError(f"cloning {name} failed: {result.stderr.strip()}")
    head = git_out(repo, "rev-parse", "HEAD").strip()
    move = head != commit and not unpinned()
    if move:
        changed = changed_paths(repo)
        for prepared, text in prepared_files(name, repo, system).items():
            if prepared in changed and read_file(os.path.join(repo, prepared)) == text:
                # Our own preparation, not the person's work: undo it before moving.
                git_out(repo, "checkout", "--quiet", "--", prepared)
                changed.discard(prepared)
        if changed:
            raise RuntimeError(
                f"{name} is at {head} with uncommitted changes to "
                f"{', '.join(sorted(changed))}; its pin is {commit}. Commit or stash them "
                f"in {repo} and run {INIT[name]} again, or set RESTMAIL_SUBSTRATE=unpinned "
                "to keep this checkout"
            )
        if not has_commit(repo, commit):
            git(repo, "fetch", "--quiet", "origin", commit)
        if not has_commit(repo, commit):
            raise RuntimeError(f"{name}'s origin does not have its pinned commit {commit}")
        git_out(repo, "checkout", "--quiet", "--detach", commit)
    at = git_out(repo, "rev-parse", "HEAD").strip()
    note = "" if at == commit else f" (unpinned; the pin is {commit})"
    print(f"  {name} @ {at}{note}", file=out)
    for prepared, text in prepared_files(name, repo, system).items():
        path = os.path.join(repo, prepared)
        current = read_file(path)
        if current != text:
            if prepared == "tasks/postgres.yml" and current != git_out(repo, "show", "HEAD:" + prepared):
                raise RuntimeError(f"{name} has uncommitted changes to {prepared}; refusing to overwrite them")
            with open(path, "w", encoding="utf-8") as handle:
                handle.write(text)
        reason = "PostgreSQL health interval 1s" if prepared == "tasks/postgres.yml" else "dkim:provision chmods in a container (Linux)"
        print(f"  {name}: {prepared} {reason}", file=out)
    return at


def inspect(name, commit, workspace=None, system=None):
    """name's checked-out commit, its preparation, and what keeps it from being the pin."""
    workspace = workspace or WORKSPACE
    system = system or platform.system()
    repo = os.path.join(workspace, name)
    if not os.path.isdir(os.path.join(repo, ".git")):
        return None, "", [f"{name} is not cloned: run {INIT[name]}"]
    head = git_out(repo, "rev-parse", "HEAD").strip()
    problems = []
    if head != commit:
        problems.append(f"{name} is at {head}, its pin is {commit}: run {INIT[name]}")
    allowed = set()
    detail = ""
    for prepared, text in prepared_files(name, repo, system).items():
        if read_file(os.path.join(repo, prepared)) == text:
            reason = "PostgreSQL health interval 1s" if prepared == "tasks/postgres.yml" else "dkim:provision chmod in a container"
            detail = "; ".join(filter(None, (detail, reason)))
            allowed.add(prepared)
        else:
            problems.append(
                f"{name}'s {prepared} is not prepared for {'native-Linux Docker' if needs_preparation(name, system) else 'fast health checks'}: run {INIT[name]}"
            )
    changed = changed_paths(repo) - allowed
    if changed:
        problems.append(
            f"{name} has uncommitted changes to {', '.join(sorted(changed))}"
        )
    return head, detail, problems


def server_commit(root=None):
    root = root or ROOT
    head = git_out(root, "rev-parse", "HEAD").strip()
    dirty = git_out(root, "status", "--porcelain", "--untracked-files=no").strip()
    return head + (" with uncommitted changes" if dirty else "")


def preflight(report=False, root=None, workspace=None, system=None, out=sys.stdout):
    """Print the receipt; 1 when a substrate is not CI's, unless only reporting."""
    system = system or platform.system()
    pinned = read_pins()
    print("substrate receipt (scripts/substrate-pins.yml)", file=out)
    print(f"  {'server':<22} {server_commit(root)}", file=out)
    problems = []
    for name, commit in pinned.items():
        head, detail, found = inspect(name, commit, workspace, system)
        verdict = "ok" if not found else "DIFFERS"
        line = f"  {name:<22} {head or 'not cloned':<40}  pin {commit}  {verdict}"
        if detail:
            line += f"; {detail}"
        print(line, file=out)
        problems += found
    if not problems:
        return 0
    lenient = report or unpinned()
    word = "warning" if lenient else "FAIL"
    for problem in problems:
        print(f"  {word}: {problem}", file=out)
    if lenient:
        print("  these are not CI's substrate inputs; a verdict here may differ from CI's", file=out)
        return 0
    print(
        "  the substrate is not CI's; set RESTMAIL_SUBSTRATE=unpinned to run it anyway",
        file=out,
    )
    return 1


def main(argv):
    try:
        if len(argv) == 2 and argv[0] == "ref":
            print(read_pins()[argv[1]])
            return 0
        if len(argv) >= 2 and argv[0] == "sync":
            pinned = read_pins()
            for name in argv[1:]:
                sync(name, pinned[name])
            return 0
        if argv in (["preflight"], ["preflight", "--report"]):
            return preflight(report=len(argv) == 2)
    except KeyError as error:
        print(f"substrate_pins: no substrate named {error}", file=sys.stderr)
        return 2
    except (OSError, RuntimeError, ValueError) as error:
        print(f"substrate_pins: {error}", file=sys.stderr)
        return 1
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
