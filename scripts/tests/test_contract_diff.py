"""The real OpenAPI 3.1 comparator and REST Mail's deliberate-change gates (#653)."""

import json
import os
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BINARY = os.environ.get("OASDIFF")


def description():
    return {
        "openapi": "3.1.0",
        "info": {"title": "Contract gate fixture", "version": "1.0.0"},
        "components": {
            "securitySchemes": {
                "CookieAuth": {"type": "apiKey", "in": "cookie", "name": "session"}
            }
        },
        "paths": {
            "/widgets": {
                "post": {
                    "operationId": "writeWidget",
                    "security": [{"CookieAuth": ["widgets:write"]}],
                    "requestBody": {
                        "required": True,
                        "content": {
                            "application/json": {
                                "schema": {
                                    "type": "object",
                                    "additionalProperties": False,
                                    "properties": {
                                        "name": {"type": ["string", "null"]},
                                        "kind": {"type": "string", "const": "widget"},
                                    },
                                    "required": ["kind"],
                                }
                            }
                        },
                    },
                    "responses": {
                        "200": {
                            "description": "A widget",
                            "content": {
                                "application/json": {
                                    "schema": {
                                        "type": "object",
                                        "properties": {
                                            "id": {"type": "integer"},
                                            "label": {"type": "string"},
                                            "state": {
                                                "type": "string",
                                                "enum": ["ready", "waiting"],
                                            },
                                        },
                                        "required": ["id", "state"],
                                    }
                                }
                            },
                        }
                    },
                }
            }
        },
    }


