"""The chart job installs its pinned promtool, so the alert tests run in CI (#2117).

helm/restmail/tests/render-test.sh runs `promtool test rules` on every alert test
and fails without promtool. Before #2117 it skipped them instead, and CI, which
installed no promtool, never ran one.
"""

import hashlib
import http.server
import io
import os
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile
import threading
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
INSTALLER = ROOT / "scripts/ci/install-promtool"
VERSION = "3.5.0"
PLATFORMS = {
    ("Linux", "x86_64"): "linux-amd64",
    ("Linux", "aarch64"): "linux-arm64",
    ("Linux", "arm64"): "linux-arm64",
    ("Darwin", "x86_64"): "darwin-amd64",
    ("Darwin", "arm64"): "darwin-arm64",
}
WORKING = b"#!/bin/sh\nprintf 'promtool, version 3.5.0\\n'\n"


def release(promtool):
    """A release archive laid out as Prometheus publishes it, holding promtool."""
    name = f"prometheus-{VERSION}.{PLATFORMS[(platform.system(), platform.machine())]}"
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
        for member, body in ((f"{name}/promtool", promtool), (f"{name}/prometheus", b"#!/bin/sh\nexit 0\n")):
            info = tarfile.TarInfo(member)
            info.size = len(body)
            info.mode = 0o755
            archive.addfile(info, io.BytesIO(body))
    return buffer.getvalue()


class Release(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.server.paths.append(self.path)
        self.send_response(200)
        self.end_headers()
        self.wfile.write(self.server.payload)

    def log_message(self, *_):
        pass


@unittest.skipUnless((platform.system(), platform.machine()) in PLATFORMS, "no pinned promtool for this machine")
class PromtoolInstallTests(unittest.TestCase):
    def test_chart_job_bootstraps_promtool_before_rendering(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        steps = workflow["jobs"]["helm"]["steps"]
        render = next(index for index, step in enumerate(steps)
                      if step.get("run") == "helm/restmail/tests/render-test.sh")
        installs = [step for step in steps[:render]
                    if step.get("run") == 'bash scripts/ci/install-promtool "$RUNNER_TEMP/bin"']
        self.assertEqual(len(installs), 1, "render-test.sh fails without promtool")
        self.assertNotIn("if", installs[0])

    def test_publish_job_bootstraps_promtool_before_rendering(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/helm.yml").read_text())
        steps = workflow["jobs"]["publish"]["steps"]
        render = next(index for index, step in enumerate(steps)
                      if "tests/render-test.sh" in step.get("run", ""))
        installs = [step for step in steps[:render]
                    if step.get("run") == 'bash scripts/ci/install-promtool "$RUNNER_TEMP/bin"']
        self.assertEqual(len(installs), 1, "render-test.sh fails without promtool")

    def run_installer(self, payload, checksum=None):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        directory = Path(temporary.name)
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Release)
        server.payload = payload
        server.paths = []
        self.addCleanup(server.server_close)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.shutdown)
        binary_directory = directory / "bin with spaces"
        github_path = directory / "github-path"
        result = subprocess.run(
            ["bash", str(INSTALLER), str(binary_directory)],
            capture_output=True, text=True, timeout=600,
            env={**os.environ, "GITHUB_PATH": str(github_path),
                 "INSTALL_PROMTOOL_BASE_URL": f"http://127.0.0.1:{server.server_port}",
                 "INSTALL_PROMTOOL_SHA256": checksum or hashlib.sha256(payload).hexdigest()},
        )
        return result, binary_directory, github_path, server.paths

    def test_verified_release_installs_promtool_for_later_steps(self):
        result, directory, github_path, paths = self.run_installer(release(WORKING))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(github_path.read_text(), f"{directory}\n")
        self.assertEqual(len(paths), 1)
        self.assertRegex(paths[0], rf"^/v{VERSION}/prometheus-{VERSION}\.[a-z]+-[a-z0-9]+\.tar\.gz$")
        version = subprocess.check_output([str(directory / "promtool"), "--version"], text=True)
        self.assertIn("3.5.0", version)
        self.assertFalse((directory / "prometheus").exists(), "only promtool is installed")

    def test_corrupt_download_is_never_installed_or_executed(self):
        result, directory, github_path, _ = self.run_installer(release(WORKING), checksum="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum", result.stderr)
        self.assertFalse((directory / "promtool").exists())
        self.assertFalse(github_path.exists())

    def test_a_verified_but_broken_binary_does_not_reach_later_steps(self):
        result, _, github_path, _ = self.run_installer(release(b"#!/bin/sh\nexit 23\n"))
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertFalse(github_path.exists())


if __name__ == "__main__":
    unittest.main()
