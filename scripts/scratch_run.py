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

import ctypes
import errno
import json
import os
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time

# How long a signalled command gets to exit before the rest of its group is killed.
GRACE_SECONDS = 10
HANGUP_SIGNALS = (signal.SIGHUP, signal.SIGINT, signal.SIGTERM)


def process_start(pid):
    if sys.platform == "darwin":
        info = ctypes.create_string_buffer(136)
        if ctypes.CDLL(None).proc_pidinfo(
            pid, 3, ctypes.c_uint64(0), info, len(info)
        ) == len(info):
            return ":".join(map(str, struct.unpack_from("=QQ", info, 120)))
        return None
    try:
        with open(f"/proc/{pid}/stat", encoding="utf-8", errors="replace") as stat:
            return stat.read().rsplit(")", 1)[1].split()[19]
    except (OSError, IndexError):
        return None


def wait_without_reaping(pid):
    if hasattr(os, "waitid"):
        os.waitid(os.P_PID, pid, os.WEXITED | os.WNOWAIT)
    else:
        waitid = ctypes.CDLL(None, use_errno=True).waitid
        info = ctypes.create_string_buffer(128)
        while waitid(1, pid, info, 0x04 | 0x20) != 0:
            error = ctypes.get_errno()
            if error != errno.EINTR:
                raise OSError(error, os.strerror(error))


def remove_owned_group(identity):
    pid = identity["pid"]
    try:
        if os.getpgid(pid) != pid or process_start(pid) != identity["start"]:
            return {**identity, "outcome": "identity-mismatch"}
        os.killpg(pid, signal.SIGKILL)
    except ProcessLookupError:
        return {**identity, "outcome": "gone"}
    except PermissionError:
        return {**identity, "outcome": "permission-denied"}
    return {**identity, "outcome": "signalled"}


def remove_when_released(read_end, started_end, directory, receipt):
    """The cleaner: blocks until the pipe closes, then removes the directory. Never returns."""
    os.setsid()
    # Only now can killing the runner's process group not reach this process.
    os.close(started_end)
    for number in HANGUP_SIGNALS:
        signal.signal(number, signal.SIG_IGN)
    # The cleaner must not keep a caller's `| tee` open after the run ends.
    null = os.open(os.devnull, os.O_RDWR)
    for stream in (0, 1, 2):
        os.dup2(null, stream)
    received = b""
    while chunk := os.read(read_end, 64):
        received += chunk
    result = remove_owned_group(json.loads(received)) if received else None
    # A process that was just killed may still be finishing a write.
    for _ in range(25):
        shutil.rmtree(directory, ignore_errors=True)
        if not os.path.exists(directory):
            break
        time.sleep(0.2)
    if receipt and result:
        with open(receipt, "x", encoding="utf-8") as output:
            json.dump(result, output)
    os._exit(0)


def run(command, directory, write_end, release=None):
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

    environment = dict(
        os.environ, GOTMPDIR=directory, TMPDIR=directory, RESTMAIL_SCRATCH_RUN=directory
    )

    def register_group():
        pid = os.getpid()
        start = process_start(pid)
        if start is None:
            os._exit(125)
        os.write(write_end, json.dumps({"pid": pid, "start": start}).encode())

    try:
        # A session of its own, so the cleaner can reach everything it started
        # and a signal to our group does not reach it twice.
        # The new process names itself to the cleaner before it execs. If this
        # process wrote the pid after Popen returned, a SIGKILL in between would
        # leave the command running with nothing that knows it.
        child = subprocess.Popen(
            command,
            env=environment,
            start_new_session=True,
            preexec_fn=register_group,  # noqa: PLW1509 - no threads here
        )
    except OSError as error:
        print(f"scratch_run: cannot run {command[0]}: {error}", file=sys.stderr)
        return 127
    if release is not None:
        wait_without_reaping(child.pid)
        release()
    status = child.wait()
    signal.alarm(0)
    return 128 - status if status < 0 else status


def main(command):
    if not command:
        print("usage: scratch_run.py <command> [argument ...]", file=sys.stderr)
        return 2
    base = os.environ.get("RESTMAIL_SCRATCH") or tempfile.gettempdir()
    os.makedirs(base, exist_ok=True)
    # Short on purpose: a Unix socket path under it has about 100 bytes on macOS.
    directory = tempfile.mkdtemp(prefix="gotmp.", dir=base)
    receipt = os.environ.get("RESTMAIL_PROCESS_CLEANUP_RECEIPT")
    if receipt:
        receipt = os.path.abspath(receipt)

    read_end, write_end = os.pipe()
    started_read, started_write = os.pipe()
    cleaner = os.fork()
    if cleaner == 0:
        os.close(write_end)
        os.close(started_read)
        remove_when_released(read_end, started_write, directory, receipt)
    os.close(read_end)
    os.close(started_write)
    # The command must not start while the cleaner can still die with this group.
    os.read(started_read, 1)
    os.close(started_read)

    def release():
        nonlocal write_end
        if write_end is not None:
            os.close(write_end)
            write_end = None
            os.waitpid(cleaner, 0)

    status = 1
    try:
        status = run(command, directory, write_end, release)
    except SystemExit as stop:
        status = stop.code
    finally:
        release()
    return status


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
