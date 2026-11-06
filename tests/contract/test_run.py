"""The contract run's helpers: each phase's configuration and the summary (#627)."""

import json
import os
import re
import shlex
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

    def test_the_contract_command_reports_the_api_coverage_of_its_phases(self):
        chores = (ROOT / "chores.yml").read_text()
        reset = chores.index("chore api:coverage:reset")
        gate = chores.index("tests/contract/run.sh gate {{.CLI_ARGS}}")
        ownership = chores.index("tests/contract/run.sh ownership {{.CLI_ARGS}}")
        report = chores.index("chore api:coverage:report")
        self.assertLess(reset, gate)
        self.assertLess(ownership, report)
        self.assertIn("API_COVER:", (ROOT / ".github/workflows/e2e-suite.yml").read_text())


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

    def test_ci_reaches_the_example_pairs_only_through_the_chore(self):
        workflow = (ROOT / ".github/workflows/e2e-suite.yml").read_text()
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        self.assertNotIn("example_pairs.sh", workflow)
        self.assertIn("chore contract:pairs\n", tasks["contract"]["cmds"][0])
        pairs = tasks["contract:pairs"]["cmds"][0]
        self.assertIn("tests/contract/example_pairs.sh -run 'WithVertrag$'", pairs)
        self.assertTrue(os.access(ROOT / "tests/contract/example_pairs.sh", os.X_OK))

    def test_the_example_pairs_pattern_selects_every_vertrag_test(self):
        # A *WithVertrag test skips without the binary, so one this pattern misses
        # looks fine everywhere and proves nothing.
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        command = next(
            line
            for line in tasks["contract:pairs"]["cmds"][0].splitlines()
            if line.strip().startswith("tests/contract/example_pairs.sh ")
        )
        arguments = shlex.split(command)
        pattern = arguments[arguments.index("-run") + 1]
        self.assertEqual(pattern, "WithVertrag$")
        written = set()
        for path in (ROOT / "internal/api").rglob("*_test.go"):
            written.update(
                re.findall(r"^func (Test\w*WithVertrag)\(", path.read_text(), re.MULTILINE)
            )
        self.assertTrue(written)
        self.assertEqual(
            sorted(name for name in written if not re.search(pattern, name)),
            [],
            "no run sends these",
        )

    def test_every_test_that_needs_vertrag_is_in_the_example_pairs(self):
        # The same trap under any other name: TestContactReplyTimeContractExchanges read the
        # binary, skipped without it, and no CI step ever set it for that test.
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        command = next(
            line
            for line in tasks["contract:pairs"]["cmds"][0].splitlines()
            if line.strip().startswith("tests/contract/example_pairs.sh ")
        )
        arguments = shlex.split(command)
        pattern = arguments[arguments.index("-run") + 1]
        functions = {}
        # The wrapper builds only this package, so a match anywhere else is not sent.
        sent = set()
        for top in ("internal", "cmd"):
            for path in (ROOT / top).rglob("*_test.go"):
                for match in re.finditer(
                    r"^func (\w+)\([^\n]*\{(?:[^\n]*\}\n|\n.*?^\}\n)",
                    path.read_text(),
                    re.MULTILINE | re.DOTALL,
                ):
                    name = match.group(1)
                    functions[name] = match.group(0)
                    if path.parent == ROOT / "internal/api" and re.search(pattern, name):
                        sent.add(name)
        readers = {name for name, body in functions.items() if "RESTMAIL_VERTRAG_BINARY" in body}
        self.assertTrue(readers, "no test reads the binary: this check looks in the wrong place")
        unsent = [
            name
            for name, body in functions.items()
            if name.startswith("Test")
            and name not in sent
            and (name in readers or any(re.search(rf"\b{reader}\b", body) for reader in readers))
        ]
        self.assertEqual(sorted(unsent), [], "these skip in every run")


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
                'if [ "$1" = contract:pairs ]; then '
                '[ "$RESTMAIL_VERTRAG_BINARY" = "$PWD/.cache/bin/vertrag" ] || exit 97; '
                'exit "${PAIRS_EXIT:-0}"; fi\n'
                'exit "${FIXTURE_EXIT:-0}"\n',
                "python3": 'if [ "${1:-}" = -B ]; then shift; fi\n'
                'case "$1" in *substrate_pins.py) shift; echo "substrate:$*" >> "$CALLS"; '
                'exit "${SUBSTRATE_EXIT:-0}";; '
                '*vertrag_pin.py) echo pin >> "$CALLS"; '
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
                'ownership) exit "${OWNERSHIP_EXIT:-0}";; esac\n',
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
                timeout=600,
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
                "substrate:preflight --report",
                "chore:contract:pairs",
                "chore:test:contract-fixtures",
                "chore:test:contract-api-keys",
                "chore:test:contract-mfa-admin",
                "chore:api:coverage:reset",
                "run:gate --tag Domains",
                "scan",
                # The gate's examples and coverage processes have consumed
                # the held queue entries the authorization run's links need.
                "chore:test:contract-fixtures",
                "run:authorization --tag Domains",
                "chore:test:contract-domain-tenants",
                "run:ownership --tag Domains",
                "chore:api:coverage:report",
                "summary:tests/contract/summary.py .cache/contract examples coverage authorization ownership ownership-domains ownership-mailboxes ownership-messages",
            ],
        )

    def test_secret_scan_is_required_and_authorization_remains_report_only(self):
        for scan, auth, expected in (("1", "0", 1), ("0", "1", 0)):
            with self.subTest(scan=scan, auth=auth):
                result, _ = self.run_command(
                    SCAN_EXIT=scan, AUTH_EXIT=auth, OWNERSHIP_EXIT=auth
                )
                self.assertEqual(result.returncode, expected, result.stderr)

    def test_ci_can_run_the_same_gates_without_serial_reports(self):
        result, calls = self.run_command(CONTRACT_REPORTS="false")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("run:gate --tag Domains", calls)
        self.assertIn("scan", calls)
        self.assertIn("chore:test:contract-api-keys", calls)
        self.assertNotIn("run:authorization --tag Domains", calls)
        self.assertNotIn("run:ownership --tag Domains", calls)

    def test_fixture_failure_prevents_contract_checks(self):
        result, calls = self.run_command(FIXTURE_EXIT="1")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(
            calls,
            [
                "pin",
                "substrate:preflight --report",
                "chore:contract:pairs",
                "chore:test:contract-fixtures",
                "summary:tests/contract/summary.py .cache/contract examples coverage authorization ownership ownership-domains ownership-mailboxes ownership-messages",
            ],
        )

    def test_example_pair_failure_stops_before_topology_and_fixtures(self):
        # In CI the pair checks failed the job before any contract phase ran.
        for endpoint in ("http://fixture.test:8080", ""):
            with self.subTest(endpoint=endpoint):
                result, calls = self.run_command(
                    principals=False, CONTRACT_ENDPOINT=endpoint, PAIRS_EXIT="1"
                )
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(
                    calls,
                    [
                        "pin",
                        "substrate:preflight" + (" --report" if endpoint else ""),
                        "chore:contract:pairs",
                        "summary:tests/contract/summary.py .cache/contract examples coverage authorization ownership ownership-domains ownership-mailboxes ownership-messages",
                    ],
                )

    def test_a_fresh_topology_prepares_only_the_principal_fixture_first(self):
        result, calls = self.run_command(principals=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0], "pin")
        self.assertEqual(calls[1], "substrate:preflight --report")
        self.assertEqual(calls[2], "chore:contract:pairs")
        self.assertEqual(calls[3], "chore:test:e2e -- -run ^TestContractPrincipals$")
        self.assertEqual(calls[4], "chore:test:contract-fixtures")

    def test_default_endpoint_starts_the_topology_before_principal_setup(self):
        result, calls = self.run_command(principals=False, CONTRACT_ENDPOINT="")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0], "pin")
        self.assertEqual(calls[1], "substrate:preflight")
        self.assertEqual(calls[2], "chore:contract:pairs")
        self.assertEqual(calls[3], "chore:e2e:up")
        self.assertEqual(calls[4], "chore:test:e2e -- -run ^TestContractPrincipals$")
        self.assertEqual(sum(call.startswith("summary:") for call in calls), 1)

    def test_a_substrate_other_than_ci_stops_before_pairs_and_topology(self):
        # The preflight fails only when this run brings the topology up (#1751).
        result, calls = self.run_command(
            principals=False, CONTRACT_ENDPOINT="", SUBSTRATE_EXIT="1"
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(
            calls,
            [
                "pin",
                "substrate:preflight",
                "summary:tests/contract/summary.py .cache/contract examples coverage authorization ownership ownership-domains ownership-mailboxes ownership-messages",
            ],
        )

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
                "summary:tests/contract/summary.py .cache/contract examples coverage authorization ownership ownership-domains ownership-mailboxes ownership-messages",
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
                        timeout=600,
                        check=False,
                    )
                    self.assertEqual(result.returncode, 17, result.stderr)
                    self.assertNotIn("| " + stale, result.stdout)
                    self.assertEqual(
                        (reports_dir / "summary.md").exists(), not deferred
                    )