@unittest.skipUnless(
    BINARY, "OASDIFF names the pinned binary; contract-diff CI installs it"
)
class ContractDiff(unittest.TestCase):
    def run_diff(self, revision, labels=(), base=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, value in (
                ("base", base or description()),
                ("revision", revision),
            ):
                (root / (name + ".json")).write_text(json.dumps(value))
            result = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / "scripts/contract_diff.py"),
                    str(root / "base.json"),
                    str(root / "revision.json"),
                    "--oasdiff",
                    BINARY,
                    *(arg for label in labels for arg in ("--label", label)),
                    "--output",
                    str(root / "report"),
                ],
                capture_output=True,
                text=True,
                check=False,
                env=dict(os.environ, GITHUB_STEP_SUMMARY=str(root / "summary.md")),
                timeout=20,
            )
            path = root / "report/changes.json"
            report = json.loads(path.read_text()) if path.exists() else None
            summary = (root / "summary.md").read_text()
            return result, report, summary

    def test_unchanged_openapi31_nullable_and_const_are_not_breaking(self):
        result, report, summary = self.run_diff(description())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(report["changes"], [])
        self.assertIn("No consumer-visible contract change", summary)

    def test_new_operation_is_additive_without_a_label(self):
        revision = description()
        revision["paths"]["/widgets"]["get"] = {
            "operationId": "listWidgets",
            "responses": {"204": {"description": "No content"}},
        }
        result, report, _ = self.run_diff(revision)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(
            any(c["classification"] == "additive" for c in report["changes"])
        )

    def test_optional_response_field_removal_needs_contract_label(self):
        revision = description()
        del revision["paths"]["/widgets"]["post"]["responses"]["200"]["content"][
            "application/json"
        ]["schema"]["properties"]["label"]
        result, report, summary = self.run_diff(revision)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])
        self.assertIn("writeWidget", summary)
        self.assertEqual(self.run_diff(revision, ["contract-change"])[0].returncode, 0)

    def test_response_enum_removal_and_request_type_widening_need_contract_label(self):
        for kind in ("enum", "type"):
            revision = description()
            operation = revision["paths"]["/widgets"]["post"]
            if kind == "enum":
                operation["responses"]["200"]["content"]["application/json"]["schema"][
                    "properties"
                ]["state"]["enum"] = ["ready"]
            else:
                operation["requestBody"]["content"]["application/json"]["schema"][
                    "properties"
                ]["name"] = {"type": ["string", "number", "null"]}
            with self.subTest(kind=kind):
                result, report, _ = self.run_diff(revision)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("contract-change", report["missing_labels"])

    def test_new_required_request_field_needs_contract_label(self):
        revision = description()
        revision["paths"]["/widgets"]["post"]["requestBody"]["content"][
            "application/json"
        ]["schema"]["required"].append("name")
        result, report, _ = self.run_diff(revision)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])

    def test_operation_removal_needs_contract_label(self):
        revision = description()
        del revision["paths"]["/widgets"]["post"]
        result, report, _ = self.run_diff(revision)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])

    def test_error_status_removal_needs_contract_label(self):
        base = description()
        base["paths"]["/widgets"]["post"]["responses"]["404"] = {
            "description": "No such widget"
        }
        result, report, _ = self.run_diff(description(), base=base)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])
        self.assertIn(
            ("response-non-success-status-removed", "breaking"),
            [(c["id"], c["classification"]) for c in report["changes"]],
        )
        self.assertEqual(
            self.run_diff(description(), ["contract-change"], base)[0].returncode, 0
        )

    def test_credential_location_change_needs_permission_label(self):
        revision = description()
        revision["components"]["securitySchemes"]["CookieAuth"]["in"] = "header"
        result, report, _ = self.run_diff(revision, ["contract-change"])
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["permission-change"])

    def test_permission_loosening_and_anonymous_access_need_permission_label(self):
        for security in ([{"CookieAuth": []}], []):
            revision = description()
            revision["paths"]["/widgets"]["post"]["security"] = security
            with self.subTest(security=security):
                result, report, _ = self.run_diff(revision, ["contract-change"])
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(report["missing_labels"], ["permission-change"])
                self.assertEqual(
                    self.run_diff(revision, ["permission-change"])[0].returncode, 0
                )

    def test_security_and_response_break_together_need_both_labels(self):
        revision = description()
        operation = revision["paths"]["/widgets"]["post"]
        operation["security"] = []
        del operation["responses"]["200"]["content"]["application/json"]["schema"][
            "properties"
        ]["id"]
        operation["responses"]["200"]["content"]["application/json"]["schema"][
            "required"
        ].remove("id")
        result, report, _ = self.run_diff(revision)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(
            report["missing_labels"], ["contract-change", "permission-change"]
        )
        self.assertEqual(
            self.run_diff(revision, report["missing_labels"])[0].returncode, 0
        )

    def test_external_refs_fail_the_gate_instead_of_fetching_them(self):
        revision = description()
        revision["paths"]["/widgets"]["post"]["requestBody"]["content"][
            "application/json"
        ]["schema"] = {"$ref": "http://127.0.0.1:1/forbidden.json"}
        result, report, summary = self.run_diff(revision)
        self.assertEqual(result.returncode, 2)
        self.assertIsNone(report)
        self.assertIn("comparison failed", summary)


CI_WORKFLOW = ROOT / ".github/workflows/ci.yml"
BRANCH_POINT_STEP = "Find where the PR branched from its base"


def workflow_step(name):
    """One step of ci.yml's contract job as text, without a YAML dependency.

    The lint job discovers these tests before it installs pyyaml.
    """
    lines = CI_WORKFLOW.read_text().splitlines()
    start = next(i for i, line in enumerate(lines) if line.strip() == f"- name: {name}")
    indent = len(lines[start]) - len(lines[start].lstrip())
    step = []
    for line in lines[start + 1 :]:
        if line.strip() and len(line) - len(line.lstrip()) <= indent:
            break
        step.append(line)
    return "\n".join(step)


def run_block(step):
    """The shell a step's `run: |` block holds."""
    return textwrap.dedent(step.split("run: |\n", 1)[1])


