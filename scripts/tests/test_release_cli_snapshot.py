"""The release snapshot is built locally, and only its download is tried again (#1942).

Release CLI run 42582420200 failed while goreleaser-action downloaded GoReleaser:
`Unexpected HTTP response: 504`, before GoReleaser started. The local gate had no
release step, and the workflow had no retry. These tests prove both, and prove that a
build that fails is a failure the first time and is never called a download fault.
"""

import hashlib
import http.server
import importlib.util
import io
import json
import os
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
import threading
import unittest
from pathlib import Path
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("ci_local", ROOT / "scripts/ci_local.py")
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)

INSTALLER = ROOT / "scripts" / "ci" / "install-goreleaser"
WORKFLOW = ROOT / ".github" / "workflows" / "release-cli.yml"
INSTALL_STEP = 'scripts/ci/install-goreleaser "$RUNNER_TEMP/bin"'
ARCHIVE = "goreleaser_test.tar.gz"
BUILD_FAILURE = "release failed after 3s error=build failed: exit status 1"


def release_job():
    return yaml.safe_load(WORKFLOW.read_text())["jobs"]["release"]


def step_named(prefix):
    return next(step for step in release_job()["steps"] if step.get("name", "").startswith(prefix))


def archive_holding(script):
    """What GoReleaser's own archive holds: the binary and files nobody installs."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
        for name, body in (("goreleaser", script), ("README.md", b"not wanted\n")):
            info = tarfile.TarInfo(name)
            info.size = len(body)
            info.mode = 0o755
            archive.addfile(info, io.BytesIO(body))
    return buffer.getvalue()


class Downloads(http.server.BaseHTTPRequestHandler):
    """Answers 504, as GitHub's download host did, to the first `failures` requests."""

    def do_GET(self):
        server = self.server
        server.requests += 1
        if server.requests <= server.failures:
            self.send_error(504)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(server.body)))
        self.end_headers()
        self.wfile.write(server.body)

    def log_message(self, *_):
        pass


