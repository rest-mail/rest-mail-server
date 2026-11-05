"""Live check of `chore clean` and the guest cache drop on a real daemon (#1963).

Opt-in: set RESTMAIL_CLEAN_LIVE=1. Without it the test skips, so the plain `unittest
discover` in Lint stays daemon-free. With it, a missing Docker is a failure.

Every resource here carries RESTMAIL_CLEAN_LIVE_LABEL, by default a label made up for
this run, so nothing another run on the machine made is in scope. The label of the
control resources differs only in its value. The drop of the guest cache acts on the
whole VM, as it does in `chore clean`: it evicts cache and removes nothing.
"""

import json
import os
import platform
import shutil
import signal
import subprocess
import tempfile
import time
import unittest
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LIVE = os.environ.get("RESTMAIL_CLEAN_LIVE") == "1"
VM_PROCESS = "com.apple.Virtualization.VirtualMachine"


def docker(*arguments, check=True):
    return subprocess.run(["docker", *arguments], capture_output=True, text=True, check=check).stdout.split()


@unittest.skipUnless(LIVE, "set RESTMAIL_CLEAN_LIVE=1 to run against a real daemon")
class CleanLiveTests(unittest.TestCase):
    def setUp(self):
        suffix = uuid.uuid4().hex[:8]
        self.label = os.environ.get("RESTMAIL_CLEAN_LIVE_LABEL") or f"rest-mail-clean-test={suffix}"
        self.control_label = self.label + "-control"
        self.prefix = f"restmail-{os.environ.get('RESTMAIL_CLEAN_LIVE_NAME', suffix)}"
        self.addCleanup(self.remove_controls)

    def remove_controls(self):
        for label in (self.label, self.control_label):
            filter_ = f"label={label}"
            ids = docker("ps", "-aq", "--filter", filter_)
            if ids:
                docker("rm", "-fv", *ids, check=False)
            for network in docker("network", "ls", "-q", "--filter", filter_):
                docker("network", "rm", network, check=False)
            for volume in docker("volume", "ls", "-q", "--filter", filter_):
                docker("volume", "rm", "-f", volume, check=False)

    def clean(self, *arguments):
        return subprocess.run(
            ["chore", "clean", *arguments],
            cwd=ROOT,
            env=dict(os.environ, RESTMAIL_LABEL=self.label),
            capture_output=True,
            text=True,
            check=False,
        )

    def labelled(self, label):
        filter_ = f"label={label}"
        return (
            docker("ps", "-aq", "--filter", filter_),
            docker("network", "ls", "-q", "--filter", filter_),
            docker("volume", "ls", "-q", "--filter", filter_),
        )

    def killed_run(self):
        """Leaves what a killed run leaves: its client gone, its resources still there."""
        volume = f"{self.prefix}-cache"
        data = f"{self.prefix}-data"
        docker("volume", "create", "--label", self.label, volume)
        docker("volume", "create", "--label", self.label, "--label", "rest-mail.data=true", data)
        environment = dict(os.environ, RESTMAIL_LABEL=self.label)
        project = f"{self.prefix}-topology"
        subprocess.run(["sh", "tasks/js-filter-network.sh", "up", project], cwd=ROOT, env=environment, check=True)
        # A foreground run, as `chore` holds one, with its client killed part-way.
        name = f"{self.prefix}-run"
        client = subprocess.Popen(
            ["docker", "run", "--rm", "--label", self.label, "--name", name, "--network", f"{project}-js-filter-net",
             "-v", f"{volume}:/cache", "-v", f"{data}:/data", "alpine:3.20", "sleep", "300"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline and not docker("ps", "-q", "--filter", f"name=^/{name}$"):
            time.sleep(0.2)
        client.send_signal(signal.SIGKILL)
        client.wait()
        self.assertEqual(len(docker("ps", "-q", "--filter", f"name=^/{name}$")), 1, "the container should outlive its client")
        # The stopped kind of leftover too.
        docker("create", "--label", self.label, "--name", f"{self.prefix}-stopped", "alpine:3.20", "true")
        return data

    def make_controls(self):
        name = f"{self.prefix}-control"
        docker("volume", "create", "--label", self.control_label, name)
        docker("network", "create", "--label", self.control_label, name)
        docker("create", "--label", self.control_label, "--name", name, "alpine:3.20", "true")
        return self.labelled(self.control_label)

    def test_after_a_killed_run_nothing_labelled_is_left_and_nothing_else_is_touched(self):
        data = self.killed_run()
        controls = self.make_controls()

        result = self.clean()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        containers, networks, volumes = self.labelled(self.label)
        self.assertEqual((containers, networks), ([], []))
        self.assertEqual(volumes, [data], "only the data volume stays by default")
        self.assertIn(data, result.stdout)

        result = self.clean("--volumes")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.labelled(self.label), ([], [], []))
        self.assertEqual(self.labelled(self.control_label), controls)

    @unittest.skipUnless(platform.system() == "Darwin", "only Docker Desktop on a Mac has the VM")
    def test_after_clean_the_vm_holds_no_deleted_file_of_a_removed_tree(self):
        pids = subprocess.run(["pgrep", "-f", VM_PROCESS], capture_output=True, text=True, check=True).stdout.split()
        self.assertTrue(pids, f"no {VM_PROCESS} process")

        def held(path):
            # lsof exits 1 when a process in the list has nothing to report.
            output = subprocess.run(
                ["lsof", "-nP", "-a", "+L1", "-p", ",".join(pids)],
                capture_output=True,
                text=True,
                check=False,
            ).stdout
            return sum(1 for line in output.splitlines() if path in line)

        base = os.environ.get("RESTMAIL_SCRATCH") or tempfile.gettempdir()
        tree = Path(tempfile.mkdtemp(prefix="removed-tree.", dir=base)).resolve()
        self.addCleanup(shutil.rmtree, tree, True)
        for number in range(200):
            (tree / f"file{number}").write_text(json.dumps({"number": number}))
        docker("run", "--rm", "--label", self.label, "-v", f"{tree}:/tree:ro", "alpine:3.20", "sh", "-c", "cat /tree/* >/dev/null")
        shutil.rmtree(tree)
        before = held(str(tree))
        # The positive control: without it, a zero below would prove nothing.
        self.assertGreater(before, 0, "the VM held no deleted file of the tree, so this run shows nothing")

        result = self.clean()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline and held(str(tree)):
            time.sleep(1)
        self.assertEqual(held(str(tree)), 0)


if __name__ == "__main__":
    unittest.main()
