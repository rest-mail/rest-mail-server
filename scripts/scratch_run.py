#!/usr/bin/env python3
"""Run one command with a temporary directory of its own, and always remove it (#1963).

    python3 scripts/scratch_run.py go test ./internal/...

A killed `go test` leaves its `go-build*` directories behind, and a test binary
leaves what it made under the system temporary directory. So the command runs
with GOTMPDIR and TMPDIR pointing at a new directory under RESTMAIL_SCRATCH, or
under the system temporary directory when that is unset.

A trap cannot do this, because nothing runs after a SIGKILL. A second process
does the removing. It waits on a pipe that only this process holds open, so the
pipe ends however this process ends, SIGKILL included. It then kills what is
left of the command and removes the directory. A normal exit takes the same path.

RESTMAIL_SCRATCH_RUN tells a script that runs itself through this that it already has.
The exit status is the command's, or 128 + the signal that ended it.
"""

import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time

# How long a signalled command gets to exit before the rest of its group is killed.
GRACE_SECONDS = 10
HANGUP_SIGNALS = (signal.SIGHUP, signal.SIGINT, signal.SIGTERM)


def remove_when_released(read_end, directory):
    """The cleaner: blocks until the pipe closes, then removes the directory. Never returns."""
    os.setsid()
    for number in HANGUP_SIGNALS:
        signal.signal(number, signal.SIG_IGN)
    # The cleaner must not keep a caller's `| tee` open after the run ends.
    null = os.open(os.devnull, os.O_RDWR)
    for stream in (0, 1, 2):
        os.dup2(null, stream)
    received = b""
    while chunk := os.read(read_end, 64):
        received += chunk
    if received:
        try:
            os.killpg(int(received), signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
    # A process that was just killed may still be finishing a write.
    for _ in range(25):
        shutil.rmtree(directory, ignore_errors=True)
        if not os.path.exists(directory):
            break
        time.sleep(0.2)
    os._exit(0)


def run(command, directory, write_end):
    """Runs the command in its own process group and returns its exit status."""
    child = None

    def kill_group(_number, _frame):
        if child is not None:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass

    def forward(number, _frame):
        if child is None:
            raise SystemExit(128 + number)
        try:
            os.killpg(child.pid, number)
        except ProcessLookupError:
            pass
        signal.alarm(GRACE_SECONDS)

    signal.signal(signal.SIGALRM, kill_group)
    for number in HANGUP_SIGNALS:
        signal.signal(number, forward)

    environment = dict(os.environ, GOTMPDIR=directory, TMPDIR=directory, RESTMAIL_SCRATCH_RUN=directory)
    try:
        # A session of its own, so the cleaner can reach everything it started
        # and a signal to our group does not reach it twice.
        child = subprocess.Popen(command, env=environment, start_new_session=True)
    except OSError as error:
        print(f"scratch_run: cannot run {command[0]}: {error}", file=sys.stderr)
        return 127
    os.write(write_end, str(child.pid).encode())
    status = child.wait()
    return 128 - status if status < 0 else status


def main(command):
    if not command:
        print("usage: scratch_run.py <command> [argument ...]", file=sys.stderr)
        return 2
    base = os.environ.get("RESTMAIL_SCRATCH") or tempfile.gettempdir()
    os.makedirs(base, exist_ok=True)
    # Short on purpose: a Unix socket path under it has about 100 bytes on macOS.
    directory = tempfile.mkdtemp(prefix="gotmp.", dir=base)

    read_end, write_end = os.pipe()
    cleaner = os.fork()
    if cleaner == 0:
        os.close(write_end)
        remove_when_released(read_end, directory)
    os.close(read_end)

    status = 1
    try:
        status = run(command, directory, write_end)
    except SystemExit as stop:
        status = stop.code
    finally:
        os.close(write_end)
        os.waitpid(cleaner, 0)
    return status


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
