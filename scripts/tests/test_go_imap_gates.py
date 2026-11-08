"""The vendored go-imap module's gates run from the root workflows (#1925).

GitHub runs only the workflows at a repository's root, so a workflow inside
third_party/go-imap would never run. These read the real workflows and run the
real scripts; Go itself runs in CI, and a stand-in for it here.
"""

import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
MODULE = ROOT / "third_party/go-imap"
CHANGED = ROOT / "scripts/ci/go-imap-changed"
FUZZ = ROOT / "scripts/ci/go-imap-fuzz"
GATE = MODULE / ".github/scripts/check_coverage.py"
CI = (ROOT / ".github/workflows/ci.yml").read_text()
SCHEDULED = (ROOT / ".github/workflows/scheduled.yml").read_text()
RUN_GATES = "needs.lint.outputs.go_imap_changed != 'false'"
RUN_LINT = "steps.go-imap.outputs.go_imap_changed != 'false'"


def job(workflow, name):
    return re.split(r"^  [\w-]+:\n", workflow.split(f"\n  {name}:\n", 1)[1], maxsplit=1, flags=re.MULTILINE)[0]


def step(source, name):
    return source.split(f"      - name: {name}\n", 1)[1].split("      - name:", 1)[0]


def git(directory, *args):
    subprocess.run(["git", "-C", str(directory), *args], check=True, capture_output=True, text=True,
                   env=dict(os.environ, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@example.test",
                            GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@example.test",
                            GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull))


class CoverageGateRuns(unittest.TestCase):
    def setUp(self):
        self.test = job(CI, "test-imap")

    def test_race_coverage_includes_external_wire_proofs(self):
        run = step(self.test, "Test the local IMAP compatibility module with coverage")
        self.assertIn("run: go test -race -count=1 -timeout=10m -covermode=atomic "
                      "-coverpkg=./... -coverprofile=coverage.out ./...\n", run)
        self.assertLess(self.test.index("-coverprofile=coverage.out"), self.test.index("go tool cover -func=coverage.out"))
        # Only the proof steps: an upload after them may not fail its job (#1896).
        self.assertNotIn("continue-on-error", run)
        self.assertNotIn("continue-on-error", step(self.test, "Require at least 95% statement coverage"))

    def test_gate_is_the_modules_own_and_fails_closed(self):
        block = step(self.test, "Require at least 95% statement coverage")
        self.assertIn("set -euo pipefail\n", block)
        self.assertIn("go tool cover -func=coverage.out | tee coverage.txt | python3 -B .github/scripts/check_coverage.py\n", block)
        # A failed coverage producer must fail even if its output looks valid.
        result = subprocess.run(
            ["bash", "-euo", "pipefail", "-c", "(printf 'total: (statements) 100.0%%\\n'; exit 1) | \"$PYTHON\" -B \"$GATE\""],
            env={**os.environ, "PYTHON": sys.executable, "GATE": str(GATE)}, capture_output=True, text=True, check=False,
        )
        self.assertNotEqual(result.returncode, 0)

    def test_the_gate_keeps_95_as_its_floor(self):
        self.assertIn("if not Decimal(95) <= total <= Decimal(100):", GATE.read_text())

    def test_failed_gate_keeps_its_evidence(self):
        block = step(self.test, "Upload IMAP coverage evidence")
        self.assertIn(f"if: ${{{{ !cancelled() && {RUN_GATES} }}}}\n", block)
        self.assertIn("third_party/go-imap/coverage.out\n", block)
        self.assertIn("third_party/go-imap/coverage.txt\n", block)

    def test_each_gate_runs_in_the_module_when_it_can_have_changed(self):
        for name in ("Check the IMAP module's 32-bit sequence dispatch",
                     "Validate the IMAP module's RFC checklist",
                     "Test the local IMAP compatibility module with coverage",
                     "Require at least 95% statement coverage"):
            with self.subTest(step=name):
                block = step(self.test, name)
                self.assertIn(f"if: {RUN_GATES}\n", block)
                self.assertIn("working-directory: third_party/go-imap\n", block)

    def test_the_module_is_tested_once_and_only_when_it_can_have_changed(self):
        # != 'false' on the gate: a missing answer runs it. It is the module's only full
        # test run, so nothing tests it twice and nothing tests it with no change (#1896).
        block = step(self.test, "Test the local IMAP compatibility module with coverage")
        self.assertIn(f"if: {RUN_GATES}\n", block)
        self.assertEqual(self.test.count("working-directory: third_party/go-imap\n        run: go test -race"), 1)

    def test_the_decision_reaches_the_test_job_for_every_event(self):
        lint = job(CI, "lint")
        self.assertIn("go_imap_changed: ${{ steps.go-imap.outputs.go_imap_changed }}\n", lint)
        self.assertIn("needs: [preflight, lint]\n", self.test)
        decide = step(lint, "Decide whether the vendored IMAP module's gates run")
        for base in ("github.event.pull_request.base.sha", "github.event.merge_group.base_sha", "github.event.before"):
            self.assertIn(base, decide)
        self.assertIn("run: scripts/ci/go-imap-changed\n", decide)
        self.assertIn("  merge_group:\n", CI)

    def test_the_32_bit_check_names_tests_that_exist(self):
        block = step(self.test, "Check the IMAP module's 32-bit sequence dispatch")
        self.assertIn('GOARCH: "386"\n', block)
        self.assertIn('CGO_ENABLED: "0"\n', block)
        pattern = re.search(r"-run '\^\((.+)\)\$'", block)[1]
        sources = "\n".join(path.read_text() for path in (MODULE / "pkg").rglob("*_test.go"))
        # -run with no match passes without running anything.
        for name in pattern.split("|"):
            with self.subTest(test=name):
                self.assertRegex(sources, rf"(?m)^func {name.replace('.*', '')}")

    def test_vet_lint_and_fixtures_run_on_the_module_itself(self):
        lint = job(CI, "lint")
        for name, command in (("Check the IMAP coverage gate's fixtures", "run: python3 -B -m unittest discover -s .github/tests -v\n"),
                              ("Run go vet on the IMAP module", "run: go vet ./...\n"),
                              ("Run golangci-lint on the IMAP module", "args: --timeout=5m --no-config\n")):
            with self.subTest(step=name):
                block = step(lint, name)
                self.assertIn(f"if: {RUN_LINT}\n", block)
                self.assertIn("working-directory: third_party/go-imap\n", block)
                self.assertIn(command, block)

    def test_no_workflow_hides_inside_the_vendored_module(self):
        self.assertEqual(list(MODULE.glob(".github/workflows/*")), [])

    def test_the_module_has_no_dependencies(self):
        # Which is why its own files and the workflow's Go version decide the answer.
        self.assertNotIn("require", (MODULE / "go.mod").read_text())