class ExamplePairsCommand(unittest.TestCase):
    def run_pairs(self, pattern="WithVertrag$", shards=3, **settings):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            (work / "internal/api").mkdir(parents=True)
            go = work / "go"
            go.write_text('#!/bin/sh\ncp "$FAKE_API_TEST" "$4"\n')
            go.chmod(0o755)
            api_test = work / "fake-api.test"
            api_test.write_text(
                f"#!{sys.executable}\n"
                "import json, os, re, sys\n"
                "from pathlib import Path\n"
                "assert Path.cwd().name == 'api'\n"
                "names = ['TestAWithVertrag', 'TestBWithVertrag', 'TestCWithVertrag',\n"
                "         'TestDWithVertrag', 'TestWithVertrag', 'TestOther',\n"
                "         'BenchmarkOtherWithVertrag', 'ExampleOtherWithVertrag']\n"
                "if sys.argv[1] == '-test.list':\n"
                "    if os.environ.get('LIST_EXIT'):\n"
                "        sys.exit(int(os.environ['LIST_EXIT']))\n"
                "    pattern = re.compile(sys.argv[2])\n"
                "    print('\\n'.join(name for name in names if pattern.search(name)))\n"
                "else:\n"
                "    pattern = re.compile(sys.argv[sys.argv.index('-test.run') + 1])\n"
                "    selected = [name for name in names if pattern.search(name)]\n"
                "    with open(os.environ['CALLS'], 'a') as calls:\n"
                "        calls.write(json.dumps(selected) + '\\n')\n"
                "    if os.environ.get('FAIL_TEST') in selected:\n"
                "        sys.exit(1)\n"
            )
            api_test.chmod(0o755)
            calls = work / "calls"
            result = subprocess.run(
                [
                    "bash",
                    str(ROOT / "tests/contract/example_pairs.sh"),
                    "-run",
                    pattern,
                    str(shards),
                ],
                cwd=work,
                env=dict(
                    os.environ,
                    PATH=str(work) + os.pathsep + os.environ["PATH"],
                    FAKE_API_TEST=str(api_test),
                    CALLS=str(calls),
                    **settings,
                ),
                text=True,
                capture_output=True,
                timeout=600,
                check=False,
            )
            runs = (
                [json.loads(line) for line in calls.read_text().splitlines()]
                if calls.exists()
                else []
            )
            return result, runs

    def test_pattern_discovers_tests_and_shards_each_match_once(self):
        result, runs = self.run_pairs()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sorted(map(len, runs)), [1, 2, 2])
        self.assertEqual(
            sorted(name for run in runs for name in run),
            [
                "TestAWithVertrag",
                "TestBWithVertrag",
                "TestCWithVertrag",
                "TestDWithVertrag",
                "TestWithVertrag",
            ],
        )

    def test_explicit_names_still_support_narrowed_runs(self):
        result, runs = self.run_pairs(
            pattern="^(TestBWithVertrag|TestCWithVertrag)$", shards=5
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sorted(runs), [["TestBWithVertrag"], ["TestCWithVertrag"]])

    def test_no_matches_fail_before_running_shards(self):
        result, runs = self.run_pairs(pattern="MissingWithVertrag$")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("no tests match pattern:", result.stderr)
        self.assertEqual(runs, [])

    def test_listing_errors_fail_before_running_shards(self):
        result, runs = self.run_pairs(LIST_EXIT="23")
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertEqual(runs, [])

    def test_failed_shard_fails_the_command(self):
        result, runs = self.run_pairs(FAIL_TEST="TestBWithVertrag")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(len(runs), 3)
        self.assertIn("FAILED", result.stdout)

    def test_stray_arguments_are_refused_before_building(self):
        # CI once passed `-count=1 -v` after the pattern: the shard count became
        # "-count=1", no shard ran, and the step still passed.
        for extra in (["-count=1", "-v"], ["-count=1"], ["0"], ["two"]):
            with (
                self.subTest(extra=extra),
                tempfile.TemporaryDirectory() as directory,
            ):
                work = Path(directory)
                go = work / "go"
                go.write_text('#!/bin/sh\necho built >> "$CALLS"\n')
                go.chmod(0o755)
                calls = work / "calls"
                result = subprocess.run(
                    ["bash", str(ROOT / "tests/contract/example_pairs.sh"), "-run"]
                    + ["^(TestAWithVertrag|TestBWithVertrag)$"]
                    + extra,
                    cwd=work,
                    env=dict(
                        os.environ,
                        PATH=str(work) + os.pathsep + os.environ["PATH"],
                        CALLS=str(calls),
                    ),
                    text=True,
                    capture_output=True,
                    timeout=600,
                    check=False,
                )
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("usage:", result.stderr)
                self.assertFalse(calls.exists())


if __name__ == "__main__":
    unittest.main()
