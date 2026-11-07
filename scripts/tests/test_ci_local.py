"""The local gate must fail closed, select dependants and notice CI drift."""

import copy
import importlib.util
import io
import json
import os
import re
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("ci_local", ROOT / "scripts/ci_local.py")
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class Selection(unittest.TestCase):
    def packages(self):
        return [
            {"ImportPath": "mod/base", "Dir": "/repo/base"},
            {"ImportPath": "mod/api", "Dir": "/repo/api", "Imports": ["mod/base"]},
            {"ImportPath": "mod/cmd", "Dir": "/repo/cmd", "Imports": ["mod/api"]},
            {
                "ImportPath": "mod/check",
                "Dir": "/repo/check",
                "TestImports": ["mod/base"],
            },
            {
                "ImportPath": "mod/external",
                "Dir": "/repo/external",
                "XTestImports": ["mod/cmd"],
            },
            {"ImportPath": "mod/alone", "Dir": "/repo/alone"},
        ]

    def test_reverse_transitive_imports_include_test_imports(self):
        self.assertEqual(
            gate.select_packages(self.packages(), ["base/base.go"], Path("/repo")),
            ["mod/api", "mod/base", "mod/check", "mod/cmd", "mod/external"],
        )

    def test_non_go_fixture_selects_owning_package(self):
        self.assertEqual(
            gate.select_packages(
                self.packages(), ["alone/testdata/golden.json"], Path("/repo")
            ),
            ["mod/alone"],
        )

    def test_modules_and_unknown_go_files_select_everything(self):
        for name in (
            "go.mod",
            "go.sum",
            "deleted/file.go",
            "third_party/go-imap/client.go",
        ):
            with self.subTest(name=name):
                self.assertEqual(
                    len(gate.select_packages(self.packages(), [name], Path("/repo"))), 6
                )

    def test_docs_do_not_run_unrelated_go_tests(self):
        self.assertEqual(
            gate.select_packages(self.packages(), ["README.md"], Path("/repo")), []
        )

    def test_root_embed_package_does_not_own_every_script_or_document(self):
        packages = self.packages() + [
            {"ImportPath": "mod", "Dir": "/repo", "EmbedFiles": ["openapi.yaml"]}
        ]
        self.assertEqual(
            gate.select_packages(
                packages,
                [
                    "scripts/ci_local.py",
                    "tests/contract/README.md",
                    "chores.yml",
                    "README.md",
                ],
                Path("/repo"),
            ),
            [],
        )
        self.assertEqual(
            gate.select_packages(packages, ["openapi.yaml"], Path("/repo")), ["mod"]
        )

    def test_topology_only_package_is_left_to_the_e2e_job(self):
        packages = [
            {"ImportPath": "mod/tests/e2e", "Dir": str(ROOT / "tests/e2e")},
            {"ImportPath": "mod/internal/api", "Dir": str(ROOT / "internal/api")},
        ]
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), True)
            with (
                patch.object(local, "run", return_value=json.dumps(packages[0])),
                patch.object(gate, "json_stream", return_value=packages),
                patch.object(gate, "changed_files", return_value=[]),
                redirect_stdout(io.StringIO()) as output,
            ):
                local.selection()
        self.assertEqual(local.packages, ["mod/internal/api"])
        self.assertIn("tests/e2e", output.getvalue())

    def test_a_change_to_one_handler_package_still_runs_the_guard_packages(self):
        # The guards read other packages' source without importing them (#1942).
        directories = ["internal/api", "internal/api/handlers", "internal/mail"]
        directories += list(gate.GUARD_PACKAGES)
        packages = [
            {"ImportPath": "mod/" + directory, "Dir": str(ROOT / directory)}
            for directory in directories
        ]
        packages[0]["Imports"] = ["mod/internal/api/handlers"]
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            with (
                patch.object(local, "run", return_value=""),
                patch.object(gate, "json_stream", return_value=packages),
                patch.object(
                    gate,
                    "changed_files",
                    return_value=["internal/api/handlers/messages_test.go"],
                ),
                redirect_stdout(io.StringIO()),
            ):
                local.selection()
        self.assertEqual(
            sorted(local.packages),
            sorted(
                ["mod/internal/api", "mod/internal/api/handlers"]
                + ["mod/" + directory for directory in gate.GUARD_PACKAGES]
            ),
        )

    def test_the_guard_packages_are_on_this_tree_and_each_walks_it(self):
        for directory in gate.GUARD_PACKAGES:
            with self.subTest(directory=directory):
                tests = "".join(
                    path.read_text()
                    for path in (ROOT / directory).glob("*_test.go")
                )
                self.assertRegex(tests, r"filepath\.WalkDir|filepath\.Glob\(\"\.\./")

    def test_json_stream_is_not_assumed_to_be_one_document(self):
        self.assertEqual(gate.json_stream('{"a":1}\n {"a":2}\n'), [{"a": 1}, {"a": 2}])


