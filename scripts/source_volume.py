#!/usr/bin/env python3
"""Copy this checkout into a new Docker volume, and fill the Go modules volume (#1963).

    volume=$(python3 scripts/source_volume.py create)
    trap 'docker volume rm -f "$volume"' EXIT
    docker run -v "$volume:/src" -w /src -v rest-mail-go-modules:/go/pkg/mod:ro golang go ...

On Docker Desktop every file a container reads through a bind mount keeps a file
open on the Mac until the guest's cache lets go, deleted worktrees included. A
container that only compiles the tree has no need of that, so it gets a copy.

The copy holds what git would commit: tracked files as they are on disk, plus
untracked files that are not ignored. Both volumes carry RESTMAIL_LABEL, so a
killed run's copy goes with `chore clean`. The source volume is one per call, and
the caller removes it. The modules volume stays between runs and is shared: `go mod
download` does nothing when they are already there. It needs the network, which is
why it runs here rather than in the container on the mailnet, which has none.
"""

import os
import secrets
import subprocess
import sys
import tarfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ALPINE_IMAGE = "alpine:3.20"
GO_IMAGE = "golang:1.26.10"
# A private topology has a modules volume of its own (#2062), so the label on it is the run's.
MODULES_VOLUME = os.environ.get("RESTMAIL_MODULES_VOLUME") or "rest-mail-go-modules"
DEFAULT_LABEL = "rest-mail.run=local"


def label():
    return os.environ.get("RESTMAIL_LABEL") or DEFAULT_LABEL


def docker(*arguments, **options):
    # stdout carries only the volume's name, for the caller to read.
    return subprocess.run(["docker", *arguments], check=True, stdout=sys.stderr, **options)


def tree_files(root):
    """The files of the checkout, relative to it, skipping tracked files deleted on disk."""
    # A checkout mounted into a Linux runner belongs to another user, which git refuses.
    listing = subprocess.run(
        ["git", "-c", f"safe.directory={root}", "-C", str(root), "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        check=True,
        stdout=subprocess.PIPE,
    ).stdout
    names = sorted({name for name in listing.decode("utf-8", "surrogateescape").split("\0") if name})
    return [name for name in names if os.path.lexists(root / name)]


def write_tree(root, stream):
    with tarfile.open(fileobj=stream, mode="w|") as archive:
        for name in tree_files(root):
            archive.add(root / name, arcname=name, recursive=False)


def copy_tree(root, volume):
    unpack = subprocess.Popen(
        ["docker", "run", "--rm", "-i", "--label", label(), "--network", "none",
         "-v", f"{volume}:/src", ALPINE_IMAGE, "tar", "-xf", "-", "-C", "/src"],
        stdin=subprocess.PIPE,
        stdout=sys.stderr,
    )
    try:
        write_tree(root, unpack.stdin)
    finally:
        unpack.stdin.close()
    if unpack.wait() != 0:
        raise subprocess.CalledProcessError(unpack.returncode, "docker run tar")


def create(root):
    volume = f"rest-mail-src-{secrets.token_hex(6)}"
    docker("volume", "create", "--label", label(), volume)
    try:
        copy_tree(root, volume)
        docker("volume", "create", "--label", label(), MODULES_VOLUME)
        docker("run", "--rm", "--label", label(), "-v", f"{volume}:/src:ro", "-w", "/src",
               "-v", f"{MODULES_VOLUME}:/go/pkg/mod", GO_IMAGE, "go", "mod", "download")
    except (subprocess.CalledProcessError, OSError):
        subprocess.run(["docker", "volume", "rm", "-f", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        raise
    return volume


def main(arguments):
    if arguments != ["create"]:
        print("usage: source_volume.py create", file=sys.stderr)
        return 2
    try:
        print(create(ROOT))
    except subprocess.CalledProcessError as error:
        print(f"source_volume: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
