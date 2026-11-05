"""A short GitHub or Docker Hub fault does not fail a job that downloads a tool or a base image (#1942)."""

import hashlib
import http.server
import io
import os
from pathlib import Path
import re
import stat
import subprocess
import tarfile
import tempfile
import threading
import unittest

from test_ci_transient_failures import ENABLE_STEP, FAKE_DOCKER, SHIM, WORKFLOWS, jobs, step_field, steps

ROOT = Path(__file__).resolve().parents[2]
INSTALLER = ROOT / "scripts" / "ci" / "install-golangci-lint"
INSTALL_STEP = 'scripts/ci/install-golangci-lint "$RUNNER_TEMP/bin"'
LINT_ACTION = "golangci/golangci-lint-action@"

# The line job 127193871015 stopped on, 2026-11-05.
BASE_IMAGE_502 = (
    "ERROR: failed to build: failed to solve: golang:1.26.10-alpine@sha256:a2012b4a: "
    "failed to resolve source metadata for docker.io/library/golang:1.26.10-alpine@sha256:a2012b4a: "
    "unexpected status from HEAD request to "
    "https://registry-1.docker.io/v2/library/golang/manifests/sha256:a2012b4a: 502 Bad Gateway"
)


def script_value(name):
    return re.search(rf"(?m)^{name}=(.+)$", INSTALLER.read_text()).group(1)


def release_archive():
    """An archive holding what the release does: the binary, under a named directory."""
    directory = f"golangci-lint-{script_value('VERSION')}-{script_value('PLATFORM')}"
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
        for name, body in (("golangci-lint", b"#!/bin/sh\necho linter\n"), ("README.md", b"not wanted\n")):
            info = tarfile.TarInfo(f"{directory}/{name}")
            info.size = len(body)
            info.mode = 0o755
            archive.addfile(info, io.BytesIO(body))
    return buffer.getvalue()


class Release(http.server.BaseHTTPRequestHandler):
    """Answers 504 to the first `failures` requests, then the archive."""

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
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.body = release_archive()

    def install(self, failures=0, retries=2, sha256=None):
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Release)
        server.failures, server.requests, server.body = failures, 0, self.body
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
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
                "INSTALL_LINT_BASE_URL": f"http://127.0.0.1:{server.server_address[1]}",
                "INSTALL_LINT_RETRIES": str(retries),
                "INSTALL_LINT_RETRY_DELAY": "0",
                "INSTALL_LINT_SHA256": sha256 or hashlib.sha256(self.body).hexdigest(),
            },
        )
        return result, server.requests, github_path

    def test_it_installs_only_the_binary_and_puts_it_on_the_path(self):
        result, requests, github_path = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, 1)
        binary = self.directory / "bin" / "golangci-lint"
        self.assertTrue(binary.stat().st_mode & stat.S_IEXEC)
        self.assertEqual([path.name for path in (self.directory / "bin").iterdir()], ["golangci-lint"])
        self.assertEqual(Path(github_path.read_text().strip()).resolve(), (self.directory / "bin").resolve())

    def test_a_504_is_tried_again(self):
        result, requests, _ = self.install(failures=2, retries=2)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, 3)

    def test_a_lasting_504_fails_and_installs_nothing(self):
        result, requests, github_path = self.install(failures=99, retries=2)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(requests, 3)
        self.assertFalse((self.directory / "bin").exists())
        self.assertFalse(github_path.exists())

    def test_a_wrong_checksum_fails_and_installs_nothing(self):
        result, _, github_path = self.install(sha256="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum", result.stderr)
        self.assertFalse((self.directory / "bin").exists())
        self.assertFalse(github_path.exists())

    def test_it_keeps_trying_for_longer_than_the_action_did(self):
        text = INSTALLER.read_text()
        retries = int(re.search(r"INSTALL_LINT_RETRIES:-(\d+)", text).group(1))
        delay = int(re.search(r"INSTALL_LINT_RETRY_DELAY:-(\d+)", text).group(1))
        self.assertGreaterEqual(retries * delay, 120)


class LintWiringTests(unittest.TestCase):
    def lint_steps(self):
        found = []
        for name, job in jobs(WORKFLOWS / "ci.yml").items():
            listed = steps(job)
            for at, step in enumerate(listed):
                if LINT_ACTION in step:
                    found.append((name, step, listed[:at]))
        return found

    def test_each_use_of_the_action_runs_the_installed_binary(self):
        found = self.lint_steps()
        self.assertGreaterEqual(len(found), 3)
        for name, step, before in found:
            with self.subTest(job=name):
                self.assertIn("install-mode: none", step)
                self.assertTrue(any(step_field(earlier, "run") == INSTALL_STEP for earlier in before))
                checkout = next(i for i, earlier in enumerate(before) if "actions/checkout@" in earlier)
                install = next(i for i, earlier in enumerate(before) if step_field(earlier, "run") == INSTALL_STEP)
                self.assertLess(checkout, install)

    def test_the_stated_version_is_the_installed_one(self):
        for name, step, _ in self.lint_steps():
            with self.subTest(job=name):
                self.assertIn("version: v" + script_value("VERSION"), step)


class BaseImageTests(unittest.TestCase):
    def test_a_502_on_a_base_image_is_retried_through_buildx(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            fake = directory / "docker"
            fake.write_text(FAKE_DOCKER)
            fake.chmod(fake.stat().st_mode | stat.S_IEXEC)
            (directory / "script").write_text(f"1 {BASE_IMAGE_502}\n0\n")
            result = subprocess.run(
                [str(SHIM), "buildx", "build", "--push", "."],
                capture_output=True,
                text=True,
                check=False,
                env={
                    "PATH": f"{SHIM.parent}:{directory}:{os.environ['PATH']}",
                    "SCRIPT": str(directory / "script"),
                    "COUNT": str(directory / "count"),
                    "CALLS": str(directory / "calls"),
                    "DOCKER_RETRY_DELAYS": "0 0 0",
                },
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(len((directory / "calls").read_text().splitlines()), 2)

    def test_the_job_building_our_images_turns_the_retry_on(self):
        job = jobs(WORKFLOWS / "docker.yml")["build-and-push"]
        self.assertIn(ENABLE_STEP, job)
        self.assertLess(job.index("actions/checkout@"), job.index(ENABLE_STEP))
        self.assertLess(job.index(ENABLE_STEP), job.index("docker/build-push-action@"))


if __name__ == "__main__":
    unittest.main()
