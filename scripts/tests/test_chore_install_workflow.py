"""Run the real installer shell with local release fixtures and a fake gh client."""

import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ChoreInstallerTests(unittest.TestCase):
    def run_installer(self, version="0.17.0", corrupt=False, attested=True):
        source = (ROOT / ".github/workflows/e2e-suite.yml").read_text()
        step = source.split("      - name: Install chore (the newest release, attested)\n", 1)[1]
        step = step.split("      - name: Populate Go module cache", 1)[0]
        script = textwrap.dedent(step.split("        run: |\n", 1)[1])
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            binary = work / "bin"
            binary.mkdir()
            assets = work / "assets"
            assets.mkdir()
            runs = work / "runs"
            runs.mkdir()
            archive = assets / ("chore-" + version + "-linux-x86_64.tar.gz")
            content = b"#!/bin/sh\necho fixture-chore\n"
            with tarfile.open(archive, "w:gz") as package:
                entry = tarfile.TarInfo("chore")
                entry.size = len(content)
                entry.mode = 0o755
                package.addfile(entry, io.BytesIO(content))
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            if corrupt:
                digest = "0" * 64
            (assets / "checksums.txt").write_text(digest + "  " + archive.name + "\n")
            (work / "chores.yml").write_text("chore_min_version: 0.17.0\n")
            capture = work / "calls.jsonl"
            client = '''#!/usr/bin/env python3
import json, os, pathlib, shutil, sys
args = sys.argv[1:]
with open(os.environ["CAPTURE"], "a") as out:
    out.write(json.dumps([pathlib.Path(sys.argv[0]).name] + args) + "\\n")
if pathlib.Path(sys.argv[0]).name == "sudo":
    assert args[:3] == ["install", "-m", "0755"]
    assert pathlib.Path(args[3]).is_file()
    assert args[4] == "/usr/local/bin/chore"
elif args[:2] == ["release", "view"]:
    print("v" + os.environ["VERSION"])
elif args[:2] == ["release", "download"]:
    destination = pathlib.Path(args[args.index("--dir") + 1])
    for source in pathlib.Path(os.environ["ASSETS"]).iterdir():
        shutil.copy2(source, destination / source.name)
elif args[:2] == ["attestation", "verify"]:
    assert args[-2:] == ["--repo", "antimatter-studios/chore"]
    sys.exit(0 if os.environ["ATTESTED"] == "yes" else 1)
else:
    raise RuntimeError("unexpected command")
'''
            for name in ("gh", "sudo"):
                path = binary / name
                path.write_text(client)
                path.chmod(0o755)
            (binary / "chore").write_text("#!/bin/sh\necho fixture-chore\n")
            (binary / "chore").chmod(0o755)
            env = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"],
                       VERSION=version, ASSETS=str(assets), TMPDIR=str(runs), CAPTURE=str(capture),
                       ATTESTED="yes" if attested else "no")
            result = subprocess.run(["bash", "-eu", "-o", "pipefail"], input=script,
                                    text=True, cwd=work, capture_output=True, env=env, timeout=10)
            calls = [json.loads(line) for line in capture.read_text().splitlines()]
            return result, calls

    def test_verified_release_installs_after_checksum_and_attestation(self):
        result, calls = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual([call[:3] for call in calls[:3]],
                         [["gh", "release", "view"], ["gh", "release", "download"], ["gh", "attestation", "verify"]])
        self.assertEqual(calls[-1][0], "sudo")

    def test_release_below_the_repository_floor_is_not_downloaded(self):
        result, calls = self.run_installer(version="0.16.9")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(calls), 1)

    def test_checksum_mismatch_never_reaches_attestation_or_install(self):
        result, calls = self.run_installer(corrupt=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(call[0] == "sudo" or call[1:3] == ["attestation", "verify"] for call in calls))

    def test_failed_attestation_never_installs(self):
        result, calls = self.run_installer(attested=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(any(call[1:3] == ["attestation", "verify"] for call in calls))
        self.assertFalse(any(call[0] == "sudo" for call in calls))


if __name__ == "__main__":
    unittest.main()
