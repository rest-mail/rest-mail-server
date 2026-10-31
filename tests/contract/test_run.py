"""The contract run's helpers: each phase's configuration and the summary (#627)."""

import json
import os
import shutil
import subprocess
import sys
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

    def test_live_url_survives_every_phase_location(self):
        url = "https://fixture.test/api/docs/openapi.json?revision=one"
        for spec in (url,):
            config = self.config(spec=spec)
            self.assertEqual(config["spec"], url)
            again = reports.configure(config, "/tmp/elsewhere", "/tmp/run/coverage")
            self.assertEqual(again["spec"], url)
        self.assertEqual(
            reports.spec_location("folder with spaces/openapi.json", "/tmp"),
            str(Path("/tmp/folder with spaces/openapi.json").resolve()),
        )

    def test_runner_only_uses_the_generated_description(self):
        script = (ROOT / "tests/contract/run.sh").read_text()
        self.assertIn('export CONTRACT_SPEC="$served"', script)
        self.assertNotIn('export CONTRACT_SPEC="$PWD/openapi.yaml"', script)
        self.assertIn("description_equivalence.py", script)
        self.assertIn("${CONTRACT_DESCRIPTION_MODE:-saved}", script)
        self.assertIn("CONTRACT_RUN_SPEC", script)
        self.assertIn(
            "CONTRACT_RUN_SPEC", (ROOT / "tests/contract/authorization.py").read_text()
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

    def test_selected_phases_ignore_other_runs_and_their_operation_counts(self):
        with tempfile.TemporaryDirectory() as directory:
            for phase in ("examples", "coverage", "authorization", "fuzz"):
                Path(directory, phase + ".xml").write_text(
                    '<testsuite><testcase name="a"/></testsuite>'
                )
            # An unrelated examples report must not even be read by fuzz.
            Path(directory, "examples.json").write_text("not this run's JSON")
            summary.main([directory, "fuzz"])
            table = Path(directory, "summary.md").read_text()
            self.assertIn("| fuzz (report only) | 1 | 1 | 0 | 0 |", table)
            for stale in ("examples", "coverage", "authorization", "Operations:"):
                self.assertNotIn(stale, table)
            table = summary.summary(directory, ["coverage", "authorization"])
            self.assertIn("| coverage |", table)
            self.assertIn("| authorization (report only) |", table)
            self.assertNotIn("fuzz", table)

    def test_ci_runs_the_complete_local_command(self):
        chores = (ROOT / "chores.yml").read_text()
        workflow = (ROOT / ".github/workflows/e2e-suite.yml").read_text()
        self.assertIn("run: chore contract\n", workflow)
        self.assertIn("CONTRACT_ENDPOINT: http://10.99.0.20:8080", workflow)
        for phase in ("gate", "authorization", "ownership", "fuzz"):
            self.assertIn("tests/contract/run.sh " + phase, chores)
        self.assertIn("secret_scan.py .cache/contract/examples.har", chores)
        self.assertIn("--audit-from .cache/contract/vertrag-run.yml", chores)
        self.assertTrue(os.access(ROOT / "tests/contract/run.sh", os.X_OK))


class ContractCommand(unittest.TestCase):
    def run_command(
        self, principals=True, stale_reports=False, artifacts=None, **settings
    ):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            (work / "tests/contract").mkdir(parents=True)
            for name in ("report_files.py", "summary.py"):
                shutil.copyfile(
                    ROOT / "tests/contract" / name, work / "tests/contract" / name
                )
            if stale_reports:
                reports_dir = work / ".cache/contract"
                reports_dir.mkdir(parents=True)
                for name in (
                    "examples",
                    "coverage",
                    "authorization",
                    "fuzz",
                    "browser-sessions",
                ):
                    (reports_dir / (name + ".xml")).write_text(
                        '<testsuite><testcase name="previous-success"/></testsuite>'
                    )
                for name in (
                    "openapi.json",
                    "vertrag-run.yml",
                    "summary.md",
                    "api-key-fixture.json",
                ):
                    (reports_dir / name).write_text("previous green verdict")
            if principals:
                (work / ".cache").mkdir(exist_ok=True)
                (work / ".cache/contract-principals.json").write_text("{}")
            scripts = {
                "chore": 'echo "chore:$*" >> "$CALLS"\n'
                'if [ "$1" = test:e2e ]; then '
                '[ "$API_BASE_URL" = "${CONTRACT_ENDPOINT:-http://10.99.0.20:8080}" ] || exit 98; fi\n'
                'exit "${FIXTURE_EXIT:-0}"\n',
                "python3": 'if [ "${1:-}" = -B ]; then shift; fi\n'
                'case "$1" in *vertrag_pin.py) echo pin >> "$CALLS"; '
                '[ "${TOOL_EXIT:-0}" = 0 ] || exit "$TOOL_EXIT"; '
                'echo "$PWD/.cache/bin/vertrag"; exit 0;; '
                '*report_files.py) exec "$REAL_PYTHON" -B "$@";; '
                '*summary.py) echo "summary:$*" >> "$CALLS"; '
                '[ "${SUMMARY_EXIT:-0}" = 0 ] || exit "$SUMMARY_EXIT"; '
                'exec "$REAL_PYTHON" -B "$@";; esac\n'
                'echo scan >> "$CALLS"\nexit "${SCAN_EXIT:-0}"\n',
                "tests/contract/run.sh": 'echo "run:$*" >> "$CALLS"\n'
                '[ "$CONTRACT_DEFER_SUMMARY" = 1 ] || exit 99\n'
                'case "$1" in gate) exit "${GATE_EXIT:-0}";; '
                'authorization) exit "${AUTH_EXIT:-0}";; '
                'ownership) exit "${OWNERSHIP_EXIT:-0}";; '
                'internal) [ -n "$CONTRACT_INTERNAL_ENDPOINT" ] && [ -n "$CONTRACT_INTERNAL_CERTS" ] || exit 97; '
                'exit "${INTERNAL_EXIT:-0}";; esac\n',
                "docker": 'echo "docker:$1" >> "$CALLS"\nexit "${DOCKER_EXIT:-0}"\n',
            }
            for name, script in scripts.items():
                path = work / name
                path.write_text("#!/bin/sh\n" + script)
                path.chmod(0o755)
            calls = work / "calls"
            env = dict(
                os.environ,
                PATH=str(work)
                + os.pathsep
                + str(Path(sys.executable).parent)
                + os.pathsep
                + os.environ["PATH"],
                CALLS=str(calls),
                CONTRACT_ENDPOINT="http://fixture.test:8080",
                REAL_PYTHON=sys.executable,
            )
            env.update(settings)
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
            if artifacts is not None:
                artifacts.update(
                    (path.name, path.read_text())
                    for path in (work / ".cache/contract").iterdir()
                    if path.is_file()
                )
            return result, calls.read_text().splitlines()

    def test_setup_failure_replaces_stale_reports_with_this_invocations_summary(self):
        for principals, endpoint in (
            (True, "http://fixture.test:8080"),
            (False, "http://fixture.test:8080"),
            (False, ""),
        ):
            with self.subTest(principals=principals, endpoint=endpoint):
                artifacts = {}
                result, _ = self.run_command(
                    principals=principals,
                    stale_reports=True,
                    artifacts=artifacts,
                    CONTRACT_ENDPOINT=endpoint,
                    FIXTURE_EXIT="1",
                )
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(set(artifacts), {"summary.md", "api-key-fixture.json"})
                self.assertIn("| Phase | Transactions |", result.stdout)
                self.assertNotIn("| examples |", artifacts["summary.md"])
                self.assertNotIn("previous", artifacts["summary.md"])

    def test_failed_gate_still_scans_and_reports_without_losing_the_failure(self):
        result, calls = self.run_command(GATE_EXIT="3", AUTH_EXIT="1")
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertEqual(
            calls,
            [
                "pin",
                "chore:test:contract-fixtures",
                "chore:test:contract-api-keys",
                "chore:test:contract-mfa-admin",
                "run:gate --tag Domains",
                "scan",
                "run:authorization --tag Domains",
                "chore:test:contract-domain-tenants",
                "run:ownership --tag Domains",
                "docker:run",
                "docker:run",
                "docker:run",
                "run:internal --tag Domains",
                "summary:tests/contract/summary.py .cache/contract examples coverage authorization ownership ownership-domains ownership-mailboxes internal",
            ],
        )

    def test_secret_scan_is_required_and_authorization_remains_report_only(self):
        for scan, auth, expected in (("1", "0", 1), ("0", "1", 0)):
            with self.subTest(scan=scan, auth=auth):
                result, _ = self.run_command(
                    SCAN_EXIT=scan, AUTH_EXIT=auth, OWNERSHIP_EXIT=auth
                )
                self.assertEqual(result.returncode, expected, result.stderr)

    def test_the_internal_pass_is_a_gate(self):
        for settings, expected in (({"INTERNAL_EXIT": "1"}, 1), ({"DOCKER_EXIT": "1"}, 1),
                                   ({"INTERNAL_EXIT": "1", "GATE_EXIT": "3"}, 3)):
            with self.subTest(settings=settings):
                result, calls = self.run_command(**settings)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertEqual("run:internal --tag Domains" in calls, "DOCKER_EXIT" not in settings)

    def test_fixture_failure_prevents_contract_checks(self):
        result, calls = self.run_command(FIXTURE_EXIT="1")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(
            calls,
            [
                "pin",
                "chore:test:contract-fixtures",
                "summary:tests/contract/summary.py .cache/contract examples coverage authorization ownership ownership-domains ownership-mailboxes internal",
            ],
        )

    def test_a_fresh_topology_prepares_only_the_principal_fixture_first(self):
        result, calls = self.run_command(principals=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0], "pin")
        self.assertEqual(calls[1], "chore:test:e2e -- -run ^TestContractPrincipals$")
        self.assertEqual(calls[2], "chore:test:contract-fixtures")

    def test_default_endpoint_starts_the_topology_before_principal_setup(self):
        result, calls = self.run_command(principals=False, CONTRACT_ENDPOINT="")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0], "pin")
        self.assertEqual(calls[1], "chore:e2e:up")
        self.assertEqual(calls[2], "chore:test:e2e -- -run ^TestContractPrincipals$")
        self.assertEqual(sum(call.startswith("summary:") for call in calls), 1)

    def test_tool_failure_precedes_topology_and_fixture_changes(self):
        artifacts = {}
        result, calls = self.run_command(
            principals=False,
            stale_reports=True,
            artifacts=artifacts,
            CONTRACT_ENDPOINT="",
            TOOL_EXIT="17",
        )
        self.assertEqual(result.returncode, 17, result.stderr)
        self.assertEqual(
            calls,
            [
                "pin",
                "summary:tests/contract/summary.py .cache/contract examples coverage authorization ownership ownership-domains ownership-mailboxes internal",
            ],
        )
        self.assertNotIn("previous", artifacts["summary.md"])


