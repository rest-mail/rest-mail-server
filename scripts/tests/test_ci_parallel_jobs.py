"""Parallel CI retains every race test, coverage profile, proof and local gate."""

from collections import Counter
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

from test_go_imap_gates import step
from test_stacked_pr_workflows import blocks, context, evaluate, field, shell_of


ROOT = Path(__file__).resolve().parents[2]
SOURCE = (ROOT / ".github/workflows/ci.yml").read_text()
JOBS = blocks(blocks(SOURCE, 0)["jobs"], 2)
STATUS_CHECK = re.compile(r"\b(?:always|cancelled|failure|success)\(\)")
POOL = "antimatter-vps-linux-x64"
FALLBACK = "ubuntu-24.04"
IMAP_GATES = (
    "Check the IMAP module's 32-bit sequence dispatch",
    "Validate the IMAP module's RFC checklist",
    "Test the local IMAP compatibility module with coverage",
    "Require at least 95% statement coverage",
    "Upload IMAP coverage evidence",
)


def needs_of(name):
    return field(JOBS[name], "needs", 4).strip("[]").split(", ")


def starts(name, results=None, cancelled=False, **event):
    """Whether GitHub starts the job, given the results of the jobs it needs."""
    condition = field(JOBS[name], "if", 4)
    values = context(**event)
    values["cancelled"] = cancelled
    values["needs"] = {need: {"result": (results or {}).get(need, "success")} for need in needs_of(name)}
    # GitHub puts success() in front of a condition with no status check of its own.
    if not STATUS_CHECK.search(condition):
        if cancelled or any(need["result"] != "success" for need in values["needs"].values()):
            return False
    return bool(evaluate(condition, values))


def after_failed_lint(outputs):
    values = context()
    values["needs"] = {"preflight": {"result": "success"}, "lint": {"result": "failure", "outputs": outputs}}
    return values


class FailedLintHidesNoOtherFailure(unittest.TestCase):
    """One run reports a lint failure and the IMAP module's failures together (#2153)."""

    def test_the_imap_tests_start_whatever_lint_concluded(self):
        for result in ("success", "failure", "cancelled"):
            with self.subTest(lint=result):
                self.assertTrue(starts("test-imap", {"lint": result}))

    def test_a_failed_lint_still_lets_the_jobs_after_the_imap_tests_report(self):
        imap = "success" if starts("test-imap", {"lint": "failure"}) else "skipped"
        for name in ("build", "test-report"):
            with self.subTest(job=name):
                self.assertTrue(starts(name, {"test-imap": imap}))

    def test_a_branch_that_failed_preflight_still_starts_no_imap_tests(self):
        for preflight in ("failure", "cancelled", "skipped"):
            for lint in ("success", "failure", "skipped"):
                with self.subTest(preflight=preflight, lint=lint):
                    self.assertFalse(starts("test-imap", {"preflight": preflight, "lint": lint}))

    def test_a_cancelled_run_starts_no_imap_tests(self):
        self.assertFalse(starts("test-imap", {"lint": "failure"}, cancelled=True))
        self.assertFalse(starts("test-imap", cancelled=True))

    def test_events_that_run_no_ci_jobs_still_start_no_imap_tests(self):
        for event in ({"event": "push", "actor": "github-merge-queue[bot]"}, {"action": "edited"}):
            with self.subTest(event=event):
                self.assertFalse(starts("test-imap", {"lint": "failure"}, **event))

    def test_a_lint_that_stopped_before_answering_runs_every_gate_on_the_fallback(self):
        values = after_failed_lint({})
        self.assertEqual(evaluate(field(JOBS["test-imap"], "runs-on", 4), values), FALLBACK)
        for name in IMAP_GATES:
            with self.subTest(step=name):
                self.assertTrue(evaluate(field(step(JOBS["test-imap"], name), "if", 8), values))

    def test_answers_lint_gave_before_failing_still_decide(self):
        values = after_failed_lint({"go_imap_changed": "false", "runners": '{"test-imap": "%s"}' % POOL})
        self.assertEqual(evaluate(field(JOBS["test-imap"], "runs-on", 4), values), POOL)
        for name in IMAP_GATES:
            with self.subTest(step=name):
                self.assertFalse(evaluate(field(step(JOBS["test-imap"], name), "if", 8), values))


