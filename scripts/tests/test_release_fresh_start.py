"""Disposable release-baseline orchestration must not touch unrelated resources."""

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("release_fresh_start", SCRIPTS / "release_fresh_start.py")
fresh = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fresh)


class ReleaseFreshStartTests(unittest.TestCase):
    def test_endpoint_accepts_only_loopback_publication(self):
        with patch.object(fresh.evidence, "run", return_value=SimpleNamespace(stdout="127.0.0.1:55504\n")):
            self.assertEqual(fresh.endpoint("owned-api", 8080), "http://127.0.0.1:55504")
        for output in ("", "0.0.0.0:8080", "[::]:8080"):
            with self.subTest(output=output), patch.object(fresh.evidence, "run", return_value=SimpleNamespace(stdout=output)):
                with self.assertRaises(ValueError):
                    fresh.endpoint("owned-api", 8080)

    def test_ready_probe_requires_http_200(self):
        response = SimpleNamespace(status=200)
        with patch.object(fresh.urllib.request, "urlopen") as opening:
            opening.return_value.__enter__.return_value = response
            fresh.wait_http("http://127.0.0.1:55504/api/health")
            opening.assert_called_once()

    def test_existing_network_is_not_removed_after_failed_creation(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / "builds.json").write_text(json.dumps({"commit": "a" * 40, "images": {name: "sha256:" + "a" * 64 for name in fresh.evidence.RELEASE_IMAGES}}))
            with patch.object(fresh.evidence, "run", side_effect=subprocess.CalledProcessError(1, ["docker", "network", "create"])), patch.object(fresh.subprocess, "run") as commands:
                self.assertEqual(fresh.main(["--directory", str(directory), "--project", "restmail-owned-proof"]), 1)
                commands.assert_not_called()

    def test_https_front_uses_the_pinned_release_nginx_base(self):
        self.assertIn("FROM " + fresh.FRONT_IMAGE + " AS prod", (fresh.evidence.ROOT / "admin/Dockerfile").read_text())


if __name__ == "__main__":
    unittest.main()
