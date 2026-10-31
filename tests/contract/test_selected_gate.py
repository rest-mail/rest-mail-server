"""Selected contract gates retain the real phase runner's failed-probe verdict."""

import json
import os
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
SELECTIONS = (
    ("--tag", "Domains"),
    ("--operation-id", "getDomain"),
    ("--only-matching", " > Read a domain > "),
)


class SelectedGate(unittest.TestCase):
    def run_gate(
        self, selection=(), *, examples=0, coverage=0, ratchet=0, complete=False
    ):
        with tempfile.TemporaryDirectory(prefix="selected contract ") as directory:
            work = Path(directory)
            helpers = work / "tests/contract"
            helpers.mkdir(parents=True)
            for name in (
                "run.sh",
                "report_files.py",
                "summary.py",
                "reports.py",
                "probes.py",
                "internal.py",
            ):
                shutil.copy2(ROOT / "tests/contract" / name, helpers / name)
            (helpers / "fixtures").mkdir()
            shutil.copyfile(
                ROOT / "tests/contract/fixtures/dkim-key.pem",
                helpers / "fixtures/dkim-key.pem",
            )
            (work / "openapi.yaml").write_text("openapi: 3.1.0\npaths: {}\n")
            (work / "vertrag.yml").write_text(
                "spec: openapi.yaml\nhookfiles: tests/contract/entrypoint.py\n"
                "surface: {count: passed, require: 100}\n"
            )
            (helpers / "description.py").write_text(
                "import sys\nfrom pathlib import Path\n"
                "Path(sys.argv[2]).write_text('{}')\n"
            )
            # This fixture isolates phase exit/ratchet behavior; input proof
            # failures are covered by test_description_equivalence.py.
            (helpers / "description_equivalence.py").write_text(
                "import sys\nfrom pathlib import Path\n"
                "print(Path(sys.argv[2]).resolve())\n"
            )
            (helpers / "repeat_fixtures.py").write_text("")
            (helpers / "secret_scan.py").write_text(
                "print('fixture secret scan ran')\n"
            )
            (helpers / "authorization.py").write_text(
                "import shutil,sys\nshutil.copyfile(sys.argv[2],sys.argv[4])\n"
            )
            (helpers / "gate.py").write_text(
                "import json,os,sys\n"
                "with open(os.environ['CALLS'],'a') as f: "
                "f.write(json.dumps({'phase':'ratchet'})+'\\n')\n"
                "sys.exit(int(os.environ['RATCHET_CODE']))\n"
            )
            (work / "scripts").mkdir()
            (work / "scripts/vertrag_pin.py").write_text(
                "from pathlib import Path\n"
                "print(Path(__file__).resolve().parents[1] / 'vertrag')\n"
            )
            binary = work / "vertrag"
            binary.write_text(
                "#!/usr/bin/env python3\n"
                "import json,os,sys\nfrom pathlib import Path\nimport yaml\n"
                "args=sys.argv[1:]\n"
                "if args==['version']: sys.exit(0)\n"
                "phase=args[args.index('--phases')+1]\n"
                "with open(os.environ['CALLS'],'a') as f: "
                "f.write(json.dumps({'phase':phase,'args':args})+'\\n')\n"
                "config=yaml.safe_load(Path(args[args.index('--config')+1]).read_text())\n"
                "code=int(os.environ.get(phase.upper()+'_CODE','0'))\n"
                "for out in config['output']:\n"
                " if not out: continue\n"
                " path=Path(out)\n"
                " data='{}'\n"
                " if path.suffix=='.xml':\n"
                "  data='<testsuite><testcase name=\"selected\">'+"
                "('<failure/>' if code else '')+'</testcase></testsuite>'\n"
                " path.write_text(data)\n"
                "sys.exit(code)\n"
            )
            binary.chmod(0o755)
            tools = work / "bin"
            tools.mkdir()
            for name in ("go", "chore"):
                path = tools / name
                path.write_text("#!/bin/sh\nexit 0\n")
                path.chmod(0o755)
            # Reading the internal mTLS certificates from their volume.
            docker = tools / "docker"
            docker.write_text("#!/bin/sh\nexit 0\n")
            docker.chmod(0o755)
            (work / ".cache").mkdir()
            (work / ".cache/contract-principals.json").write_text("{}")
            calls_file = work / "calls.jsonl"
            env = dict(
                os.environ,
                PATH=os.pathsep.join(
                    (str(tools), str(Path(sys.executable).parent), os.environ["PATH"])
                ),
                CONTRACT_ENDPOINT="http://fixture.invalid:8080",
                CONTRACT_DEFER_SUMMARY="0",
                EXAMPLES_CODE=str(examples),
                COVERAGE_CODE=str(coverage),
                RATCHET_CODE=str(ratchet),
                CALLS=str(calls_file),
            )
            env.pop("CONTRACT_LOGS", None)
            if complete:
                command = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"][
                    "contract"
                ]["cmds"][0]
                command = command.replace("{{.CLI_ARGS}}", shlex.join(selection))
                args = ["sh", "-c", command]
            else:
                args = ["bash", str(helpers / "run.sh"), "gate", *selection]
            result = subprocess.run(
                args,
                cwd=work,
                env=env,
                capture_output=True,
                text=True,
                timeout=15,
                check=False,
            )
            self.assertTrue(calls_file.exists(), result.stdout + result.stderr)
            calls = [json.loads(line) for line in calls_file.read_text().splitlines()]
            report = (work / ".cache/contract/summary.md").read_text()
            return result, calls, report

    def test_selected_probe_failures_are_not_success(self):
        for selection in SELECTIONS:
            for complete in (False, True):
                with self.subTest(selection=selection, complete=complete):
                    result, calls, report = self.run_gate(
                        selection, coverage=2, complete=complete
                    )
                    self.assertEqual(
                        result.returncode, 2, result.stdout + result.stderr
                    )
                    self.assertIn("| coverage | 1 | 0 | 1 | 0 |", report)
                    self.assertNotIn("ratchet", [c["phase"] for c in calls])
                    if complete:
                        self.assertIn("fixture secret scan ran", result.stdout)
                        self.assertIn("authorization", [c["phase"] for c in calls])
                    for call in calls:
                        self.assertEqual(
                            call["args"][-len(selection) :], list(selection)
                        )

    def test_selected_success_and_examples_failure_keep_their_verdicts(self):
        for complete in (False, True):
            with self.subTest(complete=complete):
                result, calls, report = self.run_gate(SELECTIONS[0], complete=complete)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("| coverage | 1 | 1 | 0 | 0 |", report)
                self.assertNotIn("ratchet", [c["phase"] for c in calls])
                result, calls, report = self.run_gate(
                    SELECTIONS[0], examples=1, complete=complete
                )
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertNotIn("coverage", [c["phase"] for c in calls])
                self.assertIn("| examples | 1 | 0 | 1 | 0 |", report)

    def test_full_gate_retains_the_baselines_authority(self):
        for complete in (False, True):
            for ratchet in (0, 7):
                with self.subTest(complete=complete, ratchet=ratchet):
                    result, calls, report = self.run_gate(
                        coverage=2, ratchet=ratchet, complete=complete
                    )
                    self.assertEqual(
                        result.returncode, ratchet, result.stdout + result.stderr
                    )
                    self.assertIn("ratchet", [c["phase"] for c in calls])
                    self.assertIn("| coverage | 1 | 0 | 1 | 0 |", report)

    def test_nonprobe_errors_are_not_interpreted_as_baseline_findings(self):
        for code in (1, 3, 17):
            for selection in ((), SELECTIONS[0]):
                with self.subTest(code=code, selection=selection):
                    result, calls, _ = self.run_gate(selection, coverage=code)
                    self.assertEqual(
                        result.returncode, code, result.stdout + result.stderr
                    )
                    self.assertNotIn("ratchet", [c["phase"] for c in calls])
