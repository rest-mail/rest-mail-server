"""Run each actual phase with a verified cache and a wrong PATH executable."""

import contextlib
import hashlib
import io
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import vertrag_pin


class Toolchain(unittest.TestCase):
    def test_real_phases_use_only_the_verified_cache_including_child_commands(self):
        for phase in ("gate", "authorization", "fuzz"):
            with (
                self.subTest(phase=phase),
                tempfile.TemporaryDirectory(prefix="contract tool ") as directory,
            ):
                root = Path(directory)
                helpers = root / "tests/contract"
                helpers.mkdir(parents=True)
                for name in ("run.sh", "summary.py", "report_files.py"):
                    shutil.copyfile(ROOT / "tests/contract" / name, helpers / name)
                (root / "scripts").mkdir()
                shutil.copyfile(
                    ROOT / "scripts/vertrag_pin.py", root / "scripts/vertrag_pin.py"
                )
                binary = b'#!/bin/sh\nprintf "pinned:%s\\n" "$*" >> "$CALLS"\nexit 0\n'
                archive = io.BytesIO()
                with tarfile.open(fileobj=archive, mode="w:gz") as tar:
                    member = tarfile.TarInfo("vertrag")
                    member.size = len(binary)
                    tar.addfile(member, io.BytesIO(binary))
                data = archive.getvalue()
                sha = hashlib.sha256(data).hexdigest()
                workflow = root / ".github/workflows/e2e-suite.yml"
                workflow.parent.mkdir(parents=True)
                workflow.write_text(f"VERTRAG_VERSION: 0.29.1\nVERTRAG_SHA256: {sha}\n")
                name = vertrag_pin.asset("0.29.1", *vertrag_pin.this_machine())
                with (
                    mock.patch.object(vertrag_pin, "WORKFLOW", str(workflow)),
                    mock.patch.object(
                        vertrag_pin,
                        "fetch",
                        side_effect=lambda url, sha=sha, name=name, data=data: (
                            f"{sha}  {name}\n".encode()
                            if url.endswith("checksums.txt")
                            else data
                        ),
                    ),
                    contextlib.redirect_stdout(io.StringIO()),
                ):
                    vertrag_pin.install(str(root / ".cache/bin"))
                reports = root / ".cache/contract"
                reports.mkdir()
                (reports / "openapi.json").write_text("{}")
                stubs = root / "bin"
                stubs.mkdir()
                scripts = {
                    "vertrag": 'echo wrong-path-tool >> "$CALLS"\nexit 97\n',
                    "python3": 'if [ "${1:-}" = -B ]; then shift; fi\n'
                    'case "$1" in *vertrag_pin.py|*report_files.py|*summary.py) exec "$REAL_PYTHON" -B "$@";; '
                    '*description.py) printf "{}" > "$3";; esac\nexit 0\n',
                    # These isolated stand-ins prove child tool lookup, without
                    # starting Go probes, containers, fixtures or any network.
                    "go": "vertrag version\n",
                    "chore": "vertrag version\n",
                }
                for name, body in scripts.items():
                    path = stubs / name
                    path.write_text("#!/bin/sh\n" + body)
                    path.chmod(0o755)
                calls = root / "calls"
                env = dict(
                    os.environ,
                    PATH=str(stubs) + os.pathsep + os.environ["PATH"],
                    CALLS=str(calls),
                    REAL_PYTHON=sys.executable,
                    CONTRACT_DEFER_SUMMARY="1",
                    HTTP_PROXY="http://127.0.0.1:1",
                    HTTPS_PROXY="http://127.0.0.1:1",
                    NO_PROXY="",
                )
                env.pop("GITHUB_TOKEN", None)
                result = subprocess.run(
                    ["bash", str(helpers / "run.sh"), phase, "--tag", "Domains"],
                    cwd=root,
                    env=env,
                    text=True,
                    capture_output=True,
                    timeout=10,
                    check=False,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                recorded = calls.read_text().splitlines()
                self.assertNotIn("wrong-path-tool", recorded)
                self.assertTrue(
                    all(line.startswith("pinned:") for line in recorded), recorded
                )
                expected = 2 if phase == "gate" else 1
                self.assertEqual(
                    sum(line.startswith("pinned:run ") for line in recorded), expected
                )
                self.assertTrue(any("--tag Domains" in line for line in recorded))

    def test_ci_and_local_commands_select_the_same_project_cache(self):
        workflow = (ROOT / ".github/workflows/e2e-suite.yml").read_text()
        chores = (ROOT / "chores.yml").read_text()
        runner = (ROOT / "tests/contract/run.sh").read_text()
        for entry in (workflow, chores, runner):
            self.assertIn("scripts/vertrag_pin.py ensure .cache/bin", entry)
        self.assertIn(
            "RESTMAIL_VERTRAG_BINARY: ${{ github.workspace }}/.cache/bin/vertrag",
            workflow,
        )
        self.assertNotIn("/usr/local/bin/vertrag", workflow)


if __name__ == "__main__":
    unittest.main()