class LinuxGoTests(unittest.TestCase):
    def setUp(self):
        import yaml

        self.temporary = tempfile.TemporaryDirectory(prefix="restmail-2013-tests-")
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.local = gate.Gate(ROOT, self.directory, self.directory, False, "2013")
        self.loadgen = "mod/tests/load/loadgen"
        self.local.workflow = yaml.safe_load(
            (ROOT / ".github/workflows/ci.yml").read_text()
        )
        self.version = next(
            step["with"]["go-version"]
            for step in self.local.workflow["jobs"]["lint"]["steps"]
            if step["name"] == "Set up Go"
        )
        self.local.env["GOTOOLCHAIN"] = "go" + self.version
        self.caches = {
            "GOMODCACHE": str(self.directory / "module cache"),
            "GOCACHE": str(self.directory / "build cache"),
        }

    def select(self, platform, files):
        packages = [
            {"ImportPath": self.loadgen, "Dir": str(ROOT / "tests/load/loadgen")},
            {"ImportPath": "mod/internal/api", "Dir": str(ROOT / "internal/api")},
        ]
        with (
            patch.object(gate.sys, "platform", platform),
            patch.object(self.local, "run", return_value=""),
            patch.object(gate, "json_stream", return_value=packages),
            patch.object(gate, "changed_files", return_value=files),
            redirect_stdout(io.StringIO()),
        ):
            self.local.selection()

    def test_darwin_selects_loadgen_for_linux_and_never_probes_host_aliases(self):
        self.select("darwin", ["tests/load/loadgen/main.go"])
        self.assertIn(self.loadgen, self.local.packages)
        self.assertEqual(self.local.linux_packages, [self.loadgen])
        with patch.object(gate, "unbindable_loopbacks") as probe:
            self.assertIn("NOT REQUIRED", self.local.loopback_aliases())
        probe.assert_not_called()

    def test_linux_keeps_the_existing_host_selection_and_alias_check(self):
        self.select("linux", ["tests/load/loadgen/main.go"])
        self.assertEqual(self.local.packages, [self.loadgen])
        self.assertEqual(self.local.linux_packages, [])
        with (
            patch.object(gate, "unbindable_loopbacks", return_value=[]) as probe,
            redirect_stdout(io.StringIO()),
        ):
            self.local.loopback_aliases()
        probe.assert_called_once_with()

    def test_unselected_linux_packages_do_not_start_docker(self):
        self.select("darwin", ["README.md"])
        with patch.object(self.local, "run") as commands:
            self.assertIn("NOT REQUIRED", self.local.linux_go_tests())
        commands.assert_not_called()

    def test_all_mode_still_routes_loadgen_to_linux(self):
        self.local.all_packages = True
        self.select("darwin", ["README.md"])
        self.assertEqual(self.local.linux_packages, [self.loadgen])
        self.assertIn("mod/internal/api", self.local.packages)

    def test_container_uses_ci_pin_host_caches_and_no_host_network_changes(self):
        self.local.linux_packages = [self.loadgen]
        with (
            patch.object(
                self.local, "run", side_effect=["", json.dumps(self.caches)]
            ) as commands,
            patch.object(gate.subprocess, "run") as docker,
            patch.object(self.local, "run_go_test") as tests,
        ):
            docker.return_value.returncode = 0
            self.local.linux_go_tests()
        args = tests.call_args.args[0]
        self.assertEqual(args[:3], ["docker", "run", "--rm"])
        self.assertEqual(args[args.index("--name") + 1], self.local.linux_container)
        self.assertTrue(
            self.local.linux_container.startswith("restmail-2013-linux-go-")
        )
        self.assertTrue(self.local.linux_container.endswith(self.local.run_id))
        self.assertIn("rest-mail-loop=2013", args)
        self.assertIn("rest-mail.gate=" + self.local.run_id, args)
        self.assertIn(f"{os.getuid()}:{os.getgid()}", args)
        self.assertIn(
            "type=bind,source=" + str(ROOT) + ",target=/workspace,readonly", args
        )
        for key, target in (("GOMODCACHE", "/go/pkg/mod"), ("GOCACHE", "/go-build")):
            self.assertIn(f"type=bind,source={self.caches[key]},target={target}", args)
            self.assertIn(f"{key}={target}", args)
        self.assertIn("GOTOOLCHAIN=local", args)
        self.assertIn("ENVIRONMENT=test", args)
        self.assertIn("GOMAXPROCS=" + self.local.env["GOMAXPROCS"], args)
        image_index = args.index("golang:" + self.version)
        self.assertEqual(
            args[image_index + 1:],
            [
                "go", "test", "-race", "-count=1", "-timeout=20m",
                "-skip", "WithVertrag$", self.loadgen,
            ],
        )
        for forbidden in ("sudo", "ifconfig", "--privileged", "--network", "-p", "-exec"):
            self.assertNotIn(forbidden, args)
        self.assertEqual(
            commands.call_args.args[0],
            ["go", "env", "-json", "GOMODCACHE", "GOCACHE"],
        )
        docker.assert_called_once_with(
            ["docker", "image", "inspect", "golang:" + self.version],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        self.assertFalse(self.local.created_linux_image)

    def test_missing_image_is_pulled_and_tracked_for_cleanup(self):
        self.local.linux_packages = [self.loadgen]
        with (
            patch.object(
                self.local, "run", side_effect=["", "", json.dumps(self.caches)]
            ) as commands,
            patch.object(gate.subprocess, "run") as docker,
            patch.object(self.local, "run_go_test"),
        ):
            docker.return_value.returncode = 1
            self.local.linux_go_tests()
        self.assertEqual(
            commands.call_args_list[1].args[0],
            ["docker", "pull", "golang:" + self.version],
        )
        self.assertTrue(self.local.created_linux_image)

    def test_unavailable_docker_fails_closed_before_any_tests(self):
        self.local.linux_packages = [self.loadgen]
        with (
            patch.object(
                self.local, "run",
                side_effect=subprocess.CalledProcessError(1, ["docker", "info"]),
            ),
            patch.object(self.local, "run_go_test") as tests,
            self.assertRaisesRegex(gate.CouldNotRun, "start Docker"),
        ):
            self.local.linux_go_tests()
        tests.assert_not_called()

    def test_container_tests_never_run_again_on_the_host(self):
        self.local.packages = [self.loadgen, "mod/ordinary"]
        self.local.linux_packages = [self.loadgen]
        with patch.object(self.local, "run_go_test") as tests:
            self.local.go_tests()
        self.assertEqual(tests.call_count, 1)
        self.assertNotIn(self.loadgen, tests.call_args.args[0])
        self.assertIn("mod/ordinary", tests.call_args.args[0])
        self.assertIn("-exec", tests.call_args.args[0])

    def test_container_failures_and_timeouts_keep_their_verdicts(self):
        self.local.linux_packages = [self.loadgen]
        for output, error_type in (
            ("--- FAIL: TestSources\n", subprocess.CalledProcessError),
            (gate.TIMEOUT_MARK + " after 20m\n", gate.NoVerdict),
        ):
            def run(args, **kwargs):
                if args[:2] == ["go", "env"]:
                    return json.dumps(self.caches)
                if "log" in kwargs:
                    kwargs["log"].write_text(output)
                    raise subprocess.CalledProcessError(1, args)
                return ""

            with (
                self.subTest(output=output),
                patch.object(self.local, "run", side_effect=run),
                patch.object(gate.subprocess, "run") as docker,
                self.assertRaises(error_type),
            ):
                docker.return_value.returncode = 0
                self.local.linux_go_tests()

    def test_cleanup_removes_only_its_own_surviving_container_and_pulled_image(self):
        self.local.linux_container = "restmail-2013-linux-go-" + self.local.run_id
        self.local.linux_image = "golang:" + self.version
        self.local.created_linux_image = True
        with patch.object(gate.subprocess, "run") as docker:
            docker.return_value.returncode = 0
            self.local.cleanup()
        self.assertEqual(
            [call.args[0] for call in docker.call_args_list],
            [
                ["docker", "container", "inspect", self.local.linux_container],
                ["docker", "rm", "-fv", self.local.linux_container],
                ["docker", "image", "rm", self.local.linux_image],
            ],
        )

    def test_cleanup_accepts_an_already_auto_removed_container(self):
        self.local.linux_container = "restmail-2013-linux-go-" + self.local.run_id
        with patch.object(gate.subprocess, "run") as docker:
            docker.return_value.returncode = 1
            self.local.cleanup()
        self.assertEqual(docker.call_count, 1)

    def test_linux_step_precedes_database_and_host_tests(self):
        names = [name for name, action in self.local.steps()]
        self.assertLess(names.index("Linux Go tests"), names.index("PostgreSQL"))
        self.assertLess(names.index("Linux Go tests"), names.index("Go tests"))


class Verdicts(unittest.TestCase):
    def test_failure_and_unavailable_never_become_passes(self):
        for error, status in (
            (subprocess.CalledProcessError(1, ["false"]), "FAIL"),
            (gate.CouldNotRun("missing docker"), "COULD-NOT-RUN"),
        ):
            calls = []
            with redirect_stdout(io.StringIO()) as output:
                result = gate.run_steps(
                    [
                        ("first", lambda error=error: (_ for _ in ()).throw(error)),
                        ("second", lambda calls=calls: calls.append("wrong")),
                    ]
                )
            self.assertEqual(result, 1)
            self.assertEqual(calls, [])
            self.assertIn(f"{status}: first", output.getvalue())
            self.assertIn("COULD-NOT-RUN: second", output.getvalue())
            self.assertNotIn("PASS:", output.getvalue())

    def test_not_required_is_explicit_not_a_pass(self):
        with redirect_stdout(io.StringIO()) as output:
            self.assertEqual(
                gate.run_steps([("selection", lambda: "NOT REQUIRED: no Go changes")]),
                0,
            )
        self.assertIn("NOT REQUIRED", output.getvalue())
        self.assertNotIn("PASS: selection", output.getvalue())

    def test_a_subtest_a_vertrag_example_skips_itself_is_named_not_a_failure(self):
        # A gzip pair cannot travel through a Vertrag hook; the in-process test
        # sends it. Only a whole test skipping means Vertrag did not run.
        with redirect_stdout(io.StringIO()) as output:
            gate.verify_vertrag(
                [
                    {"Action": "pass", "Test": "TestAWithVertrag"},
                    {"Action": "skip", "Test": "TestAWithVertrag/stored_gzip"},
                ],
                ["TestAWithVertrag"],
            )
        self.assertIn("TestAWithVertrag/stored_gzip", output.getvalue())

    def test_a_warning_is_its_own_verdict(self):
        with redirect_stdout(io.StringIO()) as output:
            self.assertEqual(
                gate.run_steps(
                    [("Go tests", lambda: "WARNING: a shard hit its limit")]
                ),
                gate.EXIT_WARNED,
            )
        self.assertIn("WARN: Go tests", output.getvalue())
        self.assertNotIn("PASS: Go tests", output.getvalue())

    def test_a_warning_does_not_exit_zero_and_the_last_line_says_why(self):
        self.assertNotEqual(gate.EXIT_WARNED, 0)
        self.assertIn("not passed for enqueue", gate.VERDICTS[gate.EXIT_WARNED].lower())

    def test_a_time_limit_is_no_verdict_rather_than_a_pass_or_a_failure(self):
        def reached_its_limit():
            raise gate.NoVerdict("shard 1 reached its limit")

        with redirect_stdout(io.StringIO()) as output:
            code = gate.run_steps(
                [("Go tests", reached_its_limit), ("later", lambda: None)]
            )
        self.assertEqual(code, gate.EXIT_NO_VERDICT)
        self.assertNotEqual(code, 0)
        self.assertIn("NO VERDICT: Go tests", output.getvalue())
        self.assertNotIn("PASS:", output.getvalue())
        self.assertNotIn("FAIL:", output.getvalue())
        self.assertIn("COULD-NOT-RUN: later", output.getvalue())

    def test_a_failure_outranks_a_warning_before_it(self):
        def fails():
            raise RuntimeError("broken")

        with redirect_stdout(io.StringIO()):
            self.assertEqual(
                gate.run_steps([("first", lambda: "WARNING: slow"), ("second", fails)]),
                gate.EXIT_FAILED,
            )

    def test_an_inner_run_hands_its_verdict_to_the_outer_step(self):
        self.assertIsNone(gate.carry_up(gate.EXIT_PASSED))
        self.assertTrue(gate.carry_up(gate.EXIT_WARNED).startswith("WARNING"))
        with self.assertRaises(gate.NoVerdict):
            gate.carry_up(gate.EXIT_NO_VERDICT)
        with self.assertRaisesRegex(RuntimeError, "do not push"):
            gate.carry_up(gate.EXIT_FAILED)

    def test_vertrag_skips_or_empty_selection_fail(self):
        for events in (
            [],
            [{"Action": "skip", "Test": "TestAWithVertrag"}],
            [{"Action": "pass", "Test": "TestAWithVertrag"}],
        ):
            with self.subTest(events=events):
                if events and events[0]["Action"] == "pass":
                    gate.verify_vertrag(events, ["TestAWithVertrag"])
                else:
                    with self.assertRaises(RuntimeError):
                        gate.verify_vertrag(events, ["TestAWithVertrag"])


class TestEvents(unittest.TestCase):
    def test_a_stray_line_is_kept_and_does_not_lose_the_events_around_it(self):
        events, stray = gate.parse_events(
            '{"Action":"pass","Test":"TestA"}\n'
            "go: not a test event\n"
            "\n"
            "[1]\n"
            '{"Action":"fail","Test":"TestB"}\n'
        )
        self.assertEqual(
            [event["Test"] for event in events],
            ["TestA", "TestB"],
        )
        self.assertEqual(stray, ["go: not a test event", "[1]"])

    def test_failure_report_names_failed_tests_with_their_output_only(self):
        names, report = gate.failure_report(
            [
                {"Action": "output", "Test": "TestA", "Output": "noise\n"},
                {"Action": "pass", "Test": "TestA"},
                {"Action": "output", "Test": "TestB", "Output": "owner missing\n"},
                {"Action": "fail", "Test": "TestB"},
            ]
        )
        self.assertEqual(names, ["TestB"])
        self.assertIn("owner missing", report)
        self.assertNotIn("noise", report)

    def test_a_timed_out_shard_is_told_from_a_failed_test(self):
        self.assertTrue(
            gate.timed_out(
                [{"Action": "output", "Output": "panic: test timed out after 20m0s\n"}]
            )
        )
        self.assertFalse(
            gate.timed_out([{"Action": "output", "Test": "TestA", "Output": "no\n"}])
        )

    def test_a_timed_out_shard_reports_the_panic_and_not_the_whole_dump(self):
        names, report = gate.failure_report(
            [{"Action": "output", "Output": "=== RUN TestDone\n"}]
            + [{"Action": "output", "Output": "panic: test timed out after 20m0s\n"}]
            + [
                {"Action": "output", "Output": f"goroutine {number}\n"}
                for number in range(1000)
            ]
            + [{"Action": "fail", "Test": "TestRunning"}]
        )
        self.assertEqual(names, ["TestRunning"])
        self.assertTrue(report.startswith("panic: test timed out"))
        self.assertLess(len(report.splitlines()), 100)

    def test_a_large_log_with_one_failed_test_is_read_in_seconds(self):
        # A 36-minute spin on such a log, asking per line whether it timed out (#1942).
        lines = []
        for package in range(40):
            name = f"mod/internal/package{package}"
            for test in range(250):
                test = f"TestPackage{package}Case{test}"
                lines.append(json.dumps({"Action": "run", "Package": name, "Test": test}))
                for line in range(20):
                    lines.append(
                        json.dumps(
                            {
                                "Action": "output",
                                "Package": name,
                                "Test": test,
                                "Output": f"2026/11/06 03:35:51 SELECT * FROM rows {line} "
                                + "x" * 120
                                + "\n",
                            }
                        )
                    )
                failing = test == "TestPackage17Case123"
                if failing:
                    lines.append(
                        json.dumps(
                            {
                                "Action": "output",
                                "Package": name,
                                "Test": test,
                                "Output": "    send_test.go:96: send 97 of the hour's 100: 503\n",
                            }
                        )
                    )
                lines.append(
                    json.dumps(
                        {
                            "Action": "fail" if failing else "pass",
                            "Package": name,
                            "Test": test,
                            "Elapsed": 0.1,
                        }
                    )
                )
        text = "\n".join(lines) + "\n"
        self.assertGreater(len(text), 30_000_000)

        def too_slow(signum, frame):
            raise AssertionError("reading the log did not finish within 60 seconds")

        previous = signal.signal(signal.SIGALRM, too_slow)
        signal.alarm(60)
        try:
            events, stray = gate.parse_events(text)
            started = time.monotonic()
            names, report = gate.failure_report(events)
            limited = gate.timed_out(events)
            first, cut_off = gate.split_at_limit(events)
            elapsed = time.monotonic() - started
        finally:
            signal.alarm(0)
            signal.signal(signal.SIGALRM, previous)
        self.assertEqual(stray, [])
        self.assertEqual(names, ["TestPackage17Case123"])
        self.assertTrue("send 97 of the hour's 100" in report, report[-1000:])
        self.assertFalse(limited)
        self.assertEqual((first, cut_off), (["TestPackage17Case123"], []))
        self.assertLess(elapsed, 5)

    def test_failure_with_no_named_test_keeps_the_package_output(self):
        names, report = gate.failure_report(
            [
                {"Action": "output", "Output": "panic: test timed out after 20m0s\n"},
                {"Action": "fail"},
            ]
        )
        self.assertEqual(names, [])
        self.assertIn("panic: test timed out", report)


class WorkflowCoverage(unittest.TestCase):
    def setUp(self):
        import yaml

        self.workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        self.coverage = json.loads(
            (ROOT / "scripts/ci-local-coverage.json").read_text()
        )

    def test_current_workflow_is_covered(self):
        gate.check_coverage(self.workflow, self.coverage)

    def test_any_added_lint_or_test_step_is_red(self):
        for job in ("lint", "constellation", "contract-tooling", "test", "test-api", "test-handlers", "test-contract", "test-imap"):
            workflow = copy.deepcopy(self.workflow)
            workflow["jobs"][job]["steps"].append({"name": "New gate", "run": "false"})
            with (
                self.subTest(job=job),
                self.assertRaisesRegex(RuntimeError, "New gate"),
            ):
                gate.check_coverage(workflow, self.coverage)

    def test_changed_command_is_red_even_with_the_same_name(self):
        self.workflow["jobs"]["constellation"]["steps"][1]["run"] += "\nfalse"
        with self.assertRaisesRegex(RuntimeError, "Check constellation inventory"):
            gate.check_coverage(self.workflow, self.coverage)

    def test_exception_requires_a_reason_and_coverage_a_real_step(self):
        for replacement in ({"exception": ""}, {"local": "imaginary"}):
            coverage = copy.deepcopy(self.coverage)
            coverage["lint"]["Checkout code"].update(replacement)
            with self.subTest(replacement=replacement), self.assertRaises(RuntimeError):
                gate.check_coverage(self.workflow, coverage)


class VertragShards(unittest.TestCase):
    def test_selected_api_race_tests_use_shards_not_one_twenty_minute_package(
        self,
    ):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.packages = ["mod/internal/api"]
            with (
                patch.object(
                    local,
                    "run",
                    side_effect=[
                        "TestB\nTestA\nTestPairWithVertrag\nok mod/internal/api\n",
                        "",
                    ],
                ) as commands,
                patch.object(
                    local,
                    "shards",
                    create=True,
                    return_value=[
                        {"Action": "pass", "Test": "TestA"},
                        {"Action": "pass", "Test": "TestB"},
                    ],
                ) as shards,
            ):
                local.go_tests()
            self.assertEqual(shards.call_args.args[0], ["TestA", "TestB"])
            self.assertEqual(shards.call_args.args[3:5], (local.race_shards, "20m"))
            self.assertIn("-race", commands.call_args_list[1].args[0])

    def test_a_failing_guard_stops_the_step_before_the_race_shards(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.packages = ["mod/internal/api", "mod/internal/mail", "mod/guard"]
            local.guards = ["mod/guard"]
            failure = subprocess.CalledProcessError(1, ["go", "test"])
            with (
                patch.object(local, "run_go_test", side_effect=failure) as tests,
                patch.object(local, "run") as commands,
                patch.object(local, "shards", create=True) as shards,
                self.assertRaises(subprocess.CalledProcessError),
            ):
                local.go_tests()
            self.assertEqual(tests.call_args.args[0][-1:], ["mod/guard"])
            commands.assert_not_called()
            shards.assert_not_called()

    def test_selective_mode_does_not_start_database_or_unchanged_nested_tests(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            with patch.object(local, "run") as commands:
                self.assertIn("NOT REQUIRED", local.postgres())
                self.assertIn("NOT REQUIRED", local.imap())
                self.assertIn("NOT REQUIRED", local.fixtures())
            commands.assert_not_called()

    def test_imap_module_keeps_a_local_limit_longer_than_its_slowest_test(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), True)
            with (
                patch.object(
                    local, "linter", return_value=("/bin/lint", {"with": {"args": ""}})
                ),
                patch.object(local, "run") as commands,
            ):
                local.imap()
            test = next(
                call for call in commands.call_args_list if "-race" in call.args[0]
            )
            self.assertIn("-timeout=30m", test.args[0])
            self.assertEqual(test.kwargs["root"], ROOT / "third_party/go-imap")

    def test_the_imap_step_runs_each_gate_ci_runs_on_the_module(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), True)
            with (
                patch.object(
                    local,
                    "linter",
                    return_value=(
                        "/bin/lint",
                        {"with": {"args": "--timeout=5m --no-config"}},
                    ),
                ),
                patch.object(local, "run") as commands,
            ):
                local.imap()
            calls = [
                " ".join(str(part) for part in call.args[0])
                for call in commands.call_args_list
            ]
            for expected in (
                "unittest discover -s .github/tests",
                "go vet ./...",
                "/bin/lint run --timeout=5m --no-config",
                "go run ./internal/checklist pkg/imap/rfctest",
                "-covermode=atomic -coverpkg=./...",
                "check_coverage.py",
            ):
                self.assertTrue(
                    any(expected in call for call in calls),
                    f"{expected} not in {calls}",
                )
            for call in commands.call_args_list:
                self.assertEqual(call.kwargs["root"], ROOT / "third_party/go-imap")
            # The profile stays out of the tree, which a local run must not change.
            profile = next(call for call in calls if "-coverprofile=" in call)
            self.assertIn(f"-coverprofile={temporary}", profile)

    def test_the_32_bit_step_compiles_for_linux_and_does_not_claim_to_run(self):
        # macOS has no 386 at all, so the local step can only prove it builds, and the
        # manifest says CI alone runs those wire proofs.
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), True)
            with (
                patch.object(
                    local, "linter", return_value=("/bin/lint", {"with": {"args": ""}})
                ),
                patch.object(local, "run") as commands,
            ):
                local.imap()
            build = next(
                call
                for call in commands.call_args_list
                if call.kwargs.get("env", {}).get("GOARCH") == "386"
            )
            self.assertEqual(build.kwargs["env"]["GOOS"], "linux")
            self.assertEqual(build.kwargs["env"]["CGO_ENABLED"], "0")
            self.assertEqual(build.args[0][:2], ["go", "vet"])
        manifest = json.loads((ROOT / "scripts/ci-local-coverage.json").read_text())
        reason = manifest["test-imap"]["Check the IMAP module's 32-bit sequence dispatch"]
        self.assertIn("CI", reason["exception"])

    def test_nested_module_uses_the_same_pinned_go_toolchain(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.workflow = {
                "jobs": {
                    "lint": {
                        "steps": [
                            {"name": "Set up Go", "with": {"go-version": "1.26.10"}}
                        ]
                    }
                }
            }
            with patch.object(local, "run", return_value="go1.26.10\n"):
                local.go_toolchain()
            self.assertEqual(local.env["GOTOOLCHAIN"], "go1.26.10")

    def test_fixture_packages_run_once_in_their_own_step(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.packages = ["mod/tests/contract/fixtures", "mod/internal/mail"]
            with patch.object(local, "run") as commands:
                local.go_tests()
            ran = commands.call_args.args[0]
            self.assertIn("mod/internal/mail", ran)
            self.assertNotIn("mod/tests/contract/fixtures", ran)
            local.packages = ["mod/tests/contract/fixtures"]
            with patch.object(local, "run") as commands:
                self.assertIn("NOT REQUIRED", local.go_tests())
            commands.assert_not_called()

    def test_a_shard_failure_names_the_tests_rather_than_dumping_the_log(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    for event in (
                        {"Action": "output", "Test": "TestA", "Output": "not noise\n"},
                        {"Action": "fail", "Test": "TestA"},
                    ):
                        kwargs["stdout"].write(json.dumps(event) + "\n")

                def poll(self):
                    return 1

            with (
                patch.object(gate.subprocess, "Popen", Process),
                redirect_stdout(io.StringIO()) as output,
                self.assertRaisesRegex(RuntimeError, "shards failed.*TestA"),
            ):
                local.shards(["TestA"], "binary", "label", 4, "20m")
            self.assertIn("not noise", output.getvalue())
            self.assertNotIn('"Action"', output.getvalue())

    def test_a_shard_at_its_limit_says_so_and_does_not_blame_the_running_test(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    for event in (
                        {"Action": "pass", "Test": "TestDone"},
                        {
                            "Action": "output",
                            "Output": "panic: test timed out after 20m0s\n",
                        },
                        {"Action": "fail", "Test": "TestRunning"},
                    ):
                        kwargs["stdout"].write(json.dumps(event) + "\n")

                def poll(self):
                    return 2

            with (
                patch.object(gate.subprocess, "Popen", Process),
                redirect_stdout(io.StringIO()),
                self.assertRaises(RuntimeError) as raised,
            ):
                local.shards(["TestDone", "TestRunning"], "binary", "label", 1, "20m")
            message = str(raised.exception)
            self.assertIsInstance(raised.exception, gate.NoVerdict)
            self.assertRegex(message, r"hit the 20m limit at load \d")
            self.assertIn("running then: ['TestRunning']", message)
            self.assertIn("failed tests: []", message)
            self.assertIn("0 of 1 selected tests finished", message)

    def test_tests_a_shard_did_not_reach_run_once_more_and_the_step_warns(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            patterns = []

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    patterns.append(args[-1])
                    self.code = 0
                    if len(patterns) == 1:
                        self.code = 2
                        events = (
                            {"Action": "pass", "Test": "TestDone"},
                            {
                                "Action": "output",
                                "Output": "panic: test timed out after 20m0s\n",
                            },
                            {"Action": "fail", "Test": "TestRunning"},
                        )
                    else:
                        events = ({"Action": "pass", "Test": "TestRunning"},)
                    for event in events:
                        kwargs["stdout"].write(json.dumps(event) + "\n")

                def poll(self):
                    return self.code

            with (
                patch.object(gate.subprocess, "Popen", Process),
                redirect_stdout(io.StringIO()),
            ):
                events = local.shards(
                    ["TestDone", "TestRunning"], "binary", "label", 1, "20m"
                )
            self.assertEqual(patterns[1], "^(TestRunning)$")
            self.assertIn({"Action": "pass", "Test": "TestDone"}, events)
            self.assertIn({"Action": "pass", "Test": "TestRunning"}, events)
            self.assertIn("TestRunning", local.warning())
            self.assertTrue(local.warning().startswith("WARNING"))

    def test_a_failed_test_is_not_run_again_even_beside_a_timed_out_shard(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            patterns = []

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    patterns.append(args[-1])
                    events = [{"Action": "fail", "Test": args[-1][2:-2]}]
                    if "TestSlow" in args[-1]:
                        events.insert(
                            0,
                            {
                                "Action": "output",
                                "Output": "panic: test timed out after 20m0s\n",
                            },
                        )
                    for event in events:
                        kwargs["stdout"].write(json.dumps(event) + "\n")

                def poll(self):
                    return 1

            with (
                patch.object(gate.subprocess, "Popen", Process),
                redirect_stdout(io.StringIO()),
                self.assertRaisesRegex(RuntimeError, "failed tests: \\['TestBad'\\]"),
            ):
                local.shards(["TestBad", "TestSlow"], "binary", "label", 2, "20m")
            self.assertEqual(len(patterns), 2)

    def test_tests_that_reach_the_limit_a_second_time_are_no_verdict(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            runs = []

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    runs.append(args[-1])
                    for event in (
                        {
                            "Action": "output",
                            "Output": "panic: test timed out after 60m0s\n",
                        },
                        {"Action": "fail", "Test": "TestSlowWithVertrag"},
                    ):
                        kwargs["stdout"].write(json.dumps(event) + "\n")

                def poll(self):
                    return 2

            with (
                patch.object(gate.subprocess, "Popen", Process),
                redirect_stdout(io.StringIO()),
                self.assertRaisesRegex(gate.NoVerdict, "TestSlowWithVertrag"),
            ):
                local.shards(["TestSlowWithVertrag"], "binary", "Vertrag", 3, "60m")
            self.assertEqual(len(runs), 2)

    def test_a_test_that_failed_before_the_limit_stays_failed(self):
        # A shard can fail a test and later reach its limit. The failure is real,
        # so it must not be cut off, run again and end as a warning.
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            runs = []

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    runs.append(args[-1])
                    for event in (
                        {"Action": "fail", "Test": "TestBad"},
                        {
                            "Action": "output",
                            "Output": "panic: test timed out after 20m0s\n",
                        },
                        {"Action": "fail", "Test": "TestRunning"},
                    ):
                        kwargs["stdout"].write(json.dumps(event) + "\n")

                def poll(self):
                    return 2

            with (
                patch.object(gate.subprocess, "Popen", Process),
                redirect_stdout(io.StringIO()),
                self.assertRaises(RuntimeError) as raised,
            ):
                local.shards(["TestBad", "TestRunning"], "binary", "label", 1, "20m")
            self.assertNotIsInstance(raised.exception, gate.NoVerdict)
            self.assertIn("failed tests: ['TestBad']", str(raised.exception))
            self.assertEqual(len(runs), 1)

    def test_a_handlers_shard_failure_says_the_split_may_be_the_cause(self):
        # The package depends on test order: a round-robin shard had 44 failures
        # while the whole package passed 910 of 910 (#1941).
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    event = {"Action": "fail", "Test": "TestA"}
                    kwargs["stdout"].write(json.dumps(event) + "\n")

                def poll(self):
                    return 1

            for directory, named in (
                ("internal/api/handlers", True),
                ("internal/api", False),
            ):
                with (
                    self.subTest(directory=directory),
                    patch.object(gate.subprocess, "Popen", Process),
                    redirect_stdout(io.StringIO()),
                    self.assertRaises(RuntimeError) as raised,
                ):
                    local.shards(
                        ["TestA"], "binary", "label", 8, "20m", directory, False
                    )
                self.assertEqual(
                    "depends on test order" in str(raised.exception), named
                )

    def test_vertrag_shards_allow_more_than_a_healthy_run_took_under_load(self):
        # The *WithVertrag tests took about 32 minutes at load 50 to 85.
        self.assertGreaterEqual(int(gate.VERTRAG_TIMEOUT.removesuffix("m")), 60)
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), True)
            local.api_changed = True
            with (
                patch.object(
                    local,
                    "run",
                    side_effect=["/bin/vertrag\n", "TestAWithVertrag", ""],
                ),
                patch.object(
                    local,
                    "shards",
                    return_value=[{"Action": "pass", "Test": "TestAWithVertrag"}],
                ) as shards,
                redirect_stdout(io.StringIO()),
            ):
                local.vertrag()
            self.assertEqual(shards.call_args.args[4], gate.VERTRAG_TIMEOUT)

    def test_a_go_test_outside_the_shards_that_times_out_is_no_verdict(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            script = (
                "echo 'panic: test timed out after 20m0s'; echo 'running tests:';"
                "printf '\\tTestSlow (20m0s)\\n'; exit 1"
            )
            with (
                redirect_stdout(io.StringIO()) as output,
                self.assertRaisesRegex(gate.NoVerdict, "TestSlow"),
            ):
                local.run_go_test(["sh", "-c", script])
            self.assertIn("panic: test timed out", output.getvalue())

    def test_a_go_test_failure_beside_a_timeout_stays_a_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            script = "echo '--- FAIL: TestBad'; echo 'panic: test timed out'; exit 1"
            with (
                redirect_stdout(io.StringIO()),
                self.assertRaises(subprocess.CalledProcessError),
            ):
                local.run_go_test(["sh", "-c", script])

    def test_every_go_test_step_reports_a_timeout_as_no_verdict(self):
        def timed_out(args, **kwargs):
            if "log" not in kwargs:
                return ""
            kwargs["log"].write_text("panic: test timed out after 30m0s\n")
            raise subprocess.CalledProcessError(1, args)

        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), True)
            local.packages = ["mod/internal/mail"]
            local.container = "container"
            for step in (local.imap, local.go_tests):
                with (
                    self.subTest(step=step.__name__),
                    patch.object(
                        local,
                        "linter",
                        return_value=("/bin/lint", {"with": {"args": ""}}),
                    ),
                    patch.object(local, "run", side_effect=timed_out),
                    self.assertRaises(gate.NoVerdict),
                ):
                    step()

            def fixtures_time_out(args, **kwargs):
                if args[0] == "docker":
                    return ""
                return timed_out(args, **kwargs)

            with (
                patch.object(local, "run", side_effect=fixtures_time_out),
                self.assertRaises(gate.NoVerdict),
            ):
                local.fixtures()

            def faults_time_out(args, **kwargs):
                if "test" not in args:
                    return "tag"
                return timed_out(args, **kwargs)

            with (
                patch.object(local, "run", side_effect=faults_time_out),
                self.assertRaises(gate.NoVerdict),
            ):
                local.faults()

    def test_the_imap_step_runs_for_every_path_that_makes_ci_run_it(self):
        script = (ROOT / "scripts/ci/go-imap-changed").read_text()
        paths = re.search(r"^PATHS=\((.*)\)$", script, re.MULTILINE).group(1).split()
        self.assertIn("third_party/go-imap", paths)
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            for path in paths:
                changed = path + "/file" if path == "third_party/go-imap" else path
                local.files = [changed]
                with (
                    self.subTest(path=path),
                    patch.object(
                        local,
                        "linter",
                        return_value=("/bin/lint", {"with": {"args": ""}}),
                    ),
                    patch.object(local, "run") as commands,
                ):
                    self.assertIsNone(local.imap())
                    commands.assert_called()

    def test_a_stray_line_in_a_passing_shard_is_reported_and_is_not_a_crash(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    kwargs["stdout"].write("stray from a child\n")
                    kwargs["stdout"].write('{"Action":"pass","Test":"TestA"}\n')

                def poll(self):
                    return 0

            with (
                patch.object(gate.subprocess, "Popen", Process),
                redirect_stdout(io.StringIO()) as output,
            ):
                events = local.shards(["TestA"], "binary", "label", 4, "20m")
            self.assertEqual(events, [{"Action": "pass", "Test": "TestA"}])
            self.assertIn("stray from a child", output.getvalue())

    def test_shard_wall_times_are_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    pass

                def poll(self):
                    return 0

            with (
                patch.object(gate.subprocess, "Popen", Process),
                redirect_stdout(io.StringIO()) as output,
            ):
                local.shards(["TestA", "TestB"], "binary", "label", 4, "20m")
            self.assertRegex(output.getvalue(), r"label shard 1 of 2: \d+s wall")

    def test_chart_check_runs_only_for_a_chart_change_and_names_a_missing_tool(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            local = gate.Gate(root, root, root, True)
            local.files = ["README.md"]
            with patch.object(local, "run") as commands:
                self.assertIn("NOT REQUIRED", local.chart())
            commands.assert_not_called()
            local.files = ["helm/restmail/Chart.yaml"]
            with patch.object(local, "run") as commands:
                self.assertIn("not on this base", local.chart())
            commands.assert_not_called()
            (root / "scripts").mkdir()
            (root / "scripts/check_chart_publish.py").write_text("")
            with (
                patch.object(gate.shutil, "which", return_value=None),
                self.assertRaisesRegex(gate.CouldNotRun, "helm"),
            ):
                local.chart()
            with (
                patch.object(gate.shutil, "which", return_value="/bin/helm"),
                patch.object(local, "run") as commands,
            ):
                local.chart()
            self.assertIn("scripts/check_chart_publish.py", commands.call_args.args[0])

    def test_docker_daemon_down_could_not_run_rather_than_failed(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.packages = ["mod/database"]
            with (
                patch.object(
                    local,
                    "run",
                    side_effect=subprocess.CalledProcessError(1, ["docker", "info"]),
                ),
                self.assertRaisesRegex(gate.CouldNotRun, "Docker"),
            ):
                local.postgres()

    def test_an_image_another_run_is_using_does_not_fail_the_gate(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.image, local.created_image = "postgres:16-alpine", True
            with (
                patch.object(gate.subprocess, "run") as docker,
                redirect_stdout(io.StringIO()) as output,
            ):
                docker.return_value.returncode = 1
                local.cleanup()
            self.assertIn("WARNING", output.getvalue())

    def test_binary_discovery_runs_each_test_once_and_rejects_failed_shards(self):
        names = [f"TestFuture{index}WithVertrag" for index in range(7)]
        for failure in (False, True):
            selected = []
            with (
                self.subTest(failure=failure),
                tempfile.TemporaryDirectory() as temporary,
            ):
                local = gate.Gate(ROOT, Path(temporary), Path(temporary), True)
                local.api_changed = True

                class Process:
                    def __init__(
                        self, args, selected=selected, failure=failure, **kwargs
                    ):
                        picked = args[-1][2:-2].split("|")
                        selected.extend(picked)
                        self.code = int(failure and names[0] in picked)
                        for name in picked:
                            kwargs["stdout"].write(
                                json.dumps({"Action": "pass", "Test": name}) + "\n"
                            )

                    def wait(self):
                        return self.code

                    def poll(self):
                        return self.code

                with (
                    patch.object(
                        local,
                        "run",
                        side_effect=["/bin/vertrag\n", "\n".join(names), ""],
                    ) as commands,
                    patch.object(gate.subprocess, "Popen", Process),
                    redirect_stdout(io.StringIO()),
                ):
                    if failure:
                        with self.assertRaisesRegex(RuntimeError, "shards failed"):
                            local.vertrag()
                    else:
                        local.vertrag()
                self.assertCountEqual(selected, names)
                self.assertEqual(commands.call_count, 3)

    def test_postgres_is_named_labelled_ephemeral_and_removed_with_volumes(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False, "1942")
            local.packages = ["mod/database"]
            import yaml

            local.workflow = yaml.safe_load(
                (ROOT / ".github/workflows/ci.yml").read_text()
            )
            with (
                patch.object(
                    local, "run", side_effect=["", "id", "127.0.0.1:49152\n", ""]
                ) as commands,
                patch.object(gate.subprocess, "run") as docker,
            ):
                docker.return_value.returncode = 0
                local.postgres()
                create = commands.call_args_list[1].args[0]
                self.assertIn("rest-mail-loop=1942", create)
                self.assertIn("127.0.0.1::5432", create)
                self.assertIn("/var/lib/postgresql/data", create)
                ready = docker.call_args.args[0]
                self.assertEqual(ready[ready.index("-h") + 1], "127.0.0.1")
                self.assertTrue(local.container.startswith("restmail-1942-postgres-"))
                self.assertEqual(local.env["DB_PORT"], "49152")
                self.assertIn("rest-mail.gate=" + local.run_id, create)
                self.assertTrue(local.container.endswith("-" + local.run_id))
                local.cleanup()
                # Removed by the id `docker run` printed: its own container alone.
                docker.assert_called_with(["docker", "rm", "-fv", "id"], check=False)
                self.assertFalse(local.created_image)

    def test_two_gates_on_one_machine_never_remove_each_others_database(self):
        # A gate's Postgres must outlive every other gate's cleanup: gates with
        # the same loop label share that label, and all share RESTMAIL_LABEL's
        # default, so a removal by label or name pattern would end them all (#1942).
        workflow = {"jobs": {"test": {"services": {"postgres": {"image": "postgres:16"}}}}}
        docker_ids = {}
        gates = []
        with tempfile.TemporaryDirectory() as temporary:
            for number in (1, 2):
                local = gate.Gate(ROOT, Path(temporary), Path(temporary), False, "1942")
                local.packages = ["mod/database"]
                local.workflow = workflow
                docker_ids[local] = f"{number:064x}"
                with (
                    patch.object(
                        local,
                        "run",
                        side_effect=["", docker_ids[local] + "\n", "127.0.0.1:4915%d\n" % number, ""],
                    ) as commands,
                    patch.object(gate.subprocess, "run") as docker,
                ):
                    docker.return_value.returncode = 0
                    local.postgres()
                gates.append((local, commands.call_args_list[1].args[0]))
            (first, first_create), (second, second_create) = gates
            self.assertNotEqual(first.container, second.container)
            self.assertNotEqual(first.run_id, second.run_id)
            self.assertIn("rest-mail.gate=" + first.run_id, first_create)
            self.assertNotIn("rest-mail.gate=" + first.run_id, second_create)
            for ending, other in ((first, second), (second, first)):
                with patch.object(gate.subprocess, "run") as docker:
                    docker.return_value.returncode = 0
                    ending.cleanup()
                commands = [call.args[0] for call in docker.call_args_list]
                self.assertEqual(commands, [["docker", "rm", "-fv", docker_ids[ending]]])
                for command in commands:
                    joined = " ".join(command)
                    self.assertNotIn(docker_ids[other], joined)
                    self.assertNotIn(other.container, joined)
                    self.assertNotIn("--filter", command)
                    self.assertNotIn("label=", joined)

    def test_a_container_whose_id_never_arrived_is_removed_by_its_unique_name(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False, "1942")
            local.container = f"restmail-1942-postgres-{local.run_id}"
            with patch.object(gate.subprocess, "run") as docker:
                docker.return_value.returncode = 0
                local.cleanup()
            docker.assert_called_once_with(["docker", "rm", "-fv", local.container], check=False)


class ShardPlan(unittest.TestCase):
    """Shards are planned on the times tests took, so none carries far more (#1942)."""

    MEASURED = json.loads(
        (ROOT / "scripts/tests/data/internal_api_race_seconds.json").read_text()
    )["seconds"]

    def test_measured_times_overload_a_round_robin_shard_and_not_a_planned_one(self):
        seconds = self.MEASURED
        names = sorted(seconds)
        mean = sum(seconds.values()) / gate.RACE_SHARDS
        bound = mean + max(seconds.values())
        round_robin = [
            sum(seconds[name] for name in names[shard :: gate.RACE_SHARDS])
            for shard in range(gate.RACE_SHARDS)
        ]
        # What the gate did before: one shard of eight at 250 s against 154 s.
        self.assertGreater(max(round_robin), bound)
        plan = gate.plan_shards(names, seconds, gate.RACE_SHARDS)
        self.assertEqual(len(plan), gate.RACE_SHARDS)
        self.assertCountEqual([name for shard in plan for name in shard], names)
        totals = [sum(seconds[name] for name in shard) for shard in plan]
        self.assertLessEqual(max(totals), bound, totals)

    def test_with_no_times_on_record_tests_are_dealt_out_as_ci_deals_them(self):
        names = [f"Test{index:02d}" for index in range(19)]
        self.assertEqual(
            gate.plan_shards(names, {}, 8), [names[shard::8] for shard in range(8)]
        )
        self.assertEqual(gate.plan_shards(names[:3], {}, 8), [[name] for name in names[:3]])

    def test_a_test_with_no_time_on_record_weighs_the_median(self):
        seconds = {"TestA": 1, "TestB": 2, "TestC": 30, "TestD": 30}
        plan = gate.plan_shards(["TestA", "TestB", "TestC", "TestD", "TestNew"], seconds, 2)
        # TestNew counts as 30 s, so it shares a shard with one 30 s test and the
        # 1 and 2 s tests go with the other: 60 s against 33 s.
        self.assertEqual(sorted(plan), [["TestA", "TestB", "TestD"], ["TestC", "TestNew"]])

    def test_a_shard_run_keeps_passed_times_and_the_next_plan_uses_them(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache, work = Path(temporary) / "cache", Path(temporary) / "work"
            cache.mkdir()
            work.mkdir()
            local = gate.Gate(ROOT, cache, work, False)
            patterns = []
            elapsed = {"TestHeavy": 300.04, "TestA": 1, "TestB": 1, "TestC": 1}

            class Process:
                pid = 1

                def __init__(self, args, **kwargs):
                    patterns.append(args[-1])
                    for name in args[-1][2:-2].split("|"):
                        for event in (
                            {"Action": "pass", "Test": name + "/sub", "Elapsed": 999},
                            {"Action": "pass", "Test": name, "Elapsed": elapsed[name]},
                        ):
                            kwargs["stdout"].write(json.dumps(event) + "\n")

                def poll(self):
                    return 0

            names = ["TestA", "TestB", "TestC", "TestHeavy"]
            with (
                patch.object(gate.subprocess, "Popen", Process),
                redirect_stdout(io.StringIO()),
            ):
                local.shards(names, "binary", "label", 2, "20m")
                self.assertEqual(patterns, ["^(TestA|TestC)$", "^(TestB|TestHeavy)$"])
                kept = json.loads((cache / "go-test-seconds/label.json").read_text())
                self.assertEqual(kept, {"TestA": 1, "TestB": 1, "TestC": 1, "TestHeavy": 300.0})
                patterns.clear()
                # A rerun of what a limit cut off plans on the same times.
                local.shards(names, "binary", "label-again", 2, "20m")
            self.assertEqual(patterns, ["^(TestHeavy)$", "^(TestA|TestB|TestC)$"])

    def test_unreadable_times_plan_as_if_none_were_on_record(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            path = Path(temporary) / "go-test-seconds/label.json"
            path.parent.mkdir()
            for text in ("{not json", "[1, 2]", '{"TestA": "slow", "TestB": -1}'):
                path.write_text(text)
                self.assertEqual(local.test_seconds("label"), {})


class PythonEnvironment(unittest.TestCase):
    def test_pyyaml_that_cannot_be_installed_is_named_and_is_not_a_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            python = cache / "venv/bin/python"
            python.parent.mkdir(parents=True)
            python.write_text("#!/bin/sh\nexit 1\n")
            python.chmod(0o755)
            local = gate.Gate(ROOT, cache, cache, False)
            with (
                redirect_stdout(io.StringIO()),
                self.assertRaisesRegex(gate.CouldNotRun, "PyYAML.*scripts/tests"),
            ):
                local.python_environment()

    def test_a_venv_that_cannot_start_is_named_too(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            python = cache / "venv/bin/python"
            python.parent.mkdir(parents=True)
            python.write_text("not a program")
            python.chmod(0o644)
            local = gate.Gate(ROOT, cache, cache, False)
            with (
                redirect_stdout(io.StringIO()),
                self.assertRaisesRegex(gate.CouldNotRun, "PyYAML"),
            ):
                local.python_environment()

    def test_python_suites_use_the_venv_and_not_the_python_that_started_the_gate(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            with patch.object(local, "run") as commands:
                local.python_suite("scripts/tests")
            self.assertEqual(commands.call_args.args[0][0], local.python)
            self.assertEqual(
                local.env["PATH"].split(os.pathsep)[0], str(local.python.parent)
            )

    def test_the_gate_itself_starts_without_pyyaml(self):
        # chores.yml starts it with the system Python, which may not have it.
        source = (ROOT / "scripts/ci_local.py").read_text()
        for line in source.splitlines():
            self.assertNotRegex(line, r"^(import|from) yaml")


class Wrapper(unittest.TestCase):
    def test_chore_runs_the_gate_through_the_scratch_wrapper(self):
        import yaml

        task = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]["ci:local"]
        self.assertEqual(
            task["cmds"],
            [
                "python3 -B scripts/scratch_run.py python3 -B scripts/ci_local.py {{.CLI_ARGS}}"
            ],
        )
        self.assertIn("RESTMAIL_SCRATCH", task["env"])

    def test_the_second_label_comes_from_the_run(self):
        with tempfile.TemporaryDirectory() as temporary:
            local = gate.Gate(ROOT, Path(temporary), Path(temporary), False, "1942")
            local.packages = ["mod/database"]
            local.workflow = {
                "jobs": {"test": {"services": {"postgres": {"image": "postgres:16"}}}}
            }
            with (
                patch.dict(os.environ, {"RESTMAIL_LABEL": "rest-mail-loop=test"}),
                patch.object(
                    local, "run", side_effect=["", "id", "127.0.0.1:49152\n", ""]
                ) as commands,
                patch.object(gate.subprocess, "run") as docker,
            ):
                docker.return_value.returncode = 0
                local.postgres()
            create = commands.call_args_list[1].args[0]
            self.assertIn("rest-mail-loop=test", create)
            self.assertIn("rest-mail-loop=1942", create)

    def test_each_gate_labels_its_postgres_from_its_own_context(self):
        # Every gate's container carried #1941's label and name, so gates
        # sharing the machine could not be told apart (#1942).
        workflow = {"jobs": {"test": {"services": {"postgres": {"image": "postgres:16"}}}}}
        cases = [
            ({"RESTMAIL_LOOP_LABEL": "1942"}, "/home/me/tmp/rest-mail-loop/other", "1942"),
            ({}, "/home/me/tmp/rest-mail-loop/1942j", "1942j"),
            ({}, "/home/me/tmp/rest-mail-loop/loop", "loop"),
            ({"RESTMAIL_LOOP_LABEL": "pr #1810/merge"}, None, "pr-1810-merge"),
            ({"RESTMAIL_LOOP_LABEL": "///"}, None, "local"),
        ]
        for environment, scratch, expected in cases:
            with self.subTest(environment=environment, scratch=scratch):
                with patch.dict(os.environ, environment):
                    os.environ.pop("RESTMAIL_LABEL", None)
                    if "RESTMAIL_LOOP_LABEL" not in environment:
                        os.environ.pop("RESTMAIL_LOOP_LABEL", None)
                    label = gate.loop_label(scratch)
                    self.assertEqual(label, expected)
                    with tempfile.TemporaryDirectory() as temporary:
                        local = gate.Gate(ROOT, Path(temporary), Path(temporary), False, label)
                        local.packages = ["mod/database"]
                        local.workflow = workflow
                        with (
                            patch.object(
                                local, "run", side_effect=["", "id", "127.0.0.1:49152\n", ""]
                            ) as commands,
                            patch.object(gate.subprocess, "run") as docker,
                        ):
                            docker.return_value.returncode = 0
                            local.postgres()
                    create = commands.call_args_list[1].args[0]
                    self.assertIn("rest-mail-loop=" + expected, create)
                    self.assertNotIn("rest-mail-loop=1941", create)
                    self.assertRegex(
                        local.container, rf"^restmail-{re.escape(expected)}-postgres-[0-9a-f]{{10}}$"
                    )

    def test_the_default_label_is_the_scratch_directorys_name(self):
        with patch.dict(os.environ, {"RESTMAIL_SCRATCH": "/home/me/tmp/rest-mail-loop/1942j"}):
            os.environ.pop("RESTMAIL_LOOP_LABEL", None)
            self.assertEqual(gate.loop_label(), "1942j")
            with tempfile.TemporaryDirectory() as temporary:
                self.assertEqual(gate.Gate(ROOT, Path(temporary), Path(temporary), False).loop, "1942j")

    def test_directories_go_inside_the_wrappers_when_there_is_one(self):
        with patch.dict(os.environ, {"RESTMAIL_SCRATCH_RUN": "/run/own"}):
            self.assertEqual(gate.run_parent(Path("/scratch")), Path("/run/own"))
        environment = {
            key: value
            for key, value in os.environ.items()
            if key != "RESTMAIL_SCRATCH_RUN"
        }
        with patch.dict(os.environ, environment, clear=True):
            self.assertEqual(gate.run_parent(Path("/scratch")), Path("/scratch"))

    def test_the_scratch_directory_defaults_to_the_wrappers_setting(self):
        with patch.dict(os.environ, {"RESTMAIL_SCRATCH": "/chosen"}):
            self.assertEqual(gate.default_scratch(), Path("/chosen"))


# Stands in for the venv: holds when asked, so a test can kill a run part-way.
FAKE_PYTHON = """#!/bin/sh
if [ -n "$HOLD_READY" ]; then
  echo "$$ $RESTMAIL_SCRATCH_RUN" > "$HOLD_READY"
  sleep 120
fi
exit 1
"""


class KilledPartWay(unittest.TestCase):
    """Killing `chore ci:local` part-way leaves nothing of its own behind (#1963)."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.temp = Path(temporary.name).resolve()
        self.base = self.temp / "base"
        self.ready = self.temp / "ready"
        self.repo = self.temp / "repo"
        self.repo.mkdir()
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "test@example.test")
        self.git("config", "user.name", "Local gate test")
        (self.repo / "scripts").mkdir()
        for name in ("ci_local.py", "scratch_run.py"):
            (self.repo / "scripts" / name).write_text(
                (ROOT / "scripts" / name).read_text()
            )
        self.git("add", ".")
        self.git("commit", "-q", "-m", "base")
        self.git("clone", "-q", "--bare", str(self.repo), str(self.temp / "origin.git"))
        self.git("remote", "add", "origin", str(self.temp / "origin.git"))
        self.git("fetch", "-q", "origin")
        self.cache = self.temp / "cache"
        python = self.cache / "venv/bin/python"
        python.parent.mkdir(parents=True)
        python.write_text(FAKE_PYTHON)
        python.chmod(0o755)

    def git(self, *args):
        return subprocess.check_output(
            ["git", "-C", str(self.repo), *args], text=True, stderr=subprocess.DEVNULL
        ).strip()

    def start(self, **environment):
        process = subprocess.Popen(
            [
                sys.executable,
                "-B",
                str(self.repo / "scripts/scratch_run.py"),
                sys.executable,
                "-B",
                str(self.repo / "scripts/ci_local.py"),
                "--merge-ref",
                "HEAD",
                "--cache",
                str(self.cache),
                "--scratch",
                str(self.temp / "unused"),
            ],
            env=dict(os.environ, RESTMAIL_SCRATCH=str(self.base), **environment),
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        self.addCleanup(process.wait)
        self.addCleanup(lambda: process.poll() is None and process.kill())
        return process

    def wait_for(self, condition, seconds=60):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            if condition():
                return True
            time.sleep(0.05)
        return False

    def test_sigkill_leaves_no_directory_and_the_next_run_clears_the_worktree(self):
        process = self.start(HOLD_READY=str(self.ready))
        self.assertTrue(
            self.wait_for(lambda: self.ready.exists() and self.ready.read_text()),
            "the gate never reached its first step",
        )
        pid, directory = self.ready.read_text().split()
        # Both of the gate's directories are inside the wrapper's, so its cleaner takes them.
        kept = sorted(path.name.split("-")[0] for path in Path(directory).iterdir())
        self.assertEqual(kept, ["merge", "run"])
        self.assertEqual(list((self.temp / "unused").iterdir()), [])
        os.kill(process.pid, signal.SIGKILL)
        self.assertTrue(
            self.wait_for(lambda: not list(self.base.iterdir())),
            f"left behind: {list(self.base.iterdir())}",
        )

        def holder_gone():
            try:
                os.kill(int(pid), 0)
            except ProcessLookupError:
                return True
            return False

        self.assertTrue(self.wait_for(holder_gone), "the run outlived its wrapper")
        self.assertIn(directory, self.git("worktree", "list"))

        again = self.start()
        again.wait(timeout=120)
        self.assertEqual(again.returncode, gate.EXIT_FAILED)
        self.assertNotIn(directory, self.git("worktree", "list"))
        self.assertEqual(list(self.base.iterdir()), [])


class Signals(unittest.TestCase):
    def test_termination_signals_unwind_so_that_cleanup_runs(self):
        previous = {
            number: signal.getsignal(number)
            for number in (signal.SIGTERM, signal.SIGHUP)
        }
        self.addCleanup(
            lambda: [signal.signal(number, old) for number, old in previous.items()]
        )
        gate.unwind_on_termination()
        for number in previous:
            with self.subTest(number=number), self.assertRaises(KeyboardInterrupt):
                os.kill(os.getpid(), number)
                signal.pause()


class GitTrees(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / "repo"
        self.root.mkdir()
        self.git("init", "-b", "main")
        self.git("config", "user.email", "test@example.test")
        self.git("config", "user.name", "Local gate test")
        (self.root / "base.txt").write_text("base\n")
        self.git("add", ".")
        self.git("commit", "-m", "base")
        self.base = self.git("rev-parse", "HEAD")
        self.git("update-ref", "refs/remotes/origin/main", self.base)

    def git(self, *args):
        return subprocess.check_output(
            ["git", "-C", str(self.root), *args], text=True, stderr=subprocess.DEVNULL
        ).strip()

    def test_stale_base_fails_unless_explicitly_overridden(self):
        (self.root / "base.txt").write_text("new main\n")
        self.git("commit", "-am", "advance")
        self.git(
            "update-ref", "refs/remotes/origin/main", self.git("rev-parse", "HEAD")
        )
        self.git("checkout", "--detach", self.base)
        with self.assertRaisesRegex(RuntimeError, "STALE BASE"):
            gate.check_base(self.root, False)
        with redirect_stdout(io.StringIO()) as output:
            gate.check_base(self.root, True)
        self.assertIn("WARNING", output.getvalue())

    def test_tracked_staged_unstaged_and_untracked_changes_are_seen(self):
        (self.root / "base.txt").write_text("committed\n")
        self.git("commit", "-am", "branch")
        (self.root / "staged.txt").write_text("staged")
        self.git("add", "staged.txt")
        (self.root / "base.txt").write_text("unstaged\n")
        (self.root / "untracked.txt").write_text("new")
        self.assertEqual(
            gate.changed_files(self.root), ["base.txt", "staged.txt", "untracked.txt"]
        )

    def test_main_moving_during_a_run_does_not_count_as_this_heads_change(self):
        # Worktrees share origin/main, so another worker's fetch moves it mid-run.
        self.git("checkout", "-q", "-b", "topic")
        (self.root / "mine.txt").write_text("mine")
        self.git("add", ".")
        self.git("commit", "-m", "mine")
        self.git("checkout", "-q", "--detach", self.base)
        (self.root / "theirs.txt").write_text("theirs")
        self.git("add", ".")
        self.git("commit", "-m", "theirs")
        self.git(
            "update-ref", "refs/remotes/origin/main", self.git("rev-parse", "HEAD")
        )
        self.git("checkout", "-q", "topic")
        self.assertEqual(gate.changed_files(self.root), ["mine.txt"])

    def test_moved_file_selects_the_package_it_left(self):
        (self.root / "old").mkdir()
        (self.root / "old/file.go").write_text("package old\n" * 20)
        self.git("add", ".")
        self.git("commit", "-m", "file")
        self.git(
            "update-ref", "refs/remotes/origin/main", self.git("rev-parse", "HEAD")
        )
        self.git("mv", "old", "new")
        self.git("commit", "-m", "move")
        self.assertEqual(gate.changed_files(self.root), ["new/file.go", "old/file.go"])

    def test_merge_worktree_preserves_dirty_checkout_and_cleans_on_failure(self):
        self.git("checkout", "-b", "topic")
        (self.root / "head.txt").write_text("head")
        self.git("add", ".")
        self.git("commit", "-m", "head")
        head = self.git("rev-parse", "HEAD")
        (self.root / "base.txt").write_text("do not touch\n")
        before = self.git("status", "--porcelain")
        scratch = Path(self.temporary.name) / "scratch"
        scratch.mkdir()
        with (
            self.assertRaisesRegex(RuntimeError, "intentional"),
            gate.merged_worktree(self.root, head, scratch) as work,
        ):
            self.assertEqual((work / "head.txt").read_text(), "head")
            self.assertEqual((work / "base.txt").read_text(), "base\n")
            raise RuntimeError("intentional")
        self.assertEqual(self.git("status", "--porcelain"), before)
        self.assertEqual((self.root / "base.txt").read_text(), "do not touch\n")
        self.assertEqual(list(scratch.iterdir()), [])


class QueueReading(unittest.TestCase):
    def reply(self, entries):
        return {
            "data": {
                "repository": {"mergeQueue": {"entries": {"nodes": entries}}},
            }
        }

    def node(self, position, number):
        return {
            "position": position,
            "pullRequest": {"number": number, "headRefOid": f"sha{number}"},
        }

    def test_entries_come_in_queue_order_whatever_order_they_arrive(self):
        entries = gate.queue_entries(
            self.reply([self.node(2, 200), self.node(3, 30), self.node(1, 900)])
        )
        self.assertEqual([entry.label for entry in entries], ["#900", "#200", "#30"])
        self.assertEqual(entries[0].commit, "sha900")

    def test_a_repository_with_no_merge_queue_could_not_run(self):
        with self.assertRaises(gate.CouldNotRun):
            gate.queue_entries({"data": {"repository": {"mergeQueue": None}}})

    def test_an_unreadable_queue_could_not_run_and_is_not_an_empty_one(self):
        failed = subprocess.CalledProcessError(1, ["gh"], stderr="EPROTO")
        with (
            patch.object(gate.subprocess, "run", side_effect=failed),
            self.assertRaisesRegex(gate.CouldNotRun, "merge queue.*EPROTO"),
        ):
            gate.read_merge_queue(ROOT)
        with (
            patch.object(gate.subprocess, "run", side_effect=FileNotFoundError("gh")),
            self.assertRaisesRegex(gate.CouldNotRun, "gh"),
        ):
            gate.read_merge_queue(ROOT)

    def test_a_pr_in_the_queue_goes_over_only_what_is_ahead_of_it(self):
        entries = [gate.Entry(f"#{n}", f"sha{n}", n) for n in (5, 6, 7)]
        self.assertEqual(
            [entry.number for entry in gate.entries_ahead(entries, 6)], [5]
        )
        self.assertEqual(
            [entry.number for entry in gate.entries_ahead(entries, 9)], [5, 6, 7]
        )


class UnittestOutput(unittest.TestCase):
    RULE = "=" * 70

    def test_failures_and_errors_are_named_by_the_id_that_repeats_them(self):
        output = f"""noise ERROR: fake (not.this)
ERROR: fake (not.this.either)
{self.RULE}
FAIL: test_a (test_one.Things.test_a)
----
{self.RULE}
FAIL: test_a (test_one.Things.test_a) (job='lint')
{self.RULE}
ERROR: test_two (unittest.loader._FailedTest.test_two)
{self.RULE}
ERROR: setUpClass (test_three.Slow)
FAILED (failures=1)
"""
        self.assertEqual(
            gate.unittest_failures(output),
            ["test_one.Things.test_a", "test_two", "test_three.Slow"],
        )

    def test_a_test_is_found_only_in_a_tree_that_has_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / "test_one.py").write_text(
                "class Things:\n    def test_a(self):\n        pass\n"
            )
            self.assertTrue(gate.test_exists(directory, "test_one.Things.test_a"))
            self.assertTrue(gate.test_exists(directory, "test_one"))
            self.assertFalse(gate.test_exists(directory, "test_one.Things.test_b"))
            self.assertFalse(gate.test_exists(directory, "test_two.Things.test_a"))

    def test_a_reported_import_error_covers_the_tests_in_that_module(self):
        self.assertEqual(
            gate.still_failing(
                ["test_one.Things.test_a", "test_two.Other.test_b"], ["test_one"]
            ),
            ["test_one.Things.test_a"],
        )


# A go that only knows what the gate asks of it; `vet` follows a file in the tree.
FAKE_GO = """#!/bin/sh
case "$1" in
  env) echo go1.26.10 ;;
  vet) if [ -f vet-breaks ]; then echo "vet-breaks:1: does not compile"; exit 1; fi ;;
esac
"""
WORKFLOW = """
jobs:
  lint:
    steps:
      - name: Set up Go
        with: {go-version: "1.26.10"}
"""
# A test the first entry brings, which the second entry's file breaks, as with #1900 and #1880.
LATER_TEST = """
import unittest
from pathlib import Path

class Allowed(unittest.TestCase):
    def test_only_what_is_allowed(self):
        path = Path(__file__).resolve().parents[2] / "workflow.txt"
        self.assertFalse(path.exists(), "not allowed")
"""
PASSING_TEST = """
import unittest

class Fine(unittest.TestCase):
    def test_fine(self):
        pass
"""


class Stacks(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.temp = Path(temporary.name).resolve()
        self.repo = self.temp / "repo"
        self.repo.mkdir()
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "test@example.test")
        self.git("config", "user.name", "Local gate test")
        self.write(".github/workflows/ci.yml", WORKFLOW)
        self.write("scripts/tests/test_fine.py", PASSING_TEST)
        self.write("tests/contract/test_fine_too.py", PASSING_TEST)
        self.write("shared.txt", "base\n")
        self.git("add", ".")
        self.git("commit", "-q", "-m", "base")
        self.main_commit = self.git("rev-parse", "HEAD")
        self.git("clone", "-q", "--bare", str(self.repo), str(self.temp / "origin.git"))
        self.git("remote", "add", "origin", str(self.temp / "origin.git"))
        self.git("fetch", "-q", "origin")

        self.cache = self.temp / "cache"
        python = self.cache / "venv/bin/python"
        python.parent.mkdir(parents=True)
        # This suite's own Python has PyYAML: the coverage tests above import it.
        python.write_text(f'#!/bin/sh\nexec "{sys.executable}" "$@"\n')
        python.chmod(0o755)
        tools = self.temp / "tools"
        tools.mkdir()
        (tools / "go").write_text(FAKE_GO)
        (tools / "go").chmod(0o755)
        path = patch.dict(
            os.environ, {"PATH": str(tools) + os.pathsep + os.environ["PATH"]}
        )
        path.start()
        self.addCleanup(path.stop)
        root = patch.object(gate, "ROOT", self.repo)
        root.start()
        self.addCleanup(root.stop)
        # main replaces these; the test must not change how its own process ends.
        for number in (signal.SIGTERM, signal.SIGHUP):
            self.addCleanup(signal.signal, number, signal.getsignal(number))

    def git(self, *args):
        return subprocess.check_output(
            ["git", "-C", str(self.repo), *args], text=True, stderr=subprocess.DEVNULL
        ).strip()

    def write(self, name, text):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def entry(self, name, files):
        """A commit on its own branch from main, as a queue entry would be."""
        self.git("checkout", "-q", "-b", name, self.main_commit)
        for path, text in files.items():
            self.write(path, text)
        self.git("add", ".")
        self.git("commit", "-q", "-m", name)
        commit = self.git("rev-parse", "HEAD")
        self.git("checkout", "-q", "main")
        return commit

    def queue(self, *commits):
        entries = [
            gate.Entry(f"#{index}", commit, index)
            for index, commit in enumerate(commits, 1)
        ]
        return patch.object(gate, "read_merge_queue", return_value=entries)

    def run_main(self, *arguments):
        with redirect_stdout(io.StringIO()) as output:
            code = gate.main(
                [
                    *arguments,
                    "--cache",
                    str(self.cache),
                    "--scratch",
                    str(self.temp / "scratch"),
                ]
            )
        return code, output.getvalue()

    def test_a_clean_stack_passes(self):
        first = self.entry("first", {"one.txt": "1\n"})
        second = self.entry("second", {"two.txt": "2\n"})
        with self.queue(first, second):
            code, output = self.run_main("--stack")
        self.assertEqual(code, 0, output)
        self.assertEqual(self.git("worktree", "list").count("\n"), 0)
        self.assertEqual(list((self.temp / "scratch").iterdir()), [])

    def test_a_failure_only_with_an_entry_ahead_names_the_entry(self):
        ahead = self.entry("ahead", {"scripts/tests/test_allowed.py": LATER_TEST})
        behind = self.entry("behind", {"workflow.txt": "new\n"})
        with self.queue(ahead, behind):
            code, output = self.run_main("--stack")
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertIn(
            "test_allowed.Allowed.test_only_what_is_allowed: first fails with #2",
            output,
        )
        self.assertIn("passes on main + #1", output)
        self.assertIn("does not exist with #2 alone over main", output)

    def test_stack_order_decides_which_entry_is_named(self):
        ahead = self.entry("ahead", {"workflow.txt": "new\n"})
        behind = self.entry("behind", {"scripts/tests/test_allowed.py": LATER_TEST})
        with self.queue(ahead, behind):
            code, output = self.run_main("--stack")
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertIn("first fails with #2", output)
        self.assertIn("passes with #2 alone over main", output)

    def test_a_failure_on_main_blames_no_entry(self):
        self.write("scripts/tests/test_broken.py", LATER_TEST)
        self.write("workflow.txt", "new\n")
        self.git("add", ".")
        self.git("commit", "-q", "-m", "broken")
        self.main_commit = self.git("rev-parse", "HEAD")
        self.git("push", "-q", "origin", "main")
        entry = self.entry("clean", {"one.txt": "1\n"})
        with self.queue(entry):
            code, output = self.run_main("--stack")
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertIn("fails on main itself", output)

    def test_go_vet_is_repeated_on_each_prefix_too(self):
        first = self.entry("first", {"one.txt": "1\n"})
        second = self.entry("second", {"vet-breaks": "\n"})
        with self.queue(first, second):
            code, output = self.run_main("--stack")
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertIn("go vet ./...: first fails with #2", output)
        self.assertIn("does not compile", output)

    def test_every_suite_runs_even_after_the_first_fails(self):
        vet = self.entry("vet", {"vet-breaks": "\n"})
        suite = self.entry("suite", {"tests/contract/test_bad.py": LATER_TEST})
        trips = self.entry("trips", {"workflow.txt": "new\n"})
        with self.queue(vet, suite, trips):
            code, output = self.run_main("--stack")
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertIn("go vet ./...: first fails with #1", output)
        self.assertIn("first fails with #3", output)

    def test_a_conflict_names_the_entry_and_the_rest_are_still_checked(self):
        first = self.entry("first", {"shared.txt": "one\n"})
        second = self.entry("second", {"shared.txt": "two\n"})
        third = self.entry("third", {"scripts/tests/test_allowed.py": LATER_TEST})
        fourth = self.entry("fourth", {"workflow.txt": "new\n"})
        with self.queue(first, second, third, fourth):
            code, output = self.run_main("--stack")
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertIn("#2 conflicts with main + #1: shared.txt", output)
        self.assertIn("first fails with #4", output)

    def test_a_failure_that_does_not_repeat_says_so_and_stays_failed(self):
        flaky = """
import os
import unittest
from pathlib import Path

class Once(unittest.TestCase):
    def test_once(self):
        marker = Path(os.environ["ONCE_MARKER"])
        first = not marker.exists()
        marker.write_text("seen")
        self.assertFalse(first)
"""
        entry = self.entry("flaky", {"scripts/tests/test_once.py": flaky})
        with (
            self.queue(entry),
            patch.dict(os.environ, {"ONCE_MARKER": str(self.temp / "marker")}),
        ):
            code, output = self.run_main("--stack")
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertIn("test_once.Once.test_once: did not repeat", output)

    def test_an_empty_queue_is_not_required_and_says_so(self):
        with self.queue():
            code, output = self.run_main("--stack")
        self.assertEqual(code, 0, output)
        self.assertIn("the merge queue is empty", output)

    def test_named_entries_replace_the_queue_for_a_replay(self):
        ahead = self.entry("ahead", {"scripts/tests/test_allowed.py": LATER_TEST})
        behind = self.entry("behind", {"workflow.txt": "new\n"})
        with patch.object(
            gate, "read_merge_queue", side_effect=AssertionError("queue was read")
        ):
            code, output = self.run_main(
                "--stack",
                "--base",
                self.main_commit,
                "--entry",
                ahead,
                "--entry",
                behind,
            )
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertIn(f"first fails with {behind}", output)

    def trees_merged(self, *arguments):
        """What each run sees: the tree --merge-ref and --merge-pr actually test."""
        seen = []

        def steps(local):
            seen.append(
                sorted(
                    path.name for path in local.root.iterdir() if path.name != ".git"
                )
            )
            return []

        with patch.object(gate.Gate, "steps", steps):
            code, output = self.run_main(*arguments)
        return code, output, seen

    def test_merge_pr_tests_the_pr_over_main_and_every_entry_in_order(self):
        first = self.entry("first", {"one.txt": "1\n"})
        second = self.entry("second", {"two.txt": "2\n"})
        pr = self.entry("pr", {"three.txt": "3\n"})
        self.git("push", "-q", "origin", f"{pr}:refs/pull/77/head")
        with self.queue(first, second):
            code, output, seen = self.trees_merged("--merge-pr", "77")
        self.assertEqual(code, 0, output)
        for name in ("one.txt", "two.txt", "three.txt", "shared.txt"):
            self.assertIn(name, seen[0])

    def test_merge_pr_for_a_queued_pr_leaves_out_the_entries_behind_it(self):
        pr = self.entry("pr", {"one.txt": "1\n"})
        behind = self.entry("behind", {"two.txt": "2\n"})
        self.git("push", "-q", "origin", f"{pr}:refs/pull/1/head")
        with self.queue(pr, behind):
            code, output, seen = self.trees_merged("--merge-pr", "1")
        self.assertEqual(code, 0, output)
        self.assertIn("one.txt", seen[0])
        self.assertNotIn("two.txt", seen[0])

    def test_merge_pr_names_the_entry_it_conflicts_with(self):
        # #1658 conflicted only with #1900 ahead, while GitHub showed it clean.
        first = self.entry("first", {"one.txt": "1\n"})
        second = self.entry("second", {"shared.txt": "two\n"})
        pr = self.entry("pr", {"shared.txt": "mine\n"})
        self.git("push", "-q", "origin", f"{pr}:refs/pull/77/head")
        with self.queue(first, second):
            code, output, seen = self.trees_merged("--merge-pr", "77")
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertEqual(seen, [])
        self.assertIn("conflicts with #2: shared.txt", output)

    def test_merge_pr_warns_when_an_entry_ahead_could_not_be_stacked(self):
        first = self.entry("first", {"shared.txt": "one\n"})
        second = self.entry("second", {"shared.txt": "two\n"})
        pr = self.entry("pr", {"three.txt": "3\n"})
        self.git("push", "-q", "origin", f"{pr}:refs/pull/77/head")
        with self.queue(first, second):
            code, output, seen = self.trees_merged("--merge-pr", "77")
        self.assertEqual(code, gate.EXIT_WARNED, output)
        self.assertIn("#2", output)
        self.assertIn("three.txt", seen[0])

    def test_merge_ref_stays_over_main_alone(self):
        queued = self.entry("queued", {"one.txt": "1\n"})
        head = self.entry("head", {"two.txt": "2\n"})
        with self.queue(queued):
            code, output, seen = self.trees_merged("--merge-ref", head)
        self.assertEqual(code, 0, output)
        self.assertIn("two.txt", seen[0])
        self.assertNotIn("one.txt", seen[0])

    def test_a_warned_inner_run_exits_warned(self):
        head = self.entry("head", {"two.txt": "2\n"})

        def steps(local):
            return [("Go tests", lambda: "WARNING: a shard hit its limit")]

        with patch.object(gate.Gate, "steps", steps):
            code, output = self.run_main("--merge-ref", head)
        self.assertEqual(code, gate.EXIT_WARNED, output)
        self.assertIn(gate.VERDICTS[gate.EXIT_WARNED], output)

    def test_a_timed_out_inner_run_exits_no_verdict(self):
        head = self.entry("head", {"two.txt": "2\n"})

        def limited():
            raise gate.NoVerdict("reached its limit")

        def steps(local):
            return [("Go tests", limited)]

        with patch.object(gate.Gate, "steps", steps):
            code, output = self.run_main("--merge-ref", head)
        self.assertEqual(code, gate.EXIT_NO_VERDICT, output)
        self.assertIn(gate.VERDICTS[gate.EXIT_NO_VERDICT], output)

    def test_a_main_that_cannot_be_fetched_could_not_run_and_is_not_a_failure(self):
        self.git("remote", "set-url", "origin", str(self.temp / "missing.git"))
        with self.queue():
            code, output = self.run_main("--stack")
        self.assertEqual(code, gate.EXIT_FAILED, output)
        self.assertIn("COULD-NOT-RUN: Newest main / ancestry", output)
        self.assertNotIn("FAIL: Newest main", output)

    def test_base_and_entry_need_a_mode_that_merges(self):
        for arguments in (["--entry", "abc"], ["--base", "abc"]):
            with (
                self.subTest(arguments=arguments),
                redirect_stdout(io.StringIO()),
                patch("sys.stderr", io.StringIO()),
                self.assertRaises(SystemExit),
            ):
                self.run_main(*arguments)


if __name__ == "__main__":
    unittest.main()