class InstallerTests(unittest.TestCase):
    """The download is the only thing tried again, and it is checked when it arrives."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.ran = self.directory / "goreleaser-ran"
        # Records that it ran, then fails as a broken build does.
        self.body = archive_holding(f"#!/bin/sh\necho ran >> '{self.ran}'\nexit 1\n".encode())

    def install(self, failures=0, retries=2, sha256=None):
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Downloads)
        server.failures, server.requests, server.body = failures, 0, self.body
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        github_path = self.directory / "github-path"
        result = subprocess.run(
            [str(INSTALLER), str(self.directory / "bin")],
            capture_output=True,
            text=True,
            check=False,
            env={
                **os.environ,
                "GITHUB_PATH": str(github_path),
                "INSTALL_GORELEASER_ARCHIVE": ARCHIVE,
                "INSTALL_GORELEASER_BASE_URL": f"http://127.0.0.1:{server.server_address[1]}",
                "INSTALL_GORELEASER_RETRIES": str(retries),
                "INSTALL_GORELEASER_RETRY_DELAY": "0",
                "INSTALL_GORELEASER_SHA256": sha256 or hashlib.sha256(self.body).hexdigest(),
            },
        )
        return result, server.requests, github_path

    def test_two_504s_then_success_installs_after_three_requests(self):
        result, requests, github_path = self.install(failures=2, retries=2)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, 3)
        binary = self.directory / "bin" / "goreleaser"
        self.assertTrue(binary.stat().st_mode & stat.S_IEXEC)
        self.assertEqual([path.name for path in binary.parent.iterdir()], ["goreleaser"])
        self.assertEqual(Path(github_path.read_text().strip()).resolve(), binary.parent.resolve())

    def test_a_lasting_504_fails_with_its_message_and_installs_nothing(self):
        result, requests, github_path = self.install(failures=99, retries=2)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(requests, 3)
        self.assertIn("504", result.stderr)
        self.assertFalse((self.directory / "bin").exists())
        self.assertFalse(github_path.exists())

    def test_one_more_504_than_the_tries_fails(self):
        result, requests, _ = self.install(failures=3, retries=2)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(requests, 3)

    def test_a_wrong_checksum_fails_once_and_installs_nothing(self):
        result, requests, github_path = self.install(sha256="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(requests, 1)
        self.assertIn("checksum", result.stderr)
        self.assertFalse((self.directory / "bin").exists())
        self.assertFalse(github_path.exists())

    def test_it_never_runs_what_it_installs(self):
        result, _, _ = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.ran.exists(), "the installer ran goreleaser, so it could call a build a download fault")

    def test_it_keeps_trying_for_longer_than_the_action_did(self):
        text = INSTALLER.read_text()
        retries = int(re.search(r"INSTALL_GORELEASER_RETRIES:-(\d+)", text).group(1))
        delay = int(re.search(r"INSTALL_GORELEASER_RETRY_DELAY:-(\d+)", text).group(1))
        self.assertGreaterEqual(retries * delay, 120)

    def test_every_platform_has_a_pinned_checksum(self):
        text = INSTALLER.read_text()
        archives = re.findall(r"name=(goreleaser_\w+\.tar\.gz)\n\s+expected=([0-9a-f]{64})", text)
        self.assertEqual(
            sorted(name for name, _ in archives),
            [
                "goreleaser_Darwin_arm64.tar.gz",
                "goreleaser_Darwin_x86_64.tar.gz",
                "goreleaser_Linux_arm64.tar.gz",
                "goreleaser_Linux_x86_64.tar.gz",
            ],
        )
        self.assertRegex(text, r"(?m)^VERSION=\d+\.\d+\.\d+$")


class WorkflowTests(unittest.TestCase):
    """The workflow installs first, builds once, and hides no build failure."""

    def test_no_step_downloads_goreleaser_except_the_installer(self):
        self.assertNotIn("goreleaser/goreleaser-action", WORKFLOW.read_text())

    def test_the_install_step_comes_before_every_goreleaser_step(self):
        steps = release_job()["steps"]
        names = [step.get("name", "") for step in steps]
        install = next(i for i, name in enumerate(names) if name.startswith("Install GoReleaser"))
        for i, step in enumerate(steps):
            if "goreleaser" in step.get("run", "").split() and not names[i].startswith("Install"):
                self.assertGreater(i, install, names[i])
        self.assertEqual(steps[install]["run"].strip(), INSTALL_STEP)
        self.assertNotIn("continue-on-error", steps[install])

    def test_a_build_step_is_one_plain_command_with_no_retry_and_no_hidden_failure(self):
        for prefix, command in (
            ("Snapshot build", "goreleaser release --snapshot --clean"),
            ("Release", "goreleaser release --clean"),
        ):
            step = step_named(prefix)
            with self.subTest(step=prefix):
                self.assertEqual(step["run"].strip(), command)
                self.assertNotIn("continue-on-error", step)
                self.assertNotRegex(json.dumps(step), r"retry|attempt|\|\||until|while|for ")

    def test_the_pull_request_runs_when_the_installer_changes(self):
        paths = yaml.safe_load(WORKFLOW.read_text())[True]["pull_request"]["paths"]
        self.assertIn("scripts/ci/install-goreleaser", paths)

    def test_a_failed_snapshot_build_runs_once_and_fails(self):
        """The step's own text, with a goreleaser that fails the way a broken config does."""
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            calls = directory / "calls"
            fake = directory / "bin" / "goreleaser"
            fake.parent.mkdir()
            fake.write_text(f"#!/bin/sh\necho \"$*\" >> '{calls}'\necho '{BUILD_FAILURE}' >&2\nexit 1\n")
            fake.chmod(0o755)
            step = step_named("Snapshot build")
            result = subprocess.run(
                ["bash", "-e", "-c", step["run"]],
                capture_output=True,
                text=True,
                check=False,
                env={**os.environ, **step["env"], "PATH": f"{fake.parent}:{os.environ['PATH']}"},
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(calls.read_text().splitlines(), ["release --snapshot --clean"])
            self.assertIn("build failed", result.stderr)


class SnapshotCoverageTests(unittest.TestCase):
    """`chore ci:local` notices a release workflow that moved and a release that is incomplete."""

    def setUp(self):
        self.workflow = yaml.safe_load(WORKFLOW.read_text())
        self.coverage = json.loads((ROOT / "scripts/ci-local-coverage.json").read_text())

    def test_the_current_release_workflow_is_covered(self):
        gate.check_coverage(self.workflow, self.coverage, gate.RELEASE_JOBS)

    def test_the_snapshot_and_the_install_run_in_the_local_step(self):
        entries = self.coverage["release-cli"]
        for name in (step_named("Snapshot build")["name"], step_named("Install GoReleaser")["name"]):
            self.assertEqual(entries[name]["local"], "GoReleaser snapshot")
        self.assertIn("GoReleaser snapshot", gate.STEP_NAMES)

    def test_editing_a_release_step_without_the_manifest_is_red(self):
        for prefix in ("Snapshot build", "Install GoReleaser", "Release"):
            workflow = yaml.safe_load(WORKFLOW.read_text())
            step = next(s for s in workflow["jobs"]["release"]["steps"] if s.get("name", "").startswith(prefix))
            step["run"] += " --changed"
            with self.subTest(step=prefix), self.assertRaisesRegex(RuntimeError, "CI drift: release-cli/"):
                gate.check_coverage(workflow, self.coverage, gate.RELEASE_JOBS)

    def test_a_new_release_step_is_red(self):
        self.workflow["jobs"]["release"]["steps"].append({"name": "New release gate", "run": "true"})
        with self.assertRaisesRegex(RuntimeError, "CI drift: release-cli/New release gate"):
            gate.check_coverage(self.workflow, self.coverage, gate.RELEASE_JOBS)

    def test_a_removed_release_step_is_red(self):
        self.workflow["jobs"]["release"]["steps"].pop()
        with self.assertRaisesRegex(RuntimeError, "removed/renamed steps in release-cli"):
            gate.check_coverage(self.workflow, self.coverage, gate.RELEASE_JOBS)


class SnapshotStepTests(unittest.TestCase):
    """The gate step: a build that fails fails, once; a missing download is not a pass."""

    def setUp(self):
        self.COMPLETE = [
            "restmail_0.0.0-SNAPSHOT-abc" + suffix
            for suffix in gate.RELEASE_ARCHIVES + gate.RELEASE_PACKAGES
        ] + ["checksums.txt", "artifacts.json"]
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / "root"
        self.root.mkdir()
        self.cache = Path(temporary.name) / "cache"
        self.cache.mkdir()
        self.fake = Path(temporary.name) / "goreleaser"
        self.calls = Path(temporary.name) / "calls"
        self.local = gate.Gate(self.root, self.cache, self.cache, False)
        self.local.release_workflow = yaml.safe_load(WORKFLOW.read_text())

    def fake_goreleaser(self, files=None, status=0):
        files = self.COMPLETE if files is None else files
        creates = "".join(f"touch '{self.root}/dist/{name}'\n" for name in files)
        self.fake.write_text(
            "#!/bin/sh\n"
            f"echo \"$GORELEASER_CURRENT_TAG $*\" >> '{self.calls}'\n"
            f"mkdir -p '{self.root}/dist'\n{creates}"
            f"[ {status} -eq 0 ] || echo '{BUILD_FAILURE}' >&2\nexit {status}\n"
        )
        self.fake.chmod(0o755)

    def snapshot(self):
        with patch.object(self.local, "goreleaser", return_value=self.fake):
            return self.local.goreleaser_snapshot()

    def test_it_runs_the_workflows_command_and_tag(self):
        self.fake_goreleaser()
        self.snapshot()
        self.assertEqual(self.calls.read_text().splitlines(), ["v0.0.0 release --snapshot --clean"])
        self.assertFalse((self.root / "dist").exists())

    def test_a_failing_build_fails_once_and_leaves_no_dist(self):
        self.fake_goreleaser(files=["checksums.txt"], status=1)
        with self.assertRaises(subprocess.CalledProcessError):
            self.snapshot()
        self.assertEqual(len(self.calls.read_text().splitlines()), 1)
        self.assertFalse((self.root / "dist").exists())

    def test_a_snapshot_missing_a_target_is_red(self):
        for missing in (gate.RELEASE_ARCHIVES[0], gate.RELEASE_PACKAGES[-1], "checksums.txt"):
            self.fake_goreleaser(files=[n for n in self.COMPLETE if not n.endswith(missing)])
            with self.subTest(missing=missing), self.assertRaisesRegex(RuntimeError, "incomplete"):
                self.snapshot()

    def test_a_download_that_never_arrived_is_could_not_run_not_a_pass(self):
        (self.root / "scripts/ci").mkdir(parents=True)
        shutil.copy(INSTALLER, self.root / "scripts/ci/install-goreleaser")
        local = gate.Gate(self.root, self.cache, self.cache, False)
        installed = subprocess.CalledProcessError(22, "install-goreleaser")
        with patch.object(local, "run", side_effect=installed), self.assertRaisesRegex(
            gate.CouldNotRun, "was not downloaded"
        ):
            local.goreleaser()

    def test_the_local_step_comes_after_go_build_and_before_lint(self):
        names = list(gate.STEP_NAMES)
        self.assertEqual(names.index("GoReleaser snapshot"), names.index("go build") + 1)
        self.assertEqual(len(self.local.steps()), len(names))

    def test_a_snapshot_with_every_target_has_no_problems(self):
        with tempfile.TemporaryDirectory() as temporary:
            for name in self.COMPLETE:
                (Path(temporary) / name).touch()
            self.assertEqual(gate.release_snapshot_problems(temporary), [])
        self.assertTrue(gate.release_snapshot_problems(self.root / "nothing"))


if __name__ == "__main__":
    unittest.main()
