"""The contract run's helpers: each phase's configuration and the summary (#627)."""

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

import reports
import summary
import yaml

ROOT = Path(__file__).resolve().parents[2]


class Reports(unittest.TestCase):
    def config(self, **kw):
        base = {
            "spec": "./openapi.yaml",
            "hookfiles": "tests/contract/entrypoint.py",
            "endpoint": "http://10.99.0.20:8080",
            "surface": {"require": 100},
            "reporter": ["cli", "junit"],
            "output": ["", "x.xml"],
        }
        return reports.configure(base, ROOT, "/tmp/run/examples", **kw)

    def test_each_phase_writes_junit_html_and_markdown(self):
        config = self.config()
        self.assertEqual(
            config["reporter"], ["cli", "junit", "html", "markdown", "har", "json"]
        )
        self.assertEqual(
            config["output"],
            [""]
            + [
                str(Path("/tmp/run/examples" + suffix).resolve())
                for suffix in (".xml", ".html", ".md", ".har", ".json")
            ],
        )
        self.assertEqual(config["spec"], str(ROOT / "openapi.yaml"))
        self.assertEqual(
            config["hookfiles"], str(ROOT / "tests/contract/entrypoint.py")
        )
        self.assertIn("surface", config)
        self.assertEqual(config["endpoint"], "http://10.99.0.20:8080")

    def test_an_endpoint_and_a_narrowed_run(self):
        config = self.config(narrowed=True, endpoint="http://localhost:8080")
        self.assertNotIn("surface", config)
        self.assertEqual(config["endpoint"], "http://localhost:8080")

    def test_served_description_replaces_the_oracle_for_every_phase(self):
        config = self.config(spec="/tmp/run/openapi.json")
        self.assertEqual(config["spec"], str(Path("/tmp/run/openapi.json").resolve()))
        self.assertEqual(
            config["hookfiles"], str(ROOT / "tests/contract/entrypoint.py")
        )


class Summary(unittest.TestCase):
    def test_operation_counts_and_reasons_come_from_vertrags_surface(self):
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, "examples.json").write_text(
                json.dumps(
                    {
                        "surface": {
                            "run": {
                                "operationsReached": 3,
                                "operationsPassed": 2,
                                "operations": 4,
                            },
                            "missed": [
                                {"operation": "readWidget", "reason": "unresolved link"}
                            ],
                            "notPassed": [
                                {
                                    "operation": "writeWidget",
                                    "reason": "unexpected status",
                                }
                            ],
                            "excluded": [
                                {
                                    "operation": "resetWidget",
                                    "reason": "destructive (#1)",
                                },
                                {
                                    "operation": "readWidget",
                                    "status": "500",
                                    "reason": "fault only | #2",
                                },
                            ],
                        }
                    }
                )
            )
            result = summary.summary(directory)
            self.assertIn(
                "Operations: 3 reached, 2 passed, 4 counted, 1 excluded.", result
            )
            self.assertIn("| readWidget |  | unresolved link |", result)
            self.assertIn("| writeWidget |  | unexpected status |", result)
            self.assertIn("| resetWidget |  | destructive (#1) |", result)
            self.assertIn(r"| readWidget | 500 | fault only \| #2 |", result)

    def test_one_line_per_phase_that_ran(self):
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, "examples.xml").write_text(
                '<testsuites><testsuite><testcase name="a"/><testcase name="b"><failure/></testcase>'
                '<testcase name="c"><skipped/></testcase></testsuite></testsuites>'
            )
            Path(directory, "authorization.xml").write_text(
                '<testsuite><testcase name="a"/></testsuite>'
            )
            table = summary.summary(directory)
            self.assertIn("| examples | 3 | 1 | 1 | 1 |", table)
            self.assertIn("| authorization (report only) | 1 | 1 | 0 | 0 |", table)
            self.assertNotIn("coverage", table)
            summary.main([directory])
            self.assertTrue(
                Path(directory, "summary.md").read_text().startswith("# Contract run")
            )

    def test_ci_runs_the_complete_local_command(self):
        chores = (ROOT / "chores.yml").read_text()
        workflow = (ROOT / ".github/workflows/e2e-suite.yml").read_text()
        self.assertIn("run: chore contract\n", workflow)
        self.assertIn("CONTRACT_ENDPOINT: http://10.99.0.20:8080", workflow)
        for phase in ("gate", "authorization", "fuzz"):
            self.assertIn("tests/contract/run.sh " + phase, chores)
        self.assertIn("secret_scan.py .cache/contract/examples.har", chores)
        self.assertIn("--audit-from .cache/contract/vertrag-run.yml", chores)
        self.assertTrue(os.access(ROOT / "tests/contract/run.sh", os.X_OK))


class ContractCommand(unittest.TestCase):
    def run_command(self, principals=True, **settings):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            (work / "tests/contract").mkdir(parents=True)
            if principals:
                (work / ".cache").mkdir()
                (work / ".cache/contract-principals.json").write_text("{}")
            scripts = {
                "chore": 'echo "chore:$*" >> "$CALLS"\nexit "${FIXTURE_EXIT:-0}"\n',
                "python3": 'echo scan >> "$CALLS"\nexit "${SCAN_EXIT:-0}"\n',
                "tests/contract/run.sh": 'echo "run:$*" >> "$CALLS"\n'
                'case "$1" in gate) exit "${GATE_EXIT:-0}";; '
                'authorization) exit "${AUTH_EXIT:-0}";; esac\n',
            }
            for name, script in scripts.items():
                path = work / name
                path.write_text("#!/bin/sh\n" + script)
                path.chmod(0o755)
            calls = work / "calls"
            env = dict(
                os.environ,
                PATH=str(work) + os.pathsep + os.environ["PATH"],
                CALLS=str(calls),
                CONTRACT_ENDPOINT="http://fixture.test:8080",
                **settings,
            )
            command = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"][
                "contract"
            ]["cmds"][0]
            result = subprocess.run(
                ["sh", "-eu"],
                input=command.replace("{{.CLI_ARGS}}", "--tag Domains"),
                text=True,
                cwd=work,
                env=env,
                capture_output=True,
                timeout=5,
                check=False,
            )
            return result, calls.read_text().splitlines()

    def test_failed_gate_still_scans_and_reports_without_losing_the_failure(self):
        result, calls = self.run_command(GATE_EXIT="3", AUTH_EXIT="1")
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertEqual(
            calls,
            [
                "chore:test:contract-fixtures",
                "chore:test:contract-api-keys",
                "run:gate --tag Domains",
                "scan",
                "run:authorization --tag Domains",
            ],
        )

    def test_secret_scan_is_required_and_authorization_remains_report_only(self):
        for scan, auth, expected in (("1", "0", 1), ("0", "1", 0)):
            with self.subTest(scan=scan, auth=auth):
                result, _ = self.run_command(SCAN_EXIT=scan, AUTH_EXIT=auth)
                self.assertEqual(result.returncode, expected, result.stderr)

    def test_fixture_failure_prevents_contract_checks(self):
        result, calls = self.run_command(FIXTURE_EXIT="1")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(calls, ["chore:test:contract-fixtures"])

    def test_a_fresh_topology_prepares_only_the_principal_fixture_first(self):
        result, calls = self.run_command(principals=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0], "chore:test:e2e -- -run ^TestContractPrincipals$")
        self.assertEqual(calls[1], "chore:test:contract-fixtures")


if __name__ == "__main__":
    unittest.main()
