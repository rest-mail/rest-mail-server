"""The scripts that let CI skip work that cannot change its result (#1896)."""

import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

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
        if volume:
            environment["RESTMAIL_DEV_GOCACHE_VOLUME"] = volume
        return subprocess.run(
            [str(DEV_GOCACHE), *args], env=environment, capture_output=True, text=True, timeout=600,
        )

    def test_a_new_volume_is_handed_to_its_user_and_only_the_flag_is_printed(self):
        result = self.run_script("10001")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "-v fixture-10001:/tmp/go-build-cache\n")
        calls = self.calls.read_text().splitlines()
        self.assertIn("volume create --label rest-mail.run=local fixture-10001", calls)
        self.assertIn("run --rm --label rest-mail.run=local -v fixture-10001:/cache alpine:3.20 chown 10001:10001 /cache", calls)

    def test_a_volume_in_use_is_left_alone(self):
        result = self.run_script("0", volume_exists=True)
        self.assertEqual(result.stdout, "-v fixture-0:/tmp/go-build-cache\n")
        self.assertEqual(self.calls.read_text().splitlines(), ["volume inspect fixture-0"])

    def test_it_needs_a_user_and_a_volume(self):
        self.assertEqual(self.run_script().returncode, 2)
        self.assertNotEqual(self.run_script("0", volume="").returncode, 0)


if __name__ == "__main__":
    unittest.main()