class ParallelGates(unittest.TestCase):
    def test_independent_gates_do_not_wait_for_lint(self):
        for name in ("constellation", "contract-tooling", "test", "test-api", "test-handlers", "test-contract"):
            with self.subTest(job=name):
                self.assertEqual(field(JOBS[name], "needs", 4), "preflight")
        self.assertIn("needs: [preflight, lint]", JOBS["test-imap"])

    def test_report_waits_for_every_test_producer(self):
        needs = set(field(JOBS["test-report"], "needs", 4).strip("[]").split(", "))
        self.assertEqual(needs, {"preflight", "test", "test-api", "test-handlers", "test-contract", "test-imap"})

    def test_rest_package_selection_excludes_only_the_two_sharded_packages(self):
        script = shell_of(JOBS["test"], "Run unit tests with race detector")
        selector = next(line.strip() for line in script.splitlines() if line.strip().startswith("packages="))
        prefix = "github.com/rest-mail/rest-mail-server"
        packages = [f"{prefix}/internal/{name}" for name in ("api", "api/handlers", "api/middleware", "bootstrap", "db/repositories")]
        with tempfile.TemporaryDirectory() as directory:
            executable = Path(directory) / "go"
            executable.write_text("#!/bin/sh\nprintf '%s\\n' " + " ".join(packages) + "\n")
            executable.chmod(0o755)
            result = subprocess.run(["bash", "-c", selector + '\nprintf "%s\\n" "$packages"'],
                                    env=dict(os.environ, PATH=directory + os.pathsep + os.environ["PATH"]),
                                    capture_output=True, text=True, timeout=600)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), packages[2:])
        for required in ("go test -race -count=1 -timeout=2h", '-exec "$RUNNER_TEMP/testdb-exec"',
                         "-coverprofile=coverage-rest.out", "$packages ./cmd/... ./tests/contract/browser_sessions"):
            self.assertIn(required, script)

    def test_both_matrices_run_every_test_example_and_fuzz_seed_once(self):
        names = [f"TestCase{number:02d}" for number in range(17)] + ["ExampleImport", "FuzzImport"]
        for name in ("test-api", "test-handlers"):
            selected = []
            self.assertIn("shard: [1, 2, 3, 4]", JOBS[name])
            self.assertIn("fail-fast: false", JOBS[name])
            script = shell_of(JOBS[name], "Choose this shard's tests")
            for shard in range(1, 5):
                with self.subTest(job=name, shard=shard), tempfile.TemporaryDirectory() as directory:
                    executable = Path(directory) / "go"
                    executable.write_text("#!/bin/sh\nprintf '%s\\n' " + " ".join(reversed(names)) + " 'ok example 0.0s'\n")
                    executable.chmod(0o755)
                    result = subprocess.run(["bash", "-c", script], capture_output=True, text=True, timeout=600,
                                            env=dict(os.environ, PATH=directory + os.pathsep + os.environ["PATH"],
                                                     RUNNER_TEMP=directory, GITHUB_ENV=str(Path(directory) / "env"),
                                                     SHARD=str(shard), SHARDS="4"))
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    chosen = (Path(directory) / "shard-tests").read_text().splitlines()
                    self.assertEqual(chosen, sorted(names)[shard - 1::4])
                    selected.extend(chosen)
            self.assertEqual(Counter(selected), Counter(names))

    def test_handlers_keep_race_coverage_database_and_long_import_proof(self):
        source = JOBS["test-handlers"]
        self.assertIn("image: postgres:16-alpine", source)
        self.assertIn('RESTMAIL_IMPORT_LONG_PROOF: "1"', source)
        self.assertIn('DB_HOST: localhost', source)
        self.assertIn('go test -race -count=1 -timeout=2h -coverprofile=coverage-handlers-${{ matrix.shard }}.out -run "$RUN_PATTERN" ./internal/api/handlers/', source)

    def test_missing_any_coverage_profile_fails_before_merging(self):
        profiles = ["rest"] + [f"{package}-{shard}" for package in ("api", "handlers") for shard in range(1, 5)]
        script = shell_of(JOBS["test-report"], "Merge coverage")
        for missing in profiles:
            with self.subTest(profile=missing), tempfile.TemporaryDirectory() as directory:
                coverage = Path(directory) / "coverage"
                coverage.mkdir()
                for profile in profiles:
                    if profile != missing:
                        (coverage / f"coverage-{profile}.out").write_text("mode: atomic\n")
                result = subprocess.run(["bash", "-c", script], env=dict(os.environ, RUNNER_TEMP=directory),
                                        capture_output=True, text=True, timeout=600)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(f"missing coverage profile: {missing}", result.stderr)

    def test_other_proofs_cannot_hide_a_missing_api_shard(self):
        script = shell_of(JOBS["test-report"], "Every example that needs setup is proven")
        for missing in range(1, 5):
            with self.subTest(shard=missing), tempfile.TemporaryDirectory() as directory:
                proofs = Path(directory) / "proofs"
                proofs.mkdir()
                for shard in range(1, 5):
                    if shard != missing:
                        (proofs / f"example-proofs-api-{shard}.json").write_text("{}")
                    (proofs / f"example-proofs-handlers-{shard}.json").write_text("{}")
                result = subprocess.run(["bash", "-c", script], env=dict(os.environ, RUNNER_TEMP=directory),
                                        capture_output=True, text=True, timeout=600)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(f"missing example proofs from internal/api shard {missing}", result.stderr)


if __name__ == "__main__":
    unittest.main()