class TheDecision(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.repo = Path(self.directory.name) / "repo"
        self.repo.mkdir()
        self.output = Path(self.directory.name) / "output"
        git(self.repo, "init", "--quiet")
        for path in ("third_party/go-imap/a.go", ".github/workflows/ci.yml", "scripts/ci/go-imap-changed", "internal/b.go"):
            self.write(path)
        self.base = self.commit("base")

    def write(self, path, text="one\n"):
        target = self.repo / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def commit(self, message):
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "--quiet", "--allow-empty", "-m", message)
        return subprocess.run(["git", "-C", str(self.repo), "rev-parse", "HEAD"], check=True, capture_output=True, text=True).stdout.strip()

    def decide(self, base):
        self.output.write_text("")
        result = subprocess.run([str(CHANGED)], cwd=self.repo, capture_output=True, text=True, timeout=600,
                                env=dict(os.environ, BASE_SHA=base, GITHUB_OUTPUT=str(self.output)))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return self.output.read_text()

    def test_a_change_the_gates_depend_on_runs_them(self):
        for path in ("third_party/go-imap/a.go", ".github/workflows/ci.yml", "scripts/ci/go-imap-changed", "third_party/go-imap/new.go"):
            with self.subTest(path=path):
                git(self.repo, "reset", "--quiet", "--hard", self.base)
                git(self.repo, "clean", "--quiet", "-fd")
                self.write(path, "two\n")
                self.commit("change")
                self.assertEqual(self.decide(self.base), "go_imap_changed=true\n")

    def test_any_other_change_does_not(self):
        self.write("internal/b.go", "two\n")
        self.commit("change")
        self.assertEqual(self.decide(self.base), "go_imap_changed=false\n")

    def test_an_event_with_no_base_runs_them(self):
        for base in ("", "0" * 40):
            with self.subTest(base=base):
                self.assertEqual(self.decide(base), "go_imap_changed=true\n")

    def test_a_base_that_cannot_be_fetched_runs_them(self):
        self.assertEqual(self.decide("f" * 40), "go_imap_changed=true\n")

    def test_a_base_on_the_remote_is_fetched(self):
        clone = Path(self.directory.name) / "clone"
        subprocess.run(["git", "clone", "--quiet", "--depth=1", f"file://{self.repo}", str(clone)], check=True, capture_output=True)
        self.write("third_party/go-imap/a.go", "two\n")
        head = self.commit("change")
        git(clone, "fetch", "--quiet", "--depth=1", "origin", head)
        git(clone, "checkout", "--quiet", head)
        # The clone holds only the head; the script fetches the base by its hash.
        git(self.repo, "config", "uploadpack.allowAnySHA1InWant", "true")
        self.repo = clone
        self.assertEqual(self.decide(self.base), "go_imap_changed=true\n")


