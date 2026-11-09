#!/usr/bin/env python3
"""Remove one worker's scratch run directory, and nothing else (#2065).

Workers share one scratch root, for example $HOME/tmp/rest-mail-loop. Each worker owns
exactly one run directory under it. Removing the shared root removed every other
worker's gate log, the handoff file and the key beside them. This helper is the supported
way to remove a run directory. It refuses unless every check below passes, and it
decides nothing from a name prefix.

    scratch_cleanup.py create   --root ROOT --run-id RUN --owner OWNER
    scratch_cleanup.py complete --root ROOT --run-id RUN --owner OWNER
    scratch_cleanup.py clean    --root ROOT --run-id RUN --owner OWNER [--dry-run]
    scratch_cleanup.py list     --root ROOT

`create` makes ROOT/RUN and writes an ownership marker into it. ROOT must already exist.
`complete` records that the run has finished; only its owner can do that. `clean`
removes ROOT/RUN only when all of these hold:

  - ROOT is an absolute path to a real directory. It is not a symlink, not "/", not the
    home directory and not a parent of it.
  - RUN is one plain name: no separator, no dot segment, no leading dash. ROOT/RUN is
    a real directory directly under ROOT, owned by this user. A symlink is refused.
  - RUN holds a marker that is a regular file of this user, not a symlink, and small.
    The marker names this run, this ROOT and this OWNER. A copied marker or a marker
    from another root does not match.
  - OWNER is given explicitly and equals the marker's owner exactly.
  - The marker says the run is complete. A run that is active, or has a status this
    code does not know, is never removed, not even by its owner.

Anything else is refused with exit status 3 and nothing is removed. `list` and
`clean --dry-run` only read; they never remove.

This guards the supported helper. It does not sandbox a shell `rm`, and a worker that
runs `rm -rf` on the shared root is still wrong. Do not do that.
"""

import argparse
import json
import os
import re
import shutil
import stat
import sys
import tempfile
import time
from pathlib import Path

MARKER_NAME = ".restmail-run-owner.json"
MARKER_VERSION = 1
MAX_MARKER_BYTES = 4096
MAX_NAME_LENGTH = 128
ACTIVE = "active"
COMPLETE = "complete"
REFUSED_EXIT = 3

RUN_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*")
OWNER = re.compile(r"[\x21-\x7e]+")


class Refused(Exception):
    """The request is not one this helper may carry out. Nothing was changed."""


def marker_path(run):
    return Path(run) / MARKER_NAME


def valid_run_id(run_id):
    if not isinstance(run_id, str) or len(run_id) > MAX_NAME_LENGTH or not RUN_ID.fullmatch(run_id):
        raise Refused(f"run id {run_id!r} is not one plain directory name")
    return run_id


def valid_owner(owner):
    if not isinstance(owner, str) or len(owner) > MAX_NAME_LENGTH or not OWNER.fullmatch(owner):
        raise Refused("an explicit owner is required: printable characters, no spaces")
    return owner


def real_root(root):
    """The shared root as a real path, or a refusal. It is never a run itself."""
    text = os.fspath(root)
    if not isinstance(text, str) or not os.path.isabs(text):
        raise Refused(f"root {text!r} is not an absolute path")
    if os.path.islink(text):
        raise Refused(f"root {text} is a symlink")
    resolved = Path(os.path.realpath(text))
    if not resolved.is_dir():
        raise Refused(f"root {text} is not a directory")
    home = Path(os.path.realpath(os.path.expanduser("~")))
    if resolved == resolved.parent or resolved == home or resolved in home.parents:
        raise Refused(f"root {resolved} is the filesystem root, the home directory or a parent of it")
    return resolved


def run_directory(root, run_id):
    """ROOT/RUN when it is a real directory of this user directly under the root."""
    run = root / valid_run_id(run_id)
    try:
        info = os.lstat(run)
    except FileNotFoundError:
        raise Refused(f"{run} does not exist") from None
    if not stat.S_ISDIR(info.st_mode):
        raise Refused(f"{run} is not a real directory (a file or a symlink is refused)")
    if info.st_uid != os.geteuid():
        raise Refused(f"{run} is not owned by this user")
    if Path(os.path.realpath(run)) != run or run.parent != root:
        raise Refused(f"{run} is not directly under {root}")
    return run


