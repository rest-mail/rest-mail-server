#!/usr/bin/env python3
"""Fail if chores.yml or tasks/ can make a Docker resource `chore clean` cannot find (#1963).

Three rules, each for a way a resource escapes the label:

- a `docker run`, `docker create`, `docker network create`, `docker volume create`
  or compose bring-up carries `--label "$RESTMAIL_LABEL"`;
- a named volume on a `docker run` or `docker create` has a labelled `docker volume
  create` too, because Docker otherwise creates it on first use with no labels;
- no container gets the whole checkout at /src by bind mount, because every file it
  reads keeps a file open on the Mac. Use scripts/source_volume.py.

It reads the text of the commands, so it needs neither Docker nor a topology.
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

CREATING = re.compile(
    r"(?<![`\w-])docker(?:-compose\b.*\b(?:up|create)\b|\s+compose\b.*\b(?:up|create)\b"
    r"|\s+run\b|\s+create\b|\s+network\s+create\b|\s+volume\s+create\b)"
)
RUNNING = re.compile(r"(?<![`\w-])docker\s+(?:run|create)\b")
CREATED_VOLUME = re.compile(r"(?<![`\w-])docker\s+volume\s+create\b(.*)")
# tasks/stage-certs.sh makes its first argument, labelled.
STAGED_VOLUME = re.compile(r"""stage-certs\.sh"?\s+"?([^\s"]+)""")
LABEL = re.compile(r"""(?:--label|-l)[ =]"?\$\{?RESTMAIL_LABEL""")
MOUNT = re.compile(r"""(?:^|\s)(?:-v|--volume)[ =]"?([^\s":]+)"?:(\S+)""")
WHOLE_TREE = re.compile(r"""(?:^|\s)(?:-v|--volume)[ =]"?(?:\{\{\.ROOT_DIR\}\}|\$\(pwd\)|\$PWD|\.)"?:/src\b""")

# Made by rest-mail/testbed, which labels its own. Creating them here would race it.
TESTBED_VOLUMES = {"{{.CERTS_VOLUME}}", "{{.FRAG_VOL}}"}
# Made by scripts/source_volume.py, which labels them, and read back into a variable.
HELPER_VOLUMES = {"$volume", "rest-mail-go-modules"}
# Host paths held in a shell variable, such as `-v "$out":/out`.
BIND_VARIABLES = {"$out", "$config", "$decoys"}


def source_files(root):
    tasks = sorted(path for path in (root / "tasks").rglob("*") if path.suffix in {".yml", ".yaml", ".sh"})
    return [root / "chores.yml", *tasks]


def logical_lines(text):
    """Yields (line number, command), with comments gone and continuation lines joined."""
    start, pending = None, ""
    for number, raw in enumerate(text.splitlines(), 1):
        stripped = raw.strip()
        if not pending and (stripped.startswith("#") or stripped.startswith("desc:")):
            continue
        if start is None:
            start = number
        pending += " " + stripped.rstrip("\\").strip()
        if stripped.endswith("\\"):
            continue
        yield start, pending.strip()
        start, pending = None, ""
    if pending:
        yield start, pending.strip()


def is_named_volume(source):
    if source.startswith(("/", ".", "~", "$(", "{{.ROOT_DIR}}")):
        return False
    return source not in BIND_VARIABLES


def problems(files):
    """Returns findings as strings, given {name: text}."""
    found, declared, mounted = [], set(), []
    for name, text in files.items():
        for number, line in logical_lines(text):
            if CREATING.search(line) and not LABEL.search(line):
                found.append(f'{name}:{number}: no --label "$RESTMAIL_LABEL": {line[:100]}')
            if WHOLE_TREE.search(line):
                found.append(f"{name}:{number}: the checkout is bind mounted at /src; use scripts/source_volume.py")
            created = CREATED_VOLUME.search(line)
            if created:
                declared.update(word.strip('"') for word in created.group(1).split())
            declared.update(STAGED_VOLUME.findall(line))
            if RUNNING.search(line):
                mounted += [(name, number, source) for source, _target in MOUNT.findall(line) if is_named_volume(source)]
    for name, number, source in mounted:
        if source not in declared and source not in TESTBED_VOLUMES and source not in HELPER_VOLUMES:
            found.append(f"{name}:{number}: volume {source} has no labelled `docker volume create`")
    return found


def main():
    files = {str(path.relative_to(ROOT)): path.read_text() for path in source_files(ROOT)}
    found = problems(files)
    for finding in found:
        print(finding, file=sys.stderr)
    if found:
        print(f"{len(found)} Docker resources `chore clean` could not find", file=sys.stderr)
        return 1
    print(f"every Docker resource in {len(files)} files carries RESTMAIL_LABEL")
    return 0


if __name__ == "__main__":
    sys.exit(main())