class NightlyFuzzing(unittest.TestCase):
    def setUp(self):
        self.job = job(SCHEDULED, "go-imap-fuzz")
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.temp = Path(self.directory.name)
        self.logs = self.temp / "logs"
        self.calls = self.temp / "calls"

    def fuzz(self, listing):
        """Run the real script with a stand-in for go; listing maps package to targets."""
        bin_dir = self.temp / "bin"
        bin_dir.mkdir(exist_ok=True)
        cases = "\n".join(f"      {package}) printf '{''.join(name + chr(92) + 'n' for name in names)}ok {package} 0.01s\\n' ;;"
                          for package, names in listing.items())
        fake = bin_dir / "go"
        fake.write_text(textwrap.dedent("""\
            #!/usr/bin/env bash
            if [ "$1" = list ]; then
              printf '%s\\n' {packages}
              exit 0
            fi
            if [ "$2" = -list ]; then
              case "$4" in
            {cases}
              esac
              exit 0
            fi
            echo "$*" >> "$CALLS"
            case "$*" in *FuzzBad*) echo "Failing input written to testdata/fuzz/FuzzBad/abc"; exit 1 ;; esac
            exit 0
            """).format(packages=" ".join(listing), cases=cases))
        fake.chmod(0o755)
        return subprocess.run([str(FUZZ)], cwd=self.temp, capture_output=True, text=True, timeout=600,
                              env=dict(os.environ, PATH=f"{bin_dir}:{os.environ['PATH']}", CALLS=str(self.calls),
                                       FUZZ_LOG_DIR=str(self.logs), FUZZTIME="7x"))

    def test_every_target_found_runs_alone_from_its_package(self):
        result = self.fuzz({"example/a": ["FuzzOne", "FuzzTwo"], "example/b": [], "example/c": ["FuzzThree"]})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.calls.read_text().splitlines(), [
            "test -run=^$ -fuzz=^FuzzOne$ -fuzztime=7x example/a",
            "test -run=^$ -fuzz=^FuzzTwo$ -fuzztime=7x example/a",
            "test -run=^$ -fuzz=^FuzzThree$ -fuzztime=7x example/c",
        ])
        self.assertEqual((self.logs / "failed.txt").read_text(), "")

    def test_a_failure_fails_the_run_after_the_others_ran(self):
        result = self.fuzz({"example/a": ["FuzzBad", "FuzzGood"]})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("FuzzGood", self.calls.read_text())
        self.assertEqual((self.logs / "failed.txt").read_text(), "FuzzBad example/a\n")
        self.assertIn("Failing input written", (self.logs / "FuzzBad.log").read_text())

    def test_finding_no_targets_fails(self):
        self.assertNotEqual(self.fuzz({"example/a": []}).returncode, 0)

    def test_the_module_declares_targets_to_find(self):
        declared = [name for path in MODULE.rglob("*_test.go")
                    for name in re.findall(r"(?m)^func (Fuzz\w+)\(\w+ \*testing\.F\)", path.read_text())]
        self.assertTrue(declared)

    def test_the_job_runs_the_script_from_the_module_at_the_modules_budget(self):
        self.assertIn("FUZZTIME: 500000x\n", self.job)
        block = step(self.job, "Fuzz every target")
        self.assertIn("working-directory: third_party/go-imap\n", block)
        # The runner context is not available in a job's own env.
        self.assertIn("FUZZ_LOG_DIR: ${{ runner.temp }}/go-imap-fuzz\n", block)
        self.assertEqual(self.job.split("    steps:\n", 1)[0].count("runner."), 0)
        self.assertIn("run: ../../scripts/ci/go-imap-fuzz\n", block)
        # Only the fuzzing: the upload after it may not fail its job (#1896).
        self.assertNotIn("continue-on-error", block)

    def test_a_failure_is_visible(self):
        keep = step(self.job, "Keep the logs and failing inputs")
        self.assertIn("if: failure() && steps.fuzz.outcome == 'failure'\n", keep)
        self.assertIn("third_party/go-imap/pkg/**/testdata/fuzz/**\n", keep)
        issue = step(self.job, "Open or update the issue")
        self.assertIn("if: failure() && steps.fuzz.outcome == 'failure'\n", issue)
        self.assertIn('gh issue create --repo "$GITHUB_REPOSITORY" --title "$TITLE" --label bug', issue)
        self.assertIn("gh issue close", step(self.job, "Close the issue once every target passes"))

    def test_the_nightly_workflow_may_write_the_issue(self):
        self.assertIn("  issues: write\n", SCHEDULED)
        self.assertIn('    - cron: "0 6 * * *"\n', SCHEDULED)


if __name__ == "__main__":
    unittest.main()