def read_marker(run):
    path = marker_path(run)
    try:
        info = os.lstat(path)
    except FileNotFoundError:
        raise Refused(f"{run} has no ownership marker") from None
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_size > MAX_MARKER_BYTES:
        raise Refused(f"{path} is not a plain marker file of this user")
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(descriptor, "rb") as handle:
            data = json.loads(handle.read(MAX_MARKER_BYTES + 1))
    except (OSError, ValueError) as error:
        raise Refused(f"{path} cannot be read as a marker: {error}") from None
    fields = ("run_id", "owner", "status", "root")
    if not isinstance(data, dict) or data.get("version") != MARKER_VERSION:
        raise Refused(f"{path} is not a version {MARKER_VERSION} marker")
    if not all(isinstance(data.get(field), str) for field in fields):
        raise Refused(f"{path} is missing a field")
    return data


def write_marker(run, *, run_id, owner, status, root):
    """Replaces the marker atomically. The marker is a plain file of this user."""
    body = {
        "version": MARKER_VERSION,
        "run_id": run_id,
        "owner": owner,
        "status": status,
        "root": os.path.realpath(root),
        "pid": os.getpid(),
        "updated": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    descriptor, temporary = tempfile.mkstemp(prefix=".marker.", dir=run)
    try:
        with os.fdopen(descriptor, "w") as handle:
            json.dump(body, handle)
        os.chmod(temporary, 0o600)
        os.replace(temporary, marker_path(run))
    except BaseException:
        if os.path.lexists(temporary):
            os.unlink(temporary)
        raise


def owned_marker(root, run_id, owner):
    """The run and its marker when this owner's marker names this run in this root."""
    owner = valid_owner(owner)
    run = run_directory(root, run_id)
    marker = read_marker(run)
    if marker["run_id"] != run_id or marker["root"] != str(root):
        raise Refused(f"the marker in {run} does not name this run and root")
    if marker["owner"] != owner:
        raise Refused(f"{run} belongs to another owner")
    return run, marker


def check_removable(root, run_id, owner):
    """Returns the run directory, or raises Refused. Reads only."""
    root = real_root(root)
    run, marker = owned_marker(root, run_id, owner)
    if marker["status"] != COMPLETE:
        raise Refused(f"{run} is not complete (status {marker['status']!r}); an active run is never removed")
    return run


def create_run(root, run_id, owner):
    root = real_root(root)
    valid_owner(owner)
    run = root / valid_run_id(run_id)
    try:
        os.mkdir(run, 0o700)
    except FileExistsError:
        raise Refused(f"{run} already exists") from None
    write_marker(run, run_id=run_id, owner=owner, status=ACTIVE, root=root)
    return run


def complete_run(root, run_id, owner):
    root = real_root(root)
    run, _ = owned_marker(root, run_id, owner)
    write_marker(run, run_id=run_id, owner=owner, status=COMPLETE, root=root)
    return run


def cleanup_run(root, run_id, owner):
    """Removes the owner's completed run directory and returns its path."""
    run = check_removable(root, run_id, owner)
    # Check again at the last moment. rmtree itself refuses a symlink and never follows one.
    check_removable(root, run_id, owner)
    shutil.rmtree(run)
    return run


def list_runs(root):
    """Describes each entry of the root without touching any of them."""
    root = real_root(root)
    entries = []
    for name in sorted(os.listdir(root)):
        entry = {"run_id": name, "status": "unowned", "owner": ""}
        try:
            marker = read_marker(run_directory(root, name))
            if marker["run_id"] == name and marker["root"] == str(root):
                entry["owner"] = marker["owner"]
                entry["status"] = COMPLETE if marker["status"] == COMPLETE else ACTIVE
        except Refused:
            pass
        entries.append(entry)
    return entries


def parser():
    argument_parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    commands = argument_parser.add_subparsers(dest="command", required=True)
    for name in ("create", "complete", "clean", "list"):
        command = commands.add_parser(name)
        command.add_argument("--root", required=True)
        if name != "list":
            command.add_argument("--run-id", required=True)
            command.add_argument("--owner", required=True)
        if name == "clean":
            command.add_argument("--dry-run", action="store_true")
    return argument_parser


def main(argv=None):
    arguments = parser().parse_args(argv)
    try:
        if arguments.command == "list":
            for entry in list_runs(arguments.root):
                print(f"{entry['status']}\t{entry['owner'] or '-'}\t{entry['run_id']}")
        elif arguments.command == "create":
            print(f"created {create_run(arguments.root, arguments.run_id, arguments.owner)}")
        elif arguments.command == "complete":
            print(f"completed {complete_run(arguments.root, arguments.run_id, arguments.owner)}")
        elif arguments.dry_run:
            print(f"would remove {check_removable(arguments.root, arguments.run_id, arguments.owner)}")
        else:
            print(f"removed {cleanup_run(arguments.root, arguments.run_id, arguments.owner)}")
    except Refused as refusal:
        print(f"scratch_cleanup: refused: {refusal}", file=sys.stderr)
        return REFUSED_EXIT
    return 0


if __name__ == "__main__":
    sys.exit(main())
