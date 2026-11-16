"""Every example of an operation the live run never sends is proven in process
(#1334), as every example that needs setup is (#1237)."""

import importlib.util
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("example_proofs", ROOT / "scripts/example_proofs.py")
example_proofs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(example_proofs)


class ExampleProofs(unittest.TestCase):
    def run_proof_check(self, examples, *proofs):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            examples_path = root / "examples.json"
            examples_path.write_text(json.dumps(examples))
            proof_paths = []
            for index, proof in enumerate(proofs):
                path = root / f"proofs-{index}.json"
                path.write_text(json.dumps(proof))
                proof_paths.append(str(path))
            return subprocess.run(
                [sys.executable, str(ROOT / "scripts/example_proofs.py"),
                 str(examples_path), *proof_paths],
                capture_output=True, text=True, check=False,
            )

    def test_cli_rejects_every_unproven_requirement_family(self):
        for requires in (
            {"principal": "anonymous"},
            {"body": "over_cap"},
            {"limit": "rate"},
            {"fault": "database"},
            {"state": "two_factor_session"},
            {"state": "two_factor_session", "fault": "database"},
        ):
            with self.subTest(requires=requires):
                result = self.run_proof_check({"operation": {"refused": {"requires": requires}}}, {})
                needs = ", ".join(f"{kind} {value}" for kind, value in sorted(requires.items()))
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(
                    result.stdout,
                    f"operation/refused requires {needs}, and no in-process test proved it\n"
                    "0 of 1 examples that need setup are proven in process\n",
                )

    def test_cli_requires_the_exact_operation_and_pair(self):
        examples = {"operation": {
            "first": {"requires": {"fault": "database"}},
            "second": {"requires": {"body": "over_cap"}},
        }}
        result = self.run_proof_check(
            examples, {"operation": ["first", "another"], "another_operation": ["second"]},
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(
            result.stdout,
            "operation/second requires body over_cap, and no in-process test proved it\n"
            "1 of 2 examples that need setup are proven in process\n",
        )

    def test_cli_unions_runner_proofs_without_double_counting(self):
        examples = {"operation": {
            "first": {"requires": {"fault": "database"}},
            "second": {"requires": {"body": "over_cap"}},
            "live": {},
        }}
        result = self.run_proof_check(
            examples, {"operation": ["first"]}, {"operation": ["first", "second"]},
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "2 of 2 examples that need setup are proven in process\n")

    def test_the_live_run_leaves_nothing_out(self):
        # exclude-operation-id is empty (#1334): resetTestData and
        # restoreTestSnapshot are sent by the own-data run like any other.
        self.assertEqual(example_proofs.never_sent(ROOT), frozenset())

    def test_an_unsent_operations_examples_need_proofs(self):
        root = Path(tempfile.mkdtemp())
        (root / "vertrag.yml").write_text("a: 1\nexclude-operation-id:\n  - neverSent\n\nb: 2\n")
        unsent = example_proofs.never_sent(root)
        self.assertEqual(unsent, frozenset({"neverSent"}))
        (root / "vertrag.yml").write_text("exclude-operation-id: [neverSent]  # inline\nb: 2\n")
        self.assertEqual(example_proofs.never_sent(root), frozenset({"neverSent"}))
        (root / "vertrag.yml").write_text("exclude-operation-id: []\nb:\n  - notAnExclusion\n")
        self.assertEqual(example_proofs.never_sent(root), frozenset())
        examples = {"neverSent": {"found": {}, "gone": {}}, "live": {"found": {}}}
        self.assertEqual(example_proofs.unproven(examples, {"neverSent": ["found"]}, unsent),
                         [("neverSent", "gone", {"sent": "never"})])


ANONYMOUS = {"principal": "anonymous"}
LIVE_PAIR = {"requires": ANONYMOUS, "response": {"status": 401}}
LIVE_EXAMPLES = {"getThing": {"refused": LIVE_PAIR}}
LIVE_VERDICT = {
    "summary": {"passed": True},
    "results": [{"status": "pass", "expected": "401", "actual": "401"}],
}


def run_proofs(root, examples, proofs):
    examples_path = root / "plain-examples.json"
    examples_path.write_text(json.dumps(examples))
    proofs_path = root / "plain-proofs.json"
    proofs_path.write_text(json.dumps(proofs))
    return subprocess.run(
        [sys.executable, str(ROOT / "scripts/example_proofs.py"), str(examples_path), str(proofs_path)],
        capture_output=True, text=True, check=False,
    )


class LiveProofs(unittest.TestCase):
    """The union counts a live proof only when the runner really proved it (#1237)."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.live = self.root / "live"
        self.run_dir = self.live / "anonymous"
        self.run_dir.mkdir(parents=True)
        self.write("proofs", {"getThing": ["refused"]})
        self.write("unproven", [])
        self.write("inventory", {"requires": ANONYMOUS, "proven": 1, "unproven": 0})
        self.write("getThing-refused", LIVE_VERDICT)

    def write(self, name, value):
        (self.run_dir / f"anonymous-{name}.json").write_text(json.dumps(value))

    def judge(self, examples=None, in_process=None):
        examples_path = self.root / "examples.json"
        examples_path.write_text(json.dumps(LIVE_EXAMPLES if examples is None else examples))
        proofs_path = self.root / "in-process.json"
        proofs_path.write_text(json.dumps(in_process or {}))
        return subprocess.run(
            [sys.executable, str(ROOT / "scripts/example_proofs.py"), "--live", str(self.live),
             str(examples_path), str(proofs_path)],
            capture_output=True, text=True, check=False,
        )

    def assert_refused(self, reason):
        result = self.judge()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn(reason, result.stdout)
        self.assertIn(
            "getThing/refused requires principal anonymous, and no runner proved it", result.stdout
        )
        self.assertIn("0 of 1 examples that need setup are proven, 0 of them only live", result.stdout)

    def test_a_live_proof_counts_as_proven(self):
        result = self.judge()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("1 of 1 examples that need setup are proven, 1 of them only live", result.stdout)

    def test_the_same_pair_without_live_results_is_unproven(self):
        result = run_proofs(self.root, LIVE_EXAMPLES, {})
        self.assertEqual(result.returncode, 1)
        self.assertIn("no in-process test proved it", result.stdout)

    def test_a_failed_exchange_does_not_count(self):
        self.write("getThing-refused", {
            "summary": {"passed": False},
            "results": [{"status": "fail", "expected": "401", "actual": "500"}],
        })
        self.assert_refused("getThing/refused has no passing verdict")

    def test_a_skipped_exchange_does_not_count(self):
        self.write("getThing-refused", {
            "summary": {"passed": True},
            "results": [{"status": "skip", "expected": "401", "actual": ""}],
        })
        self.assert_refused("getThing/refused has no passing verdict")

    def test_a_tool_error_with_no_verdict_does_not_count(self):
        (self.run_dir / "anonymous-getThing-refused.json").unlink()
        self.assert_refused("getThing/refused has no verdict")

    def test_an_empty_verdict_does_not_count(self):
        self.write("getThing-refused", {})
        self.assert_refused("getThing/refused has no passing verdict")

    def test_a_missing_result_does_not_count(self):
        (self.run_dir / "anonymous-proofs.json").unlink()
        self.assert_refused("live runner anonymous/anonymous: no readable result")

    def test_a_missing_runner_directory_does_not_count(self):
        shutil.rmtree(self.run_dir)
        self.assert_refused("live runner anonymous/anonymous: no readable result")

    def test_an_empty_result_does_not_count(self):
        self.write("proofs", {})
        self.assert_refused("live runner anonymous/anonymous: proved nothing")

    def test_a_malformed_result_does_not_count(self):
        (self.run_dir / "anonymous-proofs.json").write_text("{not json")
        self.assert_refused("no readable result")
        self.write("proofs", {"getThing": "refused"})
        self.assert_refused("its proof list is malformed")

    def test_a_run_that_did_not_finish_does_not_count(self):
        (self.run_dir / "anonymous-inventory.json").unlink()
        self.assert_refused("did not finish")

    def test_an_inventory_that_disagrees_does_not_count(self):
        self.write("inventory", {"requires": ANONYMOUS, "proven": 2, "unproven": 0})
        self.assert_refused("its inventory disagrees with its proof list")

    def test_a_pair_also_listed_as_unproven_does_not_count(self):
        self.write("unproven", [{"operation": "getThing", "example": "refused"}])
        self.assert_refused("getThing/refused is also listed as unproven")

    def test_a_run_that_provided_something_else_does_not_count(self):
        self.write("inventory", {"requires": {"body": "over_cap"}, "proven": 1, "unproven": 0})
        self.assert_refused("needs something other than what this run provided")

    def test_a_failed_exchange_elsewhere_keeps_the_runners_real_proofs(self):
        examples = {"getThing": {
            "refused": LIVE_PAIR,
            "other": {"requires": ANONYMOUS, "response": {"status": 401}},
        }}
        self.write("unproven", [{"operation": "getThing", "example": "other"}])
        result = self.judge(examples)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("getThing/other requires principal anonymous, and no runner proved it", result.stdout)
        self.assertIn("1 of 2 examples that need setup are proven, 1 of them only live", result.stdout)

    def test_an_in_process_proof_stands_when_the_live_runner_fails(self):
        (self.run_dir / "anonymous-proofs.json").unlink()
        result = self.judge(in_process={"getThing": ["refused"]})
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("no readable result", result.stdout)
        self.assertIn("1 of 1 examples that need setup are proven, 0 of them only live", result.stdout)

    def test_a_principal_cell_counts_only_the_pair_it_sends(self):
        cell = self.live / "principals"
        cell.mkdir()
        requires = {"principal": "without_capability"}
        examples = {"getDomain": {"outside_domain": {"requires": requires, "response": {"status": 403}}},
                    "getThing": {"forged": {"requires": requires, "response": {"status": 403}}}}
        verdict = {"summary": {"passed": True},
                   "results": [{"status": "pass", "expected": "403", "actual": "403"}]}
        for name, value in (
            ("proofs", {"getDomain": ["outside_domain"], "getThing": ["forged"]}),
            ("unproven", []),
            ("inventory", {"requires": requires, "proven": 2, "unproven": 0}),
            ("getDomain-outside_domain", verdict),
            ("getThing-forged", verdict),
        ):
            (cell / f"ordinary-{name}.json").write_text(json.dumps(value))
        (self.run_dir / "anonymous-proofs.json").unlink()
        result = self.judge(examples)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("getThing/forged is not what this run sends", result.stdout)
        self.assertIn("1 of 2 examples that need setup are proven, 1 of them only live", result.stdout)

    def test_the_cells_are_those_the_principal_runner_sends(self):
        source = (ROOT / "tests/contract/live_principals.py").read_text()
        for label, (operation, name) in example_proofs.PRINCIPAL_CELLS.items():
            with self.subTest(label=label):
                self.assertRegex(source, rf'"{label}",\s+"{operation}",\s+"{name}"')


class DeferLive(unittest.TestCase):
    def judge(self, examples):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            examples_path = root / "examples.json"
            examples_path.write_text(json.dumps(examples))
            proofs_path = root / "proofs.json"
            proofs_path.write_text("{}")
            return subprocess.run(
                [sys.executable, str(ROOT / "scripts/example_proofs.py"), "--defer-live",
                 str(examples_path), str(proofs_path)],
                capture_output=True, text=True, check=False,
            )

    def test_only_what_a_live_runner_selects_is_left_to_it(self):
        for requires in (ANONYMOUS, {"body": "over_cap"}):
            with self.subTest(requires=requires):
                result = self.judge({"op": {"pair": {"requires": requires}}})
                self.assertEqual(result.returncode, 0, result.stdout)
                self.assertIn("op/pair: no in-process test proved it; left to the live proofs", result.stdout)
                self.assertIn("0 of 1 examples that need setup are proven in process", result.stdout)
        cell = {"getDomain": {"outside_domain": {"requires": {"principal": "without_capability"}}}}
        self.assertEqual(self.judge(cell).returncode, 0)

    def test_every_other_pair_still_fails_here(self):
        for requires in (
            {"fault": "database"},
            {"limit": "rate"},
            {"principal": "without_capability"},
            {"principal": "anonymous", "fault": "database"},
        ):
            with self.subTest(requires=requires):
                result = self.judge({"op": {"pair": {"requires": requires}}})
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertIn("and no in-process test proved it", result.stdout)


class CIProofFetch(unittest.TestCase):
    """scripts/ci_example_proofs.sh, against a stand-in for gh that applies the real filters."""

    RUNS = [{"databaseId": 7, "displayTitle": "fix: a thing"}]

    @staticmethod
    def view(report=None, run="in_progress"):
        """What gh reports for a CI run; report is "status conclusion", None when it has no such job."""
        jobs = [{"name": "Test", "status": "completed", "conclusion": "success"}]
        if report:
            status, conclusion = report.split()
            jobs.append({"name": "Test report", "status": status, "conclusion": conclusion})
        return {"status": run, "jobs": jobs}

    def fetch(self, *views, runs=RUNS, early_runs=None, wait="0"):
        """Runs the script; each read of the CI run takes the next view, then the last repeats.

        The first listing of runs answers early_runs, when given. A script that keeps polling
        past what it should hits the timeout, which fails the test.
        """
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "runs.json").write_text(json.dumps(runs))
            if early_runs is not None:
                (root / "early.json").write_text(json.dumps(early_runs))
            for index, view in enumerate(views):
                (root / f"view{index}.json").write_text(json.dumps(view))
            gh = root / "gh"
            gh.write_text(
                "#!/bin/bash\n"
                'echo "$*" >> "$GH_ROOT/calls"\n'
                'filter=""; args=("$@")\n'
                'for i in "${!args[@]}"; do [ "${args[$i]}" = --jq ] && filter="${args[$((i + 1))]}"; done\n'
                'case "$1 $2" in\n'
                "'run list')\n"
                '  if [ -f "$GH_ROOT/early.json" ]; then jq -r "$filter" "$GH_ROOT/early.json"; rm "$GH_ROOT/early.json"\n'
                '  else jq -r "$filter" "$GH_ROOT/runs.json"; fi ;;\n'
                "'run view')\n"
                '  read -r n < "$GH_ROOT/n" 2>/dev/null || n=0\n'
                '  [ -f "$GH_ROOT/view$n.json" ] || n=$((n - 1))\n'
                '  echo $((n + 1)) > "$GH_ROOT/n"\n'
                '  jq -r "$filter" "$GH_ROOT/view$n.json" ;;\n'
                "esac\n"
            )
            gh.chmod(0o755)
            calls = root / "calls"
            result = subprocess.run(
                ["bash", str(ROOT / "scripts/ci_example_proofs.sh"), "abc", "merge_group", str(root / "out")],
                capture_output=True, text=True, check=False, timeout=600,
                env={**os.environ, "PATH": f"{root}:{os.environ['PATH']}", "GH_ROOT": str(root),
                     "CI_PROOFS_WAIT_SECONDS": wait, "CI_PROOFS_POLL_SECONDS": "0"},
            )
            return result, calls.read_text() if calls.exists() else ""

    def assert_refused(self, fetched, *named):
        result, calls = fetched
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("::error::", result.stderr)
        for words in named:
            self.assertIn(words, result.stderr)
        self.assertNotIn("run download", calls)

    def test_downloads_once_the_test_report_passed(self):
        result, calls = self.fetch(self.view("completed success"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("run list --workflow ci.yml --commit abc --event merge_group", calls)
        self.assertIn("run download 7 --pattern example-proofs-*", calls)

    def test_a_report_that_finishes_later_is_waited_for(self):
        result, calls = self.fetch(
            self.view(),
            self.view("queued null"),
            self.view("in_progress null"),
            self.view("completed success"),
            wait="60",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls.count("run view"), 4)
        self.assertIn("run download 7", calls)

    def test_a_run_that_appears_later_is_waited_for(self):
        result, calls = self.fetch(self.view("completed success"), early_runs=[], wait="60")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls.count("run list"), 2)
        self.assertIn("run download 7", calls)

    def test_a_newer_run_of_the_commit_replaces_the_one_it_was_reading(self):
        # Marking a draft ready starts a CI run that cancels the older one, while this job runs on.
        early = [{"databaseId": 6, "displayTitle": "fix: a thing"}]
        result, calls = self.fetch(self.view("in_progress null"), self.view("completed success"),
                                   early_runs=early, wait="60")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("run view 6", calls)
        self.assertIn("run download 7", calls)

    def test_an_ignored_edit_is_not_the_run_it_reads(self):
        # A title or description edit starts a newer CI run with every job skipped.
        runs = [{"databaseId": 9, "displayTitle": "CI (ignored edit)"}, *self.RUNS]
        result, calls = self.fetch(self.view("completed success"), runs=runs)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("run view 7", calls)
        self.assertNotIn("run view 9", calls)

    def test_a_finished_report_that_did_not_pass_fails_at_once_and_says_how(self):
        for conclusion, named in (("failure", "failed"), ("cancelled", "was cancelled"),
                                  ("skipped", "was skipped"), ("timed_out", "timed_out")):
            with self.subTest(conclusion=conclusion):
                fetched = self.fetch(self.view(f"completed {conclusion}"), wait="3600")
                self.assert_refused(fetched, "run 7", named)
                self.assertEqual(fetched[1].count("run view"), 1)

    def test_a_finished_run_without_a_report_fails_at_once(self):
        fetched = self.fetch(self.view(run="completed"), wait="3600")
        self.assert_refused(fetched, "run 7", "finished without a Test report")
        self.assertEqual(fetched[1].count("run view"), 1)

    def test_nothing_finished_by_the_bound_fails_and_says_what_was_missing(self):
        self.assert_refused(self.fetch(self.view("in_progress null")), "run 7", "still 'in_progress", "0s")
        self.assert_refused(self.fetch(self.view("queued null")), "run 7", "still 'queued", "0s")
        self.assert_refused(self.fetch(self.view()), "run 7", "no Test report yet", "0s")
        self.assert_refused(self.fetch(self.view(), runs=[]), "no CI run of abc (merge_group)", "0s")


class Workflows(unittest.TestCase):
    """The live proofs reach the union; only the merge queue proves it runs."""

    def setUp(self):
        workflows = ROOT / ".github/workflows"
        self.suite = yaml.safe_load((workflows / "e2e-suite.yml").read_text())["jobs"]["suite"]
        self.e2e = yaml.safe_load((workflows / "e2e.yml").read_text())["jobs"]
        self.ci = yaml.safe_load((workflows / "ci.yml").read_text())["jobs"]

    def test_the_live_stage_runs_after_fresh_principals_and_reports(self):
        names = [step.get("name", "") for step in self.suite["steps"]]
        stage = next(s for s in self.suite["steps"] if s.get("run") == "chore test:contract-live-proofs")
        preparation = next(s for s in self.suite["steps"] if s.get("name") == "Prepare independent live-proof principals")
        self.assertEqual(stage["if"], "inputs.contract-phases == 'live'")
        # A report, while the owner has not said a failing runner should turn the run red.
        self.assertIs(stage["continue-on-error"], True)
        self.assertIn("timeout-minutes", stage)
        self.assertEqual(preparation["if"], stage["if"])
        self.assertIn("^TestContractPrincipals$", preparation["run"])
        self.assertIn("chore test:contract-fixtures", preparation["run"])
        self.assertLess(names.index(preparation["name"]), names.index(stage["name"]))
        # The limits suite spends the API's buckets; the own-data run's resets clear every principal.
        self.assertLess(names.index(stage["name"]), names.index("Limits suite"))
        upload = next(s for s in self.suite["steps"] if s.get("with", {}).get("name") == "live-example-proofs")
        self.assertEqual(upload["if"], "always() && inputs.contract-phases == 'live'")
        paths = upload["with"]["path"].splitlines()
        self.assertEqual(paths[0], stage["env"]["CONTRACT_LIVE_PROOFS"])
        self.assertEqual(paths[1], "!" + paths[0] + "/**/vertrag.yml")

    def test_the_union_gates_the_required_check_and_reads_the_live_results(self):
        union = self.e2e["e2e-proofs"]
        self.assertEqual(set(union["needs"]), {"preflight", "changes", "e2e-run", "e2e-contract-pairs", "e2e-live"})
        self.assertEqual(union["permissions"]["actions"], "read")
        download = next(s for s in union["steps"] if s.get("with", {}).get("name") == "live-example-proofs")
        # No artifact counts for nothing; the judgement below says so.
        self.assertIs(download["continue-on-error"], True)
        judge = next(s for s in union["steps"] if "scripts/example_proofs.py" in s.get("run", ""))
        self.assertIn('--live "$RUNNER_TEMP/live-proofs"', judge["run"])
        self.assertIn("-lt 4", judge["run"])
        self.assertNotIn("continue-on-error", judge)
        gate = self.e2e["e2e"]
        self.assertIn("e2e-proofs", gate["needs"])
        self.assertTrue(gate["if"].startswith("always()"), gate["if"])
        self.assertIn("PROOFS_RESULT", gate["steps"][0]["env"])
        self.assertIn('"$PROOFS_RESULT" != "success"', gate["steps"][0]["run"])

    def test_ci_defers_only_with_the_union_behind_it(self):
        report = self.ci["test-report"]["steps"]
        judge = next(s for s in report if "scripts/example_proofs.py" in s.get("run", ""))
        self.assertIn("--defer-live", judge["run"])
        self.assertNotIn("--live ", judge["run"])

    def test_the_union_can_outlast_the_scripts_own_bound(self):
        script = (ROOT / "scripts/ci_example_proofs.sh").read_text()
        bound = int(re.search(r"CI_PROOFS_WAIT_SECONDS:-(\d+)", script)[1])
        self.assertGreater(self.e2e["e2e-proofs"]["timeout-minutes"] * 60, bound)

    def test_the_live_stage_is_the_chore_a_person_runs(self):
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        command = tasks["test:contract-live-proofs"]["cmds"][0]
        self.assertIn("--api-keys", command)
        self.assertIn("tests/contract/live_proofs.sh", command)
        self.assertTrue(os.access(ROOT / "tests/contract/live_proofs.sh", os.X_OK))
        self.assertTrue(os.access(ROOT / "scripts/ci_example_proofs.sh", os.X_OK))

    def test_the_live_stage_clears_old_results_first(self):
        script = (ROOT / "tests/contract/live_proofs.sh").read_text()
        self.assertLess(script.index('rm -rf "$out"'), script.index("run_live anonymous"))


class LiveOwnerFixtureLifecycle(unittest.TestCase):
    def invoke(self, proof_status=0, add_status=0, revoke_status=0):
        command = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]["test:contract-live-proofs"]["cmds"][0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binaries = root / "bin"
            binaries.mkdir()
            live = root / "tests/contract/live_proofs.sh"
            live.parent.mkdir(parents=True)
            stubs = {
                binaries / "docker": "printf 'API_KEY_FIXTURE={}\\n'\n",
                binaries / "chore": (
                    'printf "%s\\n" "$*" >> "$TASK_LOG"\n'
                    'case "$3" in add) exit "$ADD_STATUS";; revoke) exit "$REVOKE_STATUS";; esac\n'
                ),
                live: 'echo live >> "$TASK_LOG"\nexit "$PROOF_STATUS"\n',
            }
            for script, body in stubs.items():
                script.write_text("#!/bin/sh\n" + body)
                script.chmod(0o700)
            log = root / "commands.log"
            result = subprocess.run(
                ["bash", "-c", command], cwd=root, capture_output=True, text=True,
                env={**os.environ, "PATH": str(binaries) + os.pathsep + os.environ["PATH"],
                     "CONTRACT_FIXTURE_CONFIG": "restmail.test", "CONTRACT_FIXTURE_PROJECT": "restmail-test-api",
                     "TASK_LOG": str(log), "PROOF_STATUS": str(proof_status),
                     "ADD_STATUS": str(add_status), "REVOKE_STATUS": str(revoke_status)},
            )
            return result.returncode, log.read_text().splitlines(), result.stderr

    def test_key_is_registered_before_proofs_and_revoked_after_success(self):
        code, commands, stderr = self.invoke()
        self.assertEqual(code, 0, stderr)
        self.assertEqual(commands, ["test:contract-owner-key -- add", "live", "test:contract-owner-key -- revoke"])

    def test_a_failed_proof_still_revokes_the_key_and_stays_failed(self):
        code, commands, _ = self.invoke(proof_status=1)
        self.assertEqual(code, 1)
        self.assertEqual(commands, ["test:contract-owner-key -- add", "live", "test:contract-owner-key -- revoke"])

    def test_failed_registration_is_not_green_and_does_not_suppress_other_proofs(self):
        code, commands, _ = self.invoke(add_status=1)
        self.assertEqual(code, 1)
        self.assertEqual(commands, ["test:contract-owner-key -- add", "live"])

    def test_failed_revocation_cannot_report_success(self):
        code, commands, _ = self.invoke(revoke_status=1)
        self.assertEqual(code, 1)
        self.assertEqual(commands[-1], "test:contract-owner-key -- revoke")


if __name__ == "__main__":
    unittest.main()
