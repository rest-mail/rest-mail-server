"""Serialize peer restarts of their shared DNS resolver, not peer startup."""

import fcntl
import os
import shutil
import subprocess
import sys
from pathlib import Path
from tempfile import TemporaryDirectory


DOCKER_SHIM = """#!/bin/sh
export PATH="$RESTMAIL_PEER_DOCKER_PATH"
if [ "$#" -eq 2 ] && [ "$1" = restart ] && [ "$2" = "$RESTMAIL_PEER_RESOLVER" ]; then
    exec "$RESTMAIL_PEER_PYTHON" -B "$RESTMAIL_PEER_WRAPPER" --restart-resolver
fi
exec "$RESTMAIL_PEER_DOCKER" "$@"
"""


def main(arguments):
    lock_path = os.environ.get("RESTMAIL_E2E_DNS_LOCK")
    if arguments == ["--restart-resolver"]:
        with open(lock_path, "r+") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            return subprocess.call([
                os.environ["RESTMAIL_PEER_DOCKER"], "restart", os.environ["RESTMAIL_PEER_RESOLVER"],
            ])

    resolver, *command = arguments
    if not lock_path:
        return subprocess.call(command)

    original_path = os.environ["PATH"]
    docker = shutil.which("docker", path=original_path)
    if not docker:
        print("reference startup: docker not found on PATH", file=sys.stderr)
        return 127

    with TemporaryDirectory(prefix="restmail-peer-docker-") as directory:
        shim = Path(directory) / "docker"
        shim.write_text(DOCKER_SHIM, encoding="utf-8")
        shim.chmod(0o700)
        return subprocess.call(command, env={
            **os.environ,
            "PATH": directory + os.pathsep + original_path,
            "RESTMAIL_PEER_DOCKER": str(Path(docker).absolute()),
            "RESTMAIL_PEER_DOCKER_PATH": original_path,
            "RESTMAIL_PEER_PYTHON": sys.executable,
            "RESTMAIL_PEER_RESOLVER": resolver,
            "RESTMAIL_PEER_WRAPPER": str(Path(__file__).resolve()),
        })


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
