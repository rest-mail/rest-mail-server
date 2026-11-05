"""A test run removes its temporary directory, however it ends (#1963)."""

import importlib.util
import os
import re
import select
import shutil
import signal
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/scratch_run.py"

# Writes a file into the directory it was given, records its pid, then waits to be killed.
HOLD = textwrap.dedent("""
    import os, signal, sys, time
    signal.signal(signal.SIGINT, signal.SIG_DFL)
    directory = os.environ["GOTMPDIR"]
    open(os.path.join(directory, "work"), "w").close()
    with open(sys.argv[1], "w") as ready:
        ready.write(str(os.getpid()) + " " + directory)
    time.sleep(120)
""")

GO_TEST = textwrap.dedent("""
    package hold

    import (
    \t"os"
    \t"testing"
    \t"time"
    )

    func TestHold(t *testing.T) {
    \tdirectory := t.TempDir()
    \tif err := os.WriteFile(os.Getenv("HOLD_READY"), []byte(directory), 0o644); err != nil {
    \t\tt.Fatal(err)
    \t}
    \ttime.Sleep(2 * time.Minute)
    }
""")


# Long enough to build the standard library with an empty cache on a loaded machine.
GO_TEST_STARTS_SECONDS = 600


def wait_for(condition, seconds=30):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if condition():
            return True
        time.sleep(0.05)
    return False


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


class ScratchRunTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.temp = Path(temp.name).resolve()
        self.base = self.temp / "scratch"
        self.ready = self.temp / "ready"

    def start(self, *command, output=None, **environment):
        process = subprocess.Popen(
            [sys.executable, "-B", str(SCRIPT), *command],
            env=dict(os.environ, RESTMAIL_SCRATCH=str(self.base), **environment),
            start_new_session=True,
            stdout=output,
            stderr=subprocess.STDOUT if output else None,
        )
        self.addCleanup(process.wait)
        self.addCleanup(lambda: process.poll() is None and process.kill())
        return process

    def wait_for_go_test(self, process, log):
        """Whether go test wrote its file; failing says why, as soon as that is known."""
        start = time.monotonic()
        def state():
            return (
                f"go is {shutil.which('go')}, ran for {time.monotonic() - start:.0f} s, "
                f"exit code {process.poll()}, output:\n{log.read_text()}"
            )

        def started():
            return self.ready.exists() and self.ready.read_text()

        def over():
            return started() or process.poll() is not None

        if not wait_for(over, GO_TEST_STARTS_SECONDS):
            message = f"go test never started: {state()}"
            # The other subtest must not wait behind this one.
            process.kill()
            self.fail(message)
        if not started():
            self.fail(f"go test exited before it started: {state()}")

    def started_with_log(self, code):
        log = self.temp / "command.log"
        with log.open("wb") as output:
            process = self.start(sys.executable, "-c", code, output=output)
        return process, log

    def test_a_go_test_that_exits_is_reported_at_once_with_its_code_and_output(self):
        process, log = self.started_with_log("print('no such package', flush=True); raise SystemExit(3)")
        with self.assertRaises(self.failureException) as failed:
            self.wait_for_go_test(process, log)
        self.assertIn("go test exited before it started", str(failed.exception))
        self.assertIn("exit code 3", str(failed.exception))
        self.assertIn("no such package", str(failed.exception))

    def test_a_go_test_that_never_starts_is_reported_with_what_it_printed(self):
        process, log = self.started_with_log("print('still building', flush=True); import time; time.sleep(300)")
        self.assertTrue(wait_for(lambda: log.read_text()), "the command never printed")
        with mock.patch(f"{__name__}.GO_TEST_STARTS_SECONDS", 1):
            with self.assertRaises(self.failureException) as failed:
                self.wait_for_go_test(process, log)
        self.assertIn("go test never started", str(failed.exception))
        self.assertIn("exit code None", str(failed.exception))
        self.assertIn("still building", str(failed.exception))

    def hold(self):
        process = self.start(sys.executable, "-c", HOLD, str(self.ready))
        self.assertTrue(
            wait_for(lambda: self.ready.exists() and self.ready.read_text()),
            "the command never started",
        )
        pid, directory = self.ready.read_text().split(" ", 1)
        return process, int(pid), Path(directory)

    def assert_nothing_left(self, pid=None):
        self.assertTrue(
            wait_for(lambda: not list(self.base.iterdir())),
            f"left behind: {list(self.base.iterdir())}",
        )
        if pid is not None:
            self.assertTrue(
                wait_for(lambda: not alive(pid)), "the command outlived its run"
            )

    def test_the_directory_is_the_commands_own_and_under_scratch(self):
        code = "import os; print(os.environ['GOTMPDIR'], os.environ['TMPDIR'])"
        output = subprocess.run(
            [sys.executable, "-B", str(SCRIPT), sys.executable, "-c", code],
            env=dict(os.environ, RESTMAIL_SCRATCH=str(self.base)),
            capture_output=True,
            text=True,
            check=True,
        ).stdout.split()
        self.assertEqual(output[0], output[1])
        self.assertEqual(Path(output[0]).parent, self.base)
        self.assert_nothing_left()

    def test_exit_status_is_the_commands(self):
        for code in (0, 3):
            result = subprocess.run(
                [
                    sys.executable,
                    "-B",
                    str(SCRIPT),
                    sys.executable,
                    "-c",
                    f"raise SystemExit({code})",
                ],
                env=dict(os.environ, RESTMAIL_SCRATCH=str(self.base)),
                check=False,
            )
            self.assertEqual(result.returncode, code)
        self.assert_nothing_left()

    def test_a_command_that_cannot_start_leaves_nothing(self):
        result = subprocess.run(
            [sys.executable, "-B", str(SCRIPT), str(self.temp / "no-such-program")],
            env=dict(os.environ, RESTMAIL_SCRATCH=str(self.base)),
            capture_output=True,
            check=False,
        )
        self.assertEqual(result.returncode, 127)
        self.assert_nothing_left()

    def test_the_cleaner_can_reach_the_command_as_soon_as_it_exists(self):
        # A run killed once the command is up must not leave it running. So the
        # pipe already names the command when Popen returns, or a SIGKILL in
        # that gap leaves the cleaner with nothing to kill.
        spec = importlib.util.spec_from_file_location("scratch_run", SCRIPT)
        scratch_run = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(scratch_run)
        read_end, write_end = os.pipe()
        self.addCleanup(os.close, read_end)
        self.addCleanup(os.close, write_end)
        handled = (*scratch_run.HANGUP_SIGNALS, signal.SIGALRM)
        for number, handler in [
            (number, signal.getsignal(number)) for number in handled
        ]:
            self.addCleanup(signal.signal, number, handler)
        named = []
        real_popen = subprocess.Popen

        def popen(*args, **kwargs):
            child = real_popen(*args, **kwargs)
            ready = select.select([read_end], [], [], 0)[0]
            named.append((child.pid, os.read(read_end, 64) if ready else b""))
            return child

        with mock.patch.object(scratch_run.subprocess, "Popen", popen):
            self.assertEqual(scratch_run.run(["true"], str(self.temp), write_end), 0)
        self.assertEqual(named[0][1], str(named[0][0]).encode())

    def test_each_way_of_being_killed_leaves_nothing(self):
        for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP, signal.SIGKILL):
            with self.subTest(signal=number.name):
                self.ready.unlink(missing_ok=True)
                process, pid, directory = self.hold()
                self.assertTrue((directory / "work").exists())
                process.send_signal(number)
                self.assert_nothing_left(pid)
                process.wait()

    def test_killing_the_runners_whole_group_leaves_nothing(self):
        process, pid, _directory = self.hold()
        os.killpg(process.pid, signal.SIGKILL)
        self.assert_nothing_left(pid)

    def test_the_cleaner_has_left_the_group_before_the_command_starts(self):
        # A slow cleaner is still in the runner's process group until its setsid
        # runs. Killing that group must not be able to take the cleaner with it.
        launcher = textwrap.dedent(f"""
            import importlib.util, os, sys, time
            spec = importlib.util.spec_from_file_location("scratch_run", {str(SCRIPT)!r})
            scratch_run = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(scratch_run)
            real_setsid = os.setsid
            def slow_setsid():
                time.sleep(1)
                real_setsid()
            os.setsid = slow_setsid
            sys.exit(scratch_run.main(sys.argv[1:]))
        """)
        process = subprocess.Popen(
            [
                sys.executable,
                "-B",
                "-c",
                launcher,
                sys.executable,
                "-c",
                HOLD,
                str(self.ready),
            ],
            env=dict(os.environ, RESTMAIL_SCRATCH=str(self.base)),
            start_new_session=True,
        )
        self.addCleanup(process.wait)
        self.assertTrue(
            wait_for(lambda: self.ready.exists() and self.ready.read_text()),
            "the command never started",
        )
        pid = int(self.ready.read_text().split(" ", 1)[0])
        os.killpg(process.pid, signal.SIGKILL)
        self.assert_nothing_left(pid)

    def test_killing_only_the_command_leaves_nothing(self):
        process, pid, _directory = self.hold()
        os.kill(pid, signal.SIGKILL)
        process.wait()
        self.assertEqual(process.returncode, 128 + signal.SIGKILL)
        self.assert_nothing_left()

    def test_killing_go_test_part_way_leaves_no_temporary_directory(self):
        # Lint runs these tests before it sets Go up. The runner image's own go is on PATH
        # by then, so this runs there too, on whichever version that image has.
        if shutil.which("go") is None:
            self.skipTest("go is not installed")
        module = self.temp / "module"
        module.mkdir()
        (module / "go.mod").write_text("module hold\n\ngo 1.21\n")
        (module / "hold_test.go").write_text(GO_TEST)
        for number in (signal.SIGTERM, signal.SIGKILL):
            with self.subTest(signal=number.name):
                self.ready.unlink(missing_ok=True)
                previous = Path.cwd()
                os.chdir(module)
                self.addCleanup(os.chdir, previous)
                log = self.temp / f"go-test-{number.name}.log"
                with log.open("wb") as output:
                    process = self.start(
                        "go",
                        "test",
                        "-count=1",
                        "-timeout",
                        "15m",
                        ".",
                        output=output,
                        HOLD_READY=str(self.ready),
                    )
                os.chdir(previous)
                self.wait_for_go_test(process, log)
                # go-build* and the test's own TempDir are both under the run's directory.
                self.assertEqual(Path(self.ready.read_text()).parents[2], self.base)
                process.send_signal(number)
                self.assert_nothing_left()
                process.wait()


class ChoresUseItTests(unittest.TestCase):
    def test_every_go_test_on_the_host_goes_through_it(self):
        text = (ROOT / "chores.yml").read_text()
        bare = [line for line in text.splitlines() if re.match(r"\s*- go test\b", line)]
        self.assertEqual(bare, [])
        self.assertGreaterEqual(text.count("scripts/scratch_run.py go test"), 5)

    def test_the_example_pairs_run_goes_through_it_on_its_own(self):
        text = (ROOT / "tests/contract/example_pairs.sh").read_text()
        self.assertIn('scripts/scratch_run.py" "$0" "$@"', text)
        self.assertLess(text.index("scratch_run.py"), text.index("mktemp -d"))


if __name__ == "__main__":
    unittest.main()
