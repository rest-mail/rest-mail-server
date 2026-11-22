"""The scripts that let CI skip work that cannot change its result (#1896)."""

import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
DEV_GOCACHE = ROOT / "tasks/dev-gocache.sh"


def git(directory, *args):
    subprocess.run(
        ["git", "-c", "user.name=t", "-c", "user.email=t@example.test", *args],
        cwd=directory, check=True, capture_output=True, text=True, timeout=600,
    )


class DevGoCache(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.calls = self.directory / "calls"

    def run_script(self, *args, volume_exists=False, volume="fixture"):
        docker = self.directory / "docker"
        docker.write_text(textwrap.dedent(f"""\
            #!/bin/sh
            echo "$*" >> "{self.calls}"
            case "$1 $2" in
              "volume inspect") exit {0 if volume_exists else 1} ;;
            esac
            echo noise
        """))
        docker.chmod(0o755)
        environment = dict(os.environ, PATH=f"{self.directory}{os.pathsep}{os.environ['PATH']}")
        # A worker that shares the machine sets its own label; these calls check the default.
        environment.pop("RESTMAIL_LABEL", None)
        environment.pop("RESTMAIL_DEV_GOCACHE_VOLUME", None)
        if volume:
            environment["RESTMAIL_DEV_GOCACHE_VOLUME"] = volume
        return subprocess.run(
            [str(DEV_GOCACHE), *args], env=environment, capture_output=True, text=True, timeout=600,
        )

    def test_a_new_volume_is_handed_to_its_user_and_only_the_flag_is_printed(self):
        result = self.run_script("10001")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "-v fixture-10001:/cache\n-e GOPATH=/cache/go -e GOMODCACHE=/cache/go/pkg/mod -e GOCACHE=/cache/build\n")
        calls = self.calls.read_text().splitlines()
        self.assertIn("volume create --label rest-mail.run=local fixture-10001", calls)
        self.assertIn('run --rm --label rest-mail.run=local --network none -v fixture-10001:/writable alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc sh -ec chown "$1:$1" /writable; chmod "$2" /writable _ 10001 0700', calls)

    def test_a_volume_in_use_is_left_alone(self):
        result = self.run_script("0", volume_exists=True)
        self.assertEqual(result.stdout, "-v fixture-0:/cache\n-e GOPATH=/cache/go -e GOMODCACHE=/cache/go/pkg/mod -e GOCACHE=/cache/build\n")
        self.assertEqual(self.calls.read_text().splitlines(), ["volume inspect fixture-0"])

    def test_it_needs_a_user_and_a_volume(self):
        """A user is required; an omitted volume now selects a disk-backed default."""
        with patch.dict(os.environ, {"RESTMAIL_DEV_GOCACHE_VOLUME": "inherited-cache"}):
            self.assertEqual(self.run_script().returncode, 2)
            result = self.run_script("0", volume="")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("-v restmail-local-devcache-0:/cache\n", result.stdout)
            self.assertNotIn("--tmpfs", result.stdout)


if __name__ == "__main__":
    unittest.main()
