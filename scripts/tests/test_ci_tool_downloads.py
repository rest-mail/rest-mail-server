"""A short GitHub or Docker Hub fault does not fail a job that downloads a tool or a base image (#1942)."""

import hashlib
import http.server
import io
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tarfile
import tempfile
import threading
import unittest
from unittest import mock
import urllib.error
import yaml

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import oasdiff_pin
from test_ci_transient_failures import ENABLE_STEP, FAKE_DOCKER, SHIM, WORKFLOWS, jobs, step_field, steps
import vertrag_pin

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "ci"))
import tool_cache

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
    """Answers `status` (504) to the first `failures` requests, then the archive."""

    def do_GET(self):
        server = self.server
        server.requests += 1
        if server.requests <= server.failures:
            self.send_error(getattr(server, "status", 504))
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

    def install(self, failures=0, retries=2, sha256=None, cache=None):
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
                "GOLANGCI_LINT_DOWNLOAD_CACHE": str(cache) if cache else "",
            },
        )
        return result, server.requests, github_path

    def test_archive_cache_miss_hit_and_mismatch(self):
        cache = self.directory / "downloads"
        result, requests, _ = self.install(cache=cache)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, 1)
        result, requests, _ = self.install(cache=cache, failures=99)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, 0)
        archive = next(cache.iterdir())
        archive.write_bytes(b"corrupt archive")
        result, requests, _ = self.install(cache=cache)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, 1)
        self.assertEqual(archive.read_bytes(), self.body)

    def test_bad_cache_and_bad_download_are_never_installed(self):
        cache = self.directory / "downloads"
        cache.mkdir()
        name = f"golangci-lint-{script_value('VERSION')}-{script_value('PLATFORM')}.tar.gz"
        (cache / name).write_bytes(b"corrupt archive")
        result, requests, github_path = self.install(cache=cache, sha256="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(requests, 1)
        self.assertFalse((self.directory / "bin").exists())
        self.assertFalse(github_path.exists())

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


class ArchiveCacheTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
            member = tarfile.TarInfo("oasdiff")
            body = b"#!/bin/sh\necho comparator\n"
            member.size = len(body)
            archive.addfile(member, io.BytesIO(body))
        self.archive = buffer.getvalue()
        self.checksum = hashlib.sha256(self.archive).hexdigest()

    def test_oasdiff_cache_miss_hit_and_mismatch(self):
        cache = self.directory / "downloads"
        with (
            mock.patch.dict(os.environ, {"OASDIFF_DOWNLOAD_CACHE": str(cache)}),
            mock.patch.object(oasdiff_pin.platform, "system", return_value="Linux"),
            mock.patch.object(oasdiff_pin.platform, "machine", return_value="x86_64"),
            mock.patch.dict(oasdiff_pin.CHECKSUMS, {"linux_amd64": self.checksum}),
            mock.patch.object(oasdiff_pin, "fetch", return_value=self.archive) as fetch,
            mock.patch("sys.stdout", io.StringIO()),
        ):
            oasdiff_pin.install(self.directory / "bin")
            fetch.assert_called_once()
            archive = next(cache.iterdir())
            self.assertEqual(archive.read_bytes(), self.archive)
            fetch.reset_mock(side_effect=True)
            fetch.side_effect = AssertionError("release outage must not reach the network")
            oasdiff_pin.install(self.directory / "bin")
            fetch.assert_not_called()
            archive.write_bytes(b"corrupt archive")
            fetch.side_effect = None
            oasdiff_pin.install(self.directory / "bin")
            fetch.assert_called_once()
            self.assertEqual(archive.read_bytes(), self.archive)
            archive.write_bytes(b"corrupt archive")
            fetch.return_value = b"bad download"
            with self.assertRaisesRegex(ValueError, "pinned release checksum"):
                oasdiff_pin.install(self.directory / "uninstalled")
            self.assertFalse((self.directory / "uninstalled").exists())

    def test_base_tip_installer_keeps_its_own_pin_and_verification(self):
        source = self.directory / "oasdiff_pin.py"
        source.write_text(
            "import hashlib, pathlib\n"
            "VERSION = 'base-version'\n"
            f"CHECKSUMS = {{'linux_amd64': {self.checksum!r}}}\n"
            "def fetch(url):\n"
            "    raise AssertionError('release download outage')\n"
            "def install(directory):\n"
            "    archive = fetch('https://example.test/oasdiff_base-version_linux_amd64.tar.gz')\n"
            "    assert hashlib.sha256(archive).hexdigest() == CHECKSUMS['linux_amd64']\n"
            "    pathlib.Path(directory).write_bytes(archive)\n"
        )
        cache = self.directory / "downloads"
        cache.mkdir()
        archive = cache / "oasdiff_base-version_linux_amd64.tar.gz"
        archive.write_bytes(self.archive)
        target = self.directory / "verified"
        with mock.patch.dict(os.environ, {"OASDIFF_DOWNLOAD_CACHE": str(cache)}):
            self.assertEqual(tool_cache.install_base_oasdiff(str(source), str(target)), 0)
            self.assertEqual(target.read_bytes(), self.archive)
            archive.write_bytes(b"corrupt cache")
            with self.assertRaisesRegex(AssertionError, "outage"):
                tool_cache.install_base_oasdiff(str(source), str(target))

    def test_cache_keys_include_version_platform_and_checksum_without_network(self):
        with (
            mock.patch.object(tool_cache.platform, "system", return_value="Linux"),
            mock.patch.object(tool_cache.platform, "machine", return_value="x86_64"),
            mock.patch("urllib.request.urlopen", side_effect=AssertionError("network")),
        ):
            for tool, version, checksum in (
                ("golangci-lint", script_value("VERSION"), script_value("SHA256")),
                ("vertrag", *vertrag_pin.pinned(Path(vertrag_pin.PIN_FILE).read_text())),
                ("oasdiff", oasdiff_pin.VERSION, oasdiff_pin.CHECKSUMS["linux_amd64"]),
            ):
                with self.subTest(tool=tool):
                    key = tool_cache.cache_key(tool, ROOT / "scripts")
                    self.assertIn(f"-{tool}-{version}-linux", key)
                    self.assertTrue(key.endswith("-" + checksum))
            source = self.directory / "oasdiff_pin.py"
            source.write_text("VERSION = 'next'\nCHECKSUMS = {'linux_amd64': 'new-pin'}\n")
            changed = tool_cache.cache_key("oasdiff", self.directory)
            self.assertEqual(changed, "pinned-tool-v1-oasdiff-next-linux_amd64-new-pin")

    def test_every_workflow_install_has_a_cache_step_in_the_same_job(self):
        found = []
        for workflow in WORKFLOWS.glob("*.yml"):
            for name, job in yaml.safe_load(workflow.read_text())["jobs"].items():
                available = set()
                for step in job.get("steps", []):
                    if step.get("uses") == "./.github/actions/cache-pinned-tool":
                        available.add(step["with"]["tool"])
                    run = step.get("run", "")
                    for tool, marker in (
                        ("golangci-lint", "scripts/ci/install-golangci-lint"),
                        ("oasdiff", "oasdiff_pin.py"),
                        ("vertrag", "vertrag_pin.py"),
                    ):
                        if marker not in run or not re.search(r"\b(install|ensure|install-base-oasdiff)\b", run):
                            continue
                        with self.subTest(workflow=workflow.name, job=name, tool=tool):
                            self.assertIn(tool, available)
                            self.assertEqual(step["env"]["GITHUB_TOKEN"], "${{ github.token }}")
                        found.append((workflow.name, name, tool))
        self.assertEqual(len(found), 9)
        seed = jobs(WORKFLOWS / "ci.yml")["seed-caches"]
        self.assertIn("if: github.event_name == 'push'", seed)
        for tool in ("golangci-lint", "oasdiff", "vertrag"):
            self.assertIn(("ci.yml", "seed-caches", tool), found)
        action = (ROOT / ".github/actions/cache-pinned-tool/action.yml").read_text()
        self.assertRegex(action, r"uses: actions/cache@[0-9a-f]{40}")
        self.assertNotIn("restore-keys:", action)
        self.assertIn("runner.os", action)
        self.assertIn("runner.arch", action)

    def test_local_cache_actions_survive_every_workspaces_checkout_ref(self):
        for workflow in WORKFLOWS.glob("*.yml"):
            for name, job in yaml.safe_load(workflow.read_text())["jobs"].items():
                workspace_ref = None
                materialized = False
                for step in job.get("steps", []):
                    if step.get("uses", "").startswith("actions/checkout@"):
                        checkout = step.get("with", {})
                        if checkout.get("path", ".") == ".":
                            workspace_ref = checkout.get("ref", "workflow-ref")
                            materialized = False
                    if 'git show "$BASE_SHA:$path" > "$path"' in step.get("run", ""):
                        self.assertEqual(
                            step["env"]["BASE_SHA"],
                            "${{ github.event.pull_request.base.sha }}",
                        )
                        for path in (
                            ".github/actions/cache-pinned-tool/action.yml",
                            "scripts/ci/tool_cache.py",
                            "scripts/ci/github_release.py",
                        ):
                            self.assertIn(path, step["run"])
                        materialized = True
                    if step.get("uses") == "./.github/actions/cache-pinned-tool":
                        with self.subTest(workflow=workflow.name, job=name, step=step["name"]):
                            self.assertIsNotNone(workspace_ref)
                            if workspace_ref not in (
                                "workflow-ref",
                                "${{ github.event.merge_group.head_sha }}",
                            ):
                                self.assertTrue(materialized, "an older head may lack cache support")


class PinnedDownloadTests(unittest.TestCase):
    """The installers' own fetch, against a real server, as `HTTP Error 504` hit it."""

    def serve(self, failures, status=504):
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Release)
        server.failures, server.requests, server.body, server.status = failures, 0, b"archive", status
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        return server, f"http://127.0.0.1:{server.server_address[1]}/archive"

    def each_tool(self):
        for module in (vertrag_pin, oasdiff_pin):
            with self.subTest(module.__name__), mock.patch.object(module.time, "sleep"), \
                    mock.patch("sys.stderr", io.StringIO()):
                yield module

    def test_a_504_is_tried_again(self):
        for module in self.each_tool():
            server, url = self.serve(failures=len(module.RETRY_DELAYS))
            self.assertEqual(module.fetch(url), b"archive")
            self.assertEqual(server.requests, len(module.RETRY_DELAYS) + 1)

    def test_a_lasting_504_still_fails(self):
        for module in self.each_tool():
            server, url = self.serve(failures=99)
            with self.assertRaises(urllib.error.HTTPError):
                module.fetch(url)
            self.assertEqual(server.requests, len(module.RETRY_DELAYS) + 1)

    def test_a_missing_release_is_not_tried_again(self):
        for module in self.each_tool():
            server, url = self.serve(failures=99, status=404)
            with self.assertRaises(urllib.error.HTTPError):
                module.fetch(url)
            self.assertEqual(server.requests, 1)

    def test_nothing_but_the_installed_path_reaches_stdout(self):
        for module in self.each_tool():
            _, url = self.serve(failures=1)
            with mock.patch("sys.stdout", io.StringIO()) as stdout:
                module.fetch(url)
            self.assertEqual(stdout.getvalue(), "")

    def test_the_kind_download_keeps_trying(self):
        text = (WORKFLOWS / "cluster-e2e.yml").read_text()
        line = next(line for line in text.splitlines() if "kubernetes-sigs/kind/releases/download" in line)
        self.assertIn("--retry-all-errors", line)


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