class PhaseCommand(unittest.TestCase):
    def test_real_runner_early_failure_does_not_summarize_stale_phases(self):
        for phase, stale in (
            ("gate", "fuzz"),
            ("fuzz", "examples"),
            ("authorization", "authorization"),
        ):
            for deferred in (False, True):
                with (
                    self.subTest(phase=phase, deferred=deferred),
                    tempfile.TemporaryDirectory() as directory,
                ):
                    work = Path(directory)
                    helpers = work / "tests/contract"
                    helpers.mkdir(parents=True)
                    for name in ("run.sh", "summary.py", "report_files.py"):
                        shutil.copyfile(ROOT / "tests/contract" / name, helpers / name)
                    reports_dir = work / ".cache/contract"
                    reports_dir.mkdir(parents=True)
                    (reports_dir / (stale + ".xml")).write_text(
                        '<testsuite><testcase name="stale"><failure/></testcase></testsuite>'
                    )
                    (work / "scripts").mkdir()
                    (work / "scripts/vertrag_pin.py").write_text(
                        "from pathlib import Path\nprint(Path(__file__).resolve().parents[1] / 'vertrag')\n"
                    )
                    vertrag = work / "vertrag"
                    vertrag.write_text("#!/bin/sh\nexit 17\n")
                    vertrag.chmod(0o755)
                    result = subprocess.run(
                        ["bash", str(helpers / "run.sh"), phase],
                        cwd=work,
                        env=dict(
                            os.environ,
                            PATH=str(work)
                            + os.pathsep
                            + str(Path(sys.executable).parent)
                            + os.pathsep
                            + os.environ["PATH"],
                            CONTRACT_DEFER_SUMMARY="1" if deferred else "0",
                        ),
                        text=True,
                        capture_output=True,
                        timeout=10,
                        check=False,
                    )
                    self.assertEqual(result.returncode, 17, result.stderr)
                    self.assertNotIn("| " + stale, result.stdout)
                    self.assertEqual(
                        (reports_dir / "summary.md").exists(), not deferred
                    )


if __name__ == "__main__":
    unittest.main()
