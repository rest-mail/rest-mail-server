"""A test run removes its temporary directory, however it ends (#1963)."""

import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from pathlib import Path

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

    def start(self, *command, **environment):
        process = subprocess.Popen(
            [sys.executable, "-B", str(SCRIPT), *command],
            env=dict(os.environ, RESTMAIL_SCRATCH=str(self.base), **environment),
            start_new_session=True,
        )
        self.addCleanup(process.wait)
        self.addCleanup(lambda: process.poll() is None and process.kill())
        return process

    def hold(self):
        process = self.start(sys.executable, "-c", HOLD, str(self.ready))
        self.assertTrue(wait_for(lambda: self.ready.exists() and self.ready.read_text()), "the command never started")
        pid, directory = self.ready.read_text().split(" ", 1)
        return process, int(pid), Path(directory)

    def assert_nothing_left(self, pid=None):
        self.assertTrue(wait_for(lambda: not list(self.base.iterdir())), f"left behind: {list(self.base.iterdir())}")
        if pid is not None:
            self.assertTrue(wait_for(lambda: not alive(pid)), "the command outlived its run")

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
                [sys.executable, "-B", str(SCRIPT), sys.executable, "-c", f"raise SystemExit({code})"],
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

    def test_killing_only_the_command_leaves_nothing(self):
        process, pid, _directory = self.hold()
        os.kill(pid, signal.SIGKILL)
        process.wait()
        self.assertEqual(process.returncode, 128 + signal.SIGKILL)
        self.assert_nothing_left()

    def test_killing_go_test_part_way_leaves_no_temporary_directory(self):
        # Lint runs these tests before it sets Go up; the test jobs and the local gate have it.
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
                process = self.start("go", "test", "-count=1", "-timeout", "3m", ".", HOLD_READY=str(self.ready))
                os.chdir(previous)
                self.assertTrue(wait_for(lambda: self.ready.exists() and self.ready.read_text(), 180), "go test never started")
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
