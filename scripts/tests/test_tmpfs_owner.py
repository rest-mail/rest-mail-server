"""tasks/tmpfs-owner.sh: the --tmpfs owner option each container engine accepts.

Docker takes uid=/gid=; podman, which the antimatter-vps runners use, refuses them
("unknown mount option") and takes U, which chowns the tmpfs to the container's user.
"""

import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
HELPER = ROOT / "tasks/tmpfs-owner.sh"

DOCKER_COMPONENTS = "Engine containerd runc docker-init "
PODMAN_COMPONENTS = "Podman Engine "


class TmpfsOwner(unittest.TestCase):
    def run_helper(self, *args, components=None, fail=False):
        with tempfile.TemporaryDirectory() as directory:
            docker = Path(directory) / "docker"
            body = "exit 1" if fail else f"printf '%s\\n' '{components}'"
            docker.write_text(
                "#!/usr/bin/env bash\n"
                # Only `docker version --format ...` may be asked.
                '[ "$1" = version ] && [ "$2" = --format ] || exit 99\n'
                f"{body}\n"
            )
            docker.chmod(docker.stat().st_mode | stat.S_IXUSR)
            env = dict(os.environ, PATH=f"{directory}{os.pathsep}{os.environ['PATH']}")
            return subprocess.run(
                [str(HELPER), *args], env=env, text=True, capture_output=True, check=False
            )

    def test_docker_gets_uid_and_gid(self):
        result = self.run_helper("10001", components=DOCKER_COMPONENTS)
        self.assertEqual((result.returncode, result.stdout), (0, "uid=10001,gid=10001\n"))

    def test_a_separate_gid(self):
        result = self.run_helper("101", "102", components=DOCKER_COMPONENTS)
        self.assertEqual(result.stdout, "uid=101,gid=102\n")

    def test_podman_gets_u(self):
        for args in (("10001",), ("101", "101")):
            with self.subTest(args=args):
                result = self.run_helper(*args, components=PODMAN_COMPONENTS)
                self.assertEqual((result.returncode, result.stdout), (0, "U\n"))

    def test_an_unanswered_engine_query_keeps_dockers_form(self):
        result = self.run_helper("10001", fail=True)
        self.assertEqual((result.returncode, result.stdout), (0, "uid=10001,gid=10001\n"))

    def test_usage(self):
        for args in ((), ("1", "2", "3")):
            with self.subTest(args=args):
                result = self.run_helper(*args, components=DOCKER_COMPONENTS)
                self.assertEqual(result.returncode, 2)
                self.assertIn("usage", result.stderr)

    def test_no_tmpfs_names_its_owner_directly(self):
        """Every owned tmpfs asks the helper, so it works on both engines."""
        paths = [*ROOT.glob("tasks/*.yml"), *ROOT.glob(".github/workflows/*.yml"), ROOT / "chores.yml"]
        users = 0
        for path in paths:
            if not path.is_file():
                continue
            for number, line in enumerate(path.read_text().splitlines(), 1):
                if "--tmpfs" in line:
                    with self.subTest(path=path.name, line=number):
                        self.assertNotRegex(line, r"\b[ug]id=")
                if "tmpfs-owner.sh" in line:
                    users += 1
        self.assertGreaterEqual(users, 4)

    def test_gateway_dev_tmpfs_keeps_mode_0700(self):
        for service in ("imap-gateway", "pop3-gateway", "smtp-gateway"):
            with self.subTest(service=service):
                source = (ROOT / "tasks" / f"{service}.yml").read_text()
                self.assertIn('TMPFS_OWNER=$("{{.TASKFILE_DIR}}/tmpfs-owner.sh" 10001)', source)
                self.assertRegex(source, re.escape("--tmpfs /app/tmp:rw,exec,nosuid,nodev,size=128m,mode=0700,$TMPFS_OWNER"))


if __name__ == "__main__":
    unittest.main()