class GitFixture:
    """A real repository, so the workflow's own shell finds a real merge base.

    Nothing from the running git's hooks or config reaches it: the tests may run
    from a commit hook, where GIT_DIR names the outer repository.
    """

    def __init__(self, root):
        self.root = Path(root)
        self.origin = self.root / "origin"
        self.clone = self.root / "clone"
        self.temp = self.root / "runner"
        self.temp.mkdir()
        self.commits = 0
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        self.env.update(
            GIT_CONFIG_GLOBAL=os.devnull,
            GIT_CONFIG_NOSYSTEM="1",
            GIT_AUTHOR_NAME="Fixture",
            GIT_AUTHOR_EMAIL="fixture@example.test",
            GIT_COMMITTER_NAME="Fixture",
            GIT_COMMITTER_EMAIL="fixture@example.test",
        )
        self.origin.mkdir()
        self.git("init", "--quiet", "--initial-branch=main")
        self.git("config", "uploadpack.allowAnySHA1InWant", "true")

    def git(self, *args, cwd=None):
        return subprocess.run(
            ["git", *args],
            cwd=cwd or self.origin,
            env=self.env,
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()

    def commit(self, spec):
        (self.origin / "openapi.json").write_text(json.dumps(spec))
        self.git("add", "openapi.json")
        # Unique: two same-second commits with one parent and tree are one commit.
        self.commits += 1
        self.git("commit", "--quiet", "--allow-empty", "-m", f"change {self.commits}")
        return self.git("rev-parse", "HEAD")

    def branch(self, name, start):
        self.git("checkout", "--quiet", "-b", name, start)

    def spec_at(self, revision):
        return json.loads(self.git("show", f"{revision}:openapi.json"))

    def clone_origin(self):
        self.git("clone", "--quiet", str(self.origin), str(self.clone), cwd=self.root)

    def branch_point(self, base_tip, head):
        """Run the workflow's own step; a missing clone is made from origin."""
        if not self.clone.exists():
            self.clone_origin()
        output = self.temp / "output"
        summary = self.temp / "summary"
        result = subprocess.run(
            ["bash", "-c", run_block(workflow_step(BRANCH_POINT_STEP))],
            cwd=self.clone,
            env=dict(
                self.env,
                BASE_SHA=base_tip,
                HEAD_SHA=head,
                RUNNER_TEMP=str(self.temp),
                GITHUB_OUTPUT=str(output),
                GITHUB_STEP_SUMMARY=str(summary),
            ),
            capture_output=True,
            text=True,
            check=False,
            timeout=20,
        )
        outputs = output.read_text() if output.exists() else ""
        resolved = outputs.split("sha=", 1)[1].strip() if "sha=" in outputs else None
        return result, resolved, summary.read_text() if summary.exists() else ""


class BranchPointStep(unittest.TestCase):
    """The base the contract job compares from (#1752). Needs git, not oasdiff."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.git = GitFixture(directory.name)

    def test_up_to_date_branch_compares_from_the_base_tip(self):
        base_tip = self.git.commit(description())
        self.git.branch("pr", base_tip)
        head = self.git.commit(description())
        result, resolved, _ = self.git.branch_point(base_tip, head)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(resolved, base_tip)

    def test_stale_branch_compares_from_where_it_branched(self):
        start = self.git.commit(description())
        base_tip = self.git.commit(description())
        self.git.branch("pr", start)
        head = self.git.commit(description())
        result, resolved, _ = self.git.branch_point(base_tip, head)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(resolved, start)

    def test_the_exact_revisions_are_published(self):
        start = self.git.commit(description())
        base_tip = self.git.commit(description())
        self.git.branch("pr", start)
        head = self.git.commit(description())
        _, _, summary = self.git.branch_point(base_tip, head)
        comparison = json.loads(
            (self.git.temp / "contract-diff/comparison.json").read_text()
        )
        self.assertEqual(
            comparison,
            {
                "comparison": "merge-base",
                "base_tip": base_tip,
                "merge_base": start,
                "head": head,
                "base_commits_since_merge_base": 1,
            },
        )
        for revision in (base_tip, start, head):
            self.assertIn(revision, summary)

    def test_a_base_tip_on_no_branch_is_fetched_by_its_exact_revision(self):
        # A rerun keeps its event's base, which a force-pushed parent branch may
        # no longer reach; checkout's full history then does not include it.
        start = self.git.commit(description())
        self.git.branch("pr", start)
        head = self.git.commit(description())
        self.git.branch("parent", start)
        base_tip = self.git.commit(description())
        self.git.git("checkout", "--quiet", "main")
        self.git.clone_origin()
        self.git.git("update-ref", "-d", "refs/heads/parent")
        self.git.git(
            "update-ref", "-d", "refs/remotes/origin/parent", cwd=self.git.clone
        )
        self.git.git("gc", "--quiet", "--prune=now", cwd=self.git.clone)
        with self.assertRaises(subprocess.CalledProcessError):
            self.git.git("cat-file", "-e", f"{base_tip}^{{commit}}", cwd=self.git.clone)
        self.git.git("update-ref", "refs/fixture/keep", base_tip)
        result, resolved, _ = self.git.branch_point(base_tip, head)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(resolved, start)

    def test_unrelated_histories_fail_instead_of_comparing_from_the_tip(self):
        base_tip = self.git.commit(description())
        self.git.git("checkout", "--quiet", "--orphan", "pr")
        head = self.git.commit(description())
        result, resolved, _ = self.git.branch_point(base_tip, head)
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(resolved)
        self.assertIn("no single merge base", result.stderr)

    def test_two_merge_bases_fail_instead_of_choosing_one(self):
        start = self.git.commit(description())
        self.git.branch("pr", start)
        pr_side = self.git.commit(description())
        self.git.git("checkout", "--quiet", "main")
        main_side = self.git.commit(description())
        self.git.git("merge", "--quiet", "--no-edit", "--no-ff", pr_side)
        base_tip = self.git.git("rev-parse", "HEAD")
        self.git.git("checkout", "--quiet", "pr")
        self.git.git("merge", "--quiet", "--no-edit", "--no-ff", main_side)
        head = self.git.git("rev-parse", "HEAD")
        result, resolved, _ = self.git.branch_point(base_tip, head)
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(resolved)
        self.assertIn("no single merge base", result.stderr)

    def test_the_job_checks_out_the_step_result_and_keeps_its_event(self):
        head = workflow_step("Checkout the exact PR head")
        self.assertIn("ref: ${{ github.event.pull_request.head.sha }}", head)
        self.assertIn("fetch-depth: 0", head)
        self.assertIn("persist-credentials: false", head)
        step = workflow_step(BRANCH_POINT_STEP)
        self.assertIn("id: branch-point", step)
        self.assertIn("BASE_SHA: ${{ github.event.pull_request.base.sha }}", step)
        self.assertIn("HEAD_SHA: ${{ github.event.pull_request.head.sha }}", step)
        base = workflow_step("Checkout the comparison base")
        self.assertIn("ref: ${{ steps.branch-point.outputs.sha }}", base)
        self.assertIn("path: .cache/contract-diff-base", base)
        self.assertIn("persist-credentials: false", base)
        self.assertIn(
            "cd .cache/contract-diff-base",
            workflow_step("Generate the base and head descriptions"),
        )
        # A merge group has no pull request: the job stays skipped there, and the
        # verdict accepts that, so the queue's result does not depend on this job.
        text = CI_WORKFLOW.read_text()
        job = text[text.index("  contract-diff:\n") :]
        self.assertIn("if: github.event_name == 'pull_request'", job[:200])
        self.assertIn("success | skipped) ;;", text)


def base_only_change(spec):
    """What main adds after a pull request branched: an operation and a field."""
    spec["paths"]["/gadgets"] = {
        "get": {
            "operationId": "listGadgets",
            "responses": {"204": {"description": "No content"}},
        }
    }
    spec["paths"]["/widgets"]["post"]["responses"]["200"]["content"][
        "application/json"
    ]["schema"]["properties"]["colour"] = {"type": "string"}
    return spec


@unittest.skipUnless(
    BINARY, "OASDIFF names the pinned binary; contract-diff CI installs it"
)
class StaleBranchComparison(unittest.TestCase):
    """The whole path: the workflow's base, the real comparator, the gates (#1752)."""

    run_diff = ContractDiff.run_diff

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.git = GitFixture(directory.name)

    def compare(self, head_change, labels=()):
        """A stale pull request making head_change, gated as CI gates it."""
        start = self.git.commit(description())
        base_tip = self.git.commit(base_only_change(description()))
        self.git.branch("pr", start)
        head = self.git.commit(head_change(description()))
        step, resolved, _ = self.git.branch_point(base_tip, head)
        self.assertEqual(step.returncode, 0, step.stderr)
        self.assertEqual(resolved, start)
        self.base_tip = base_tip
        self.head = head
        return self.run_diff(
            self.git.spec_at(head), labels, base=self.git.spec_at(resolved)
        )

    def assert_names_nothing_from_main(self, report):
        reported = json.dumps(report["changes"])
        self.assertNotIn("gadgets", reported.lower())
        self.assertNotIn("colour", reported)

    def test_stale_branch_is_not_blamed_for_what_main_added(self):
        result, report, summary = self.compare(lambda spec: spec)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(report["changes"], [])
        self.assertEqual(report["missing_labels"], [])
        self.assertIn("No consumer-visible contract change", summary)
        # The fixture does reproduce #1752: from the base tip, the same unchanged
        # head loses the operation and the field, and the gate asks for a label.
        result, report, _ = self.run_diff(
            self.git.spec_at(self.head), base=self.git.spec_at(self.base_tip)
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])
        self.assertIn("gadgets", json.dumps(report["changes"]).lower())
        self.assertIn("colour", json.dumps(report["changes"]))

    def test_stale_branch_reports_only_its_own_additions(self):
        def add_operation(spec):
            spec["paths"]["/widgets"]["get"] = {
                "operationId": "listWidgets",
                "responses": {"204": {"description": "No content"}},
            }
            return spec

        result, report, _ = self.compare(add_operation)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            [(c["id"], c["classification"]) for c in report["changes"]],
            [("endpoint-added", "additive")],
        )
        self.assert_names_nothing_from_main(report)

    def test_stale_branch_field_removal_still_needs_contract_label(self):
        def remove_field(spec):
            del spec["paths"]["/widgets"]["post"]["responses"]["200"]["content"][
                "application/json"
            ]["schema"]["properties"]["label"]
            return spec

        result, report, summary = self.compare(remove_field)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])
        self.assertIn("writeWidget", summary)
        self.assert_names_nothing_from_main(report)
        self.assertEqual(
            self.run_diff(
                self.git.spec_at(self.head),
                ["contract-change"],
                base=description(),
            )[0].returncode,
            0,
        )

    def test_stale_branch_operation_removal_still_needs_contract_label(self):
        def remove_operation(spec):
            del spec["paths"]["/widgets"]["post"]
            return spec

        result, report, _ = self.compare(remove_operation)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])
        self.assertIn(
            ("api-removed-without-deprecation", "breaking"),
            [(c["id"], c["classification"]) for c in report["changes"]],
        )
        self.assert_names_nothing_from_main(report)

    def test_stale_branch_security_change_still_needs_permission_label(self):
        def allow_anonymous(spec):
            spec["paths"]["/widgets"]["post"]["security"] = []
            return spec

        result, report, _ = self.compare(allow_anonymous, ["contract-change"])
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["permission-change"])
        self.assertTrue(
            any(c["classification"] == "security" for c in report["changes"])
        )
        self.assert_names_nothing_from_main(report)
        self.assertEqual(
            self.run_diff(
                self.git.spec_at(self.head),
                ["permission-change"],
                base=description(),
            )[0].returncode,
            0,
        )

    def test_rebased_branch_removing_what_main_added_is_its_own_removal(self):
        self.git.commit(description())
        base_tip = self.git.commit(base_only_change(description()))
        self.git.branch("pr", base_tip)
        head = self.git.commit(description())
        step, resolved, _ = self.git.branch_point(base_tip, head)
        self.assertEqual(step.returncode, 0, step.stderr)
        self.assertEqual(resolved, base_tip)
        result, report, _ = self.run_diff(
            self.git.spec_at(head), base=self.git.spec_at(resolved)
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])
        self.assertIn("gadgets", json.dumps(report["changes"]).lower())


