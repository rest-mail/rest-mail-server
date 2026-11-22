"""Writable Docker volumes preserve run ownership and initialize Unix permissions."""

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class WritableVolumeTests(unittest.TestCase):
    def launch(self, exists):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            docker = directory / "docker"
            docker.write_text(
                "#!/usr/bin/env python3\n"
                "import json, os, sys\n"
                "with open(os.environ['CAPTURE'], 'a') as output: output.write(json.dumps(sys.argv[1:]) + '\\n')\n"
                "if sys.argv[1:3] == ['volume', 'inspect']: sys.exit(int(os.environ['MISSING']))\n"
            )
            docker.chmod(0o755)
            capture = directory / "calls.jsonl"
            result = subprocess.run(
                ["sh", str(ROOT / "tasks/writable-volume.sh"), "restmail-fixture-tmp", "10001", "0700", "/app/tmp"],
                env={**os.environ, "PATH": str(directory) + os.pathsep + os.environ["PATH"],
                     "CAPTURE": str(capture), "MISSING": str(int(not exists)), "RESTMAIL_LABEL": "rest-mail-loop=fixture"},
                text=True, capture_output=True, check=True,
            )
            return result.stdout, [json.loads(line) for line in capture.read_text().splitlines()]

    def test_new_volume_is_labelled_and_owned_before_mounting(self):
        flags, calls = self.launch(False)
        self.assertEqual(flags, "-v restmail-fixture-tmp:/app/tmp\n")
        self.assertEqual(calls[1], ["volume", "create", "--label", "rest-mail-loop=fixture", "restmail-fixture-tmp"])
        helper = calls[2]
        self.assertEqual(helper[:6], ["run", "--rm", "--label", "rest-mail-loop=fixture", "--network", "none"])
        self.assertIn("restmail-fixture-tmp:/writable", helper)
        self.assertIn('chown "$1:$1" /writable; chmod "$2" /writable', helper)
        self.assertEqual(helper[-3:], ["_", "10001", "0700"])
        self.assertNotIn("--tmpfs", helper)

    def test_existing_volume_is_reused_without_resetting_contents(self):
        flags, calls = self.launch(True)
        self.assertEqual(len(calls), 1)
        self.assertEqual(flags, "-v restmail-fixture-tmp:/app/tmp\n")

    def test_missing_arguments_fail_without_docker(self):
        result = subprocess.run(["sh", str(ROOT / "tasks/writable-volume.sh")], capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertIn("usage:", result.stderr)


if __name__ == "__main__":
    unittest.main()
