"""The chart job installs its pinned yq without relying on runner image tools."""

import hashlib
import http.server
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
INSTALLER = ROOT / "scripts/ci/install-yq"
PAYLOAD = b"#!/bin/sh\nprintf 'yq (https://github.com/mikefarah/yq/) version v4.46.1\\n'\n"


class BinaryRelease(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(self.server.payload)

    def log_message(self, *_):
        pass


class YqInstallTests(unittest.TestCase):
    def test_chart_job_bootstraps_yq_before_rendering(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        steps = workflow["jobs"]["helm"]["steps"]
        render = next(index for index, step in enumerate(steps)
                      if step.get("run") == "helm/restmail/tests/render-test.sh")
        installs = [step for step in steps[:render]
                    if step.get("run") == 'bash scripts/ci/install-yq "$RUNNER_TEMP/bin"']
        self.assertEqual(len(installs), 1, "the private runner does not provide yq")
        self.assertNotIn("if", installs[0])

    def run_installer(self, payload=PAYLOAD, checksum=None):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        directory = Path(temporary.name)
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), BinaryRelease)
        server.payload = payload
        self.addCleanup(server.server_close)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.shutdown)
        binary_directory = directory / "bin with spaces"
        github_path = directory / "github-path"
        result = subprocess.run(
            ["bash", str(INSTALLER), str(binary_directory)],
            capture_output=True, text=True, timeout=30,
            env={**os.environ, "GITHUB_PATH": str(github_path),
                 "INSTALL_YQ_BASE_URL": f"http://127.0.0.1:{server.server_port}",
                 "INSTALL_YQ_SHA256": checksum or hashlib.sha256(payload).hexdigest()},
        )
        return result, binary_directory, github_path

    def test_verified_binary_is_executable_and_available_to_later_steps(self):
        result, directory, github_path = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(github_path.read_text(), f"{directory}\n")
        version = subprocess.check_output([str(directory / "yq"), "--version"], text=True)
        self.assertIn("v4.46.1", version)

    def test_corrupt_download_is_never_installed_or_executed(self):
        result, directory, github_path = self.run_installer(checksum="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum", result.stderr)
        self.assertFalse((directory / "yq").exists())
        self.assertFalse(github_path.exists())

    def test_a_verified_but_broken_binary_does_not_reach_later_steps(self):
        result, _, github_path = self.run_installer(payload=b"#!/bin/sh\nexit 23\n")
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertFalse(github_path.exists())