GATE_SCRIPTS_STEP = "Take the verdict scripts from the base tip"
VERDICT_COMPARATOR_STEP = "Install the comparator the verdict uses"
VERDICT_STEP = "Report contract changes and require deliberate labels"
HEAD_COMPARATOR_STEP = "Install the comparator the PR's tests use"
HEAD_TESTS_STEP = "Prove classification and label gates with the real comparator"
REAL_SCRIPT = (ROOT / "scripts/contract_diff.py").read_text()
# What a pull request could write to decide its own verdict.
ALWAYS_PASSES = "import sys\nsys.exit(0)\n"
A_REMOVAL = json.dumps(
    [{"id": "api-removed-without-deprecation", "level": 3, "text": "fixture removal"}]
)


def installer_of(comparator):
    """An oasdiff_pin.py that needs no network and installs the given shell."""
    return textwrap.dedent(
        f"""\
        import pathlib, sys
        target = pathlib.Path(sys.argv[2])
        target.mkdir(parents=True, exist_ok=True)
        (target / "oasdiff").write_text({comparator!r})
        (target / "oasdiff").chmod(0o755)
        """
    )


def reporting(changes):
    return f"#!/bin/sh\ncat <<'EOF'\n{changes}\nEOF\n"


class VerdictFixture(GitFixture):
    """Runs the job's own shell, in order, in a clone standing at the PR head."""

    def commit_scripts(self, script=None, installer=None):
        for name, text in (("contract_diff.py", script), ("oasdiff_pin.py", installer)):
            path = self.origin / "scripts" / name
            if text is not None:
                path.parent.mkdir(exist_ok=True)
                path.write_text(text)
                self.git("add", str(path))
        return self.commit(description())

    def env_for(self, base_tip, merge_base, head):
        binaries = self.temp / "bin"
        binaries.mkdir(exist_ok=True)
        github = binaries / "gh"
        github.write_text('#!/bin/sh\nprintf "%s\\n" "$VERDICT_LABELS"\n')
        github.chmod(0o755)
        return dict(
            self.env,
            PATH=str(binaries) + os.pathsep + self.env["PATH"],
            PR="1",
            GITHUB_REPOSITORY="fixture/repository",
            VERDICT_LABELS="",
            BASE_SHA=base_tip,
            MERGE_BASE=merge_base,
            HEAD_SHA=head,
            RUNNER_TEMP=str(self.temp),
            GITHUB_OUTPUT=str(self.temp / "output"),
            GITHUB_STEP_SUMMARY=str(self.temp / "summary"),
            GITHUB_EVENT_PATH=str(self.temp / "event.json"),
        )

    def run_step(self, shell, env):
        return subprocess.run(
            ["bash", "-c", shell],
            cwd=self.clone,
            env=env,
            capture_output=True,
            text=True,
            check=False,
            timeout=20,
        )

    def take_scripts(self, base_tip, head):
        """The steps up to the head's first code: where the verdict's copies come from."""
        (self.temp / "output").unlink(missing_ok=True)
        step, merge_base, _ = self.branch_point(base_tip, head)
        assert step.returncode == 0, step.stderr
        self.git("checkout", "--quiet", head, cwd=self.clone)
        env = self.env_for(base_tip, merge_base, head)
        return self.run_step(run_block(workflow_step(GATE_SCRIPTS_STEP)), env), env

    def verdict(
        self, base_tip, head, labels=(), base_spec=None, head_spec=None, event_labels=()
    ):
        taken, env = self.take_scripts(base_tip, head)
        assert taken.returncode == 0, taken.stderr
        installed = self.run_step(
            run_block(workflow_step(VERDICT_COMPARATOR_STEP)), env
        )
        assert installed.returncode == 0, installed.stderr
        (self.temp / "event.json").write_text(
            json.dumps(
                {"pull_request": {"labels": [{"name": n} for n in event_labels]}}
            )
        )
        env["VERDICT_LABELS"] = "\n".join(labels)
        for name, spec in (("base", base_spec), ("head", head_spec)):
            (self.temp / "contract-diff" / f"{name}.json").write_text(
                json.dumps(spec or description())
            )
        return self.run_step(run_block(workflow_step(VERDICT_STEP)), env)

    def report(self):
        return json.loads((self.temp / "contract-diff/changes.json").read_text())


class VerdictScripts(unittest.TestCase):
    """The verdict comes from copies the PR did not change (#1760). Needs git only."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.git = VerdictFixture(directory.name)
        self.installer = installer_of(reporting(A_REMOVAL))

    def base_with_gate(self):
        return self.git.commit_scripts(REAL_SCRIPT, self.installer)

    def assert_contract_label_required(self, result):
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertEqual(self.git.report()["missing_labels"], ["contract-change"])

    def test_a_head_that_rewrites_the_script_does_not_decide_its_verdict(self):
        base_tip = self.base_with_gate()
        self.git.branch("pr", base_tip)
        head = self.git.commit_scripts(ALWAYS_PASSES)
        self.assert_contract_label_required(self.git.verdict(base_tip, head))
        # The base's copy reads the labels, so a deliberate change can still land.
        self.assertEqual(
            self.git.verdict(base_tip, head, ["contract-change"]).returncode, 0
        )

    def test_a_removed_live_label_is_not_restored_by_the_old_event(self):
        base_tip = self.base_with_gate()
        self.git.branch("pr", base_tip)
        head = self.git.commit_scripts(ALWAYS_PASSES)
        self.assert_contract_label_required(
            self.git.verdict(base_tip, head, event_labels=["contract-change"])
        )

    def test_a_head_that_rewrites_the_installer_does_not_choose_the_comparator(self):
        base_tip = self.base_with_gate()
        self.git.branch("pr", base_tip)
        head = self.git.commit_scripts(installer=installer_of(reporting("[]")))
        self.assert_contract_label_required(self.git.verdict(base_tip, head))

    def test_a_stale_head_takes_the_rules_of_the_base_tip(self):
        # Base tip, not merge base: branching from an old commit picks no old rules,
        # and the arguments ci.yml passes always belong to the script it runs.
        start = self.git.commit_scripts(ALWAYS_PASSES, installer_of(reporting("[]")))
        base_tip = self.base_with_gate()
        self.git.branch("pr", start)
        head = self.git.commit(description())
        self.assert_contract_label_required(self.git.verdict(base_tip, head))

    def test_a_head_from_before_the_scripts_existed_still_gets_a_verdict(self):
        start = self.git.commit(description())
        base_tip = self.base_with_gate()
        self.git.branch("pr", start)
        head = self.git.commit(description())
        self.assert_contract_label_required(self.git.verdict(base_tip, head))

    def test_a_base_tip_without_the_scripts_fails_instead_of_using_the_head(self):
        base_tip = self.git.commit(description())
        self.git.branch("pr", base_tip)
        head = self.git.commit_scripts(ALWAYS_PASSES, self.installer)
        taken, _ = self.git.take_scripts(base_tip, head)
        self.assertNotEqual(taken.returncode, 0)
        self.assertIn(f"::error::{base_tip} has no scripts/", taken.stderr)

    def test_a_pr_changing_the_arguments_in_lockstep_fails_closed(self):
        # ci.yml comes from the merged ref, so it can name a flag the base's copy
        # lacks. The change lands in two PRs; the README says how.
        base_tip = self.base_with_gate()
        self.git.branch("pr", base_tip)
        head = self.git.commit_scripts(ALWAYS_PASSES)
        taken, env = self.git.take_scripts(base_tip, head)
        self.assertEqual(taken.returncode, 0, taken.stderr)
        self.git.run_step(run_block(workflow_step(VERDICT_COMPARATOR_STEP)), env)
        shell = run_block(workflow_step(VERDICT_STEP)).replace(
            "--output", "--pr-output"
        )
        result = self.git.run_step(shell, env)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("--pr-output", result.stderr)

    def test_the_job_passes_only_arguments_this_scripts_copy_accepts(self):
        # The contract job is skipped in the merge queue and judges a PR with the
        # base's copy, so nothing else notices a PR dropping a flag ci.yml still
        # passes, until it has failed every other PR. Lint runs this on the queue.
        base_tip = self.git.commit_scripts(REAL_SCRIPT, installer_of(reporting("[]")))
        self.git.branch("pr", base_tip)
        head = self.git.commit(description())
        result = self.git.verdict(base_tip, head)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.git.report(), {"changes": [], "missing_labels": []})

    def test_the_summary_names_the_copy_and_only_this_prs_changes_to_the_scripts(self):
        start = self.base_with_gate()
        base_tip = self.git.commit_scripts(REAL_SCRIPT + "\n# newer rules\n")
        self.git.branch("pr", start)
        unchanged = self.git.commit(description())
        changed = self.git.commit_scripts(REAL_SCRIPT + "\n# this PR's rules\n")
        summary = self.git.temp / "summary"

        taken, _ = self.git.take_scripts(base_tip, unchanged)
        self.assertEqual(taken.returncode, 0, taken.stderr)
        self.assertIn(base_tip, summary.read_text())
        self.assertNotIn("does not judge", summary.read_text())

        summary.unlink()
        taken, _ = self.git.take_scripts(base_tip, changed)
        self.assertEqual(taken.returncode, 0, taken.stderr)
        self.assertIn("scripts/contract_diff.py", summary.read_text())
        self.assertIn("does not judge", summary.read_text())

    def test_the_head_is_still_tested_and_runs_only_after_the_copies_are_taken(self):
        tests = workflow_step(HEAD_TESTS_STEP)
        self.assertIn("discover -s scripts/tests -p test_contract_diff.py", tests)
        self.assertIn("OASDIFF: ${{ runner.temp }}/bin/oasdiff", tests)
        self.assertIn(
            'python3 scripts/oasdiff_pin.py install "$RUNNER_TEMP/bin"',
            workflow_step(HEAD_COMPARATOR_STEP),
        )
        text = CI_WORKFLOW.read_text()
        order = [
            text.index(f"- name: {name}\n")
            for name in (
                BRANCH_POINT_STEP,
                GATE_SCRIPTS_STEP,
                VERDICT_COMPARATOR_STEP,
                HEAD_COMPARATOR_STEP,
                HEAD_TESTS_STEP,
                "Generate the base and head descriptions",
                VERDICT_STEP,
            )
        ]
        self.assertEqual(order, sorted(order))
        self.assertIn(
            "MERGE_BASE: ${{ steps.branch-point.outputs.sha }}",
            workflow_step(GATE_SCRIPTS_STEP),
        )


@unittest.skipUnless(
    BINARY, "OASDIFF names the pinned binary; contract-diff CI installs it"
)
class VerdictWithTheRealComparator(unittest.TestCase):
    """The whole path with real descriptions and a head that passes itself (#1760)."""

    def test_a_real_removal_needs_its_label_whatever_the_head_script_says(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        fixture = VerdictFixture(directory.name)
        base_tip = fixture.commit_scripts(
            REAL_SCRIPT,
            installer_of(f'#!/bin/sh\nexec "{Path(BINARY).resolve()}" "$@"\n'),
        )
        fixture.branch("pr", base_tip)
        head = fixture.commit_scripts(ALWAYS_PASSES)
        removed = description()
        del removed["paths"]["/widgets"]["post"]
        result = fixture.verdict(base_tip, head, head_spec=removed)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertEqual(fixture.report()["missing_labels"], ["contract-change"])


if __name__ == "__main__":
    unittest.main()
