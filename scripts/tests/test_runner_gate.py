"""Every job that can run on the antimatter-vps pool asks the runner gate where to run.

The antimatter-vps pool has four runners. A job that names that pool directly waits in
GitHub's queue until one is free and takes it from the jobs that asked for it, so no job
names it.

CI, E2E and the E2E suite (GATED_WORKFLOWS) give each such job a `<job>-gate` job just
before it, on the gate's own runner, which answers `runner-gate pick <class>` with a label:
a pool runner while one is free, otherwise GitHub's. The other workflows still pick once,
in an early job, through runner-gate.antimatter-studios.com, until they move to the gate
jobs too.
"""

import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github/workflows"
POOL = "antimatter-vps-linux-x64"
HOSTED = "ubuntu-latest"
# Where every gate job runs, and the workflows whose jobs ask it.
GATE_LABEL = "antimatter-vps-gate"
GATED_WORKFLOWS = {"ci.yml", "e2e.yml", "e2e-suite.yml"}
# The class a gate asks for, by what the job ran on before it was gated.
CLASSES = {
    "ubuntu-latest": "linux-x64", "ubuntu-24.04": "linux-x64", "ubuntu-24.04-arm": "linux-arm64",
    "macos-15": "macos-arm64", "macos-latest": "macos-arm64",
    "windows-latest": "windows-x64", "windows-11-arm": "windows-arm64",
}
# What only a Linux job that needs KVM does: such a job asks for linux-x64-kvm.
NEEDS_KVM = re.compile(r"\bmodprobe\b|\binsmod\b|/dev/kvm|\bqemu|\bvagrant\b|\bzfs\b", re.IGNORECASE)
# A condition with one of these runs after a job it needs failed, or was skipped.
STATUS_CHECK = re.compile(r"\b(?:always|cancelled|failure)\(\)")
GATE_OUTPUT = "${{ steps.pick.outputs.runner }}"
# What a gated job falls back to: the Ubuntu the pool runs (24.04), not whatever
# ubuntu-latest has become, so a job behaves the same wherever it lands.
FALLBACK = "ubuntu-24.04"
PICK_STEP = "Pick runners (antimatter-vps runners while free, else GitHub's)"
PICKER_OUTPUT = "${{ steps.runners.outputs.runners || '{}' }}"
PICKED = (
    r"fromJSON\(needs\.(?P<picker>[\w-]+)\.outputs\.runners \|\| '\{\}'\)"
    r"\[(?:'(?P<key>[\w.-]+)'|format\('(?P<format>[\w-]+)\.\{0\}', matrix\.(?P<axis>\w+)\))\]"
    r" \|\| 'ubuntu-24\.04'"
)
GATED = re.compile(
    # A tag's release stays on GitHub's own runners; only a pull request's build is picked.
    r"^\$\{\{ (?:(?P<guard>github\.event_name == 'pull_request') && \()?" + PICKED
    + r"(?(guard)\) \|\| 'ubuntu-latest') \}\}$"
)

# Jobs of the picker workflows that stay on GitHub's runners, and why. A job missing from
# here must be picked for, so a new job is either picked for or says here why it cannot be.
KEPT_HOSTED = {
    ("pulumi.yml", "preflight"): "picks the runners for the rest (bootstrap)",
    ("release-cli.yml", "preflight"): "picks the runners for the rest (bootstrap)",
    ("scheduled.yml", "dependency-review"): "picks the runners for the rest (bootstrap)",
    ("email-explained.yml", "preflight"): "the only other job uses docker buildx, so nothing to pick for",
    ("email-explained.yml", "publish"): "docker/build-push-action and buildx, which the pool cannot run",
    ("docker.yml", "build-and-push"): "docker/build-push-action and buildx, which the pool cannot run",
    ("docker.yml", "release-evidence"): "buildx and a live Docker topology, kept off the memory-constrained pool",
    ("docker.yml", "promote-release-images"): "buildx copies the authorized multi-platform indices",
    ("cluster-e2e.yml", "install"): "kind needs a privileged Docker the pool's podman sidecar does not give; it picks for the rest",
    ("contract-compat.yml", "newest-main"): "the workflow's only job: no earlier job to pick in",
    ("contract-faults.yml", "finding-issues"): "its workflow has no early job to pick in",
    ("contract-fuzz.yml", "finding-issues"): "its workflow has no early job to pick in",
    ("bump-vertrag.yml", "bump"): "the workflow's only job: no earlier job to pick in",
    ("ci-labels.yml", "rerun-contract-gate"): "the workflow's only job: no earlier job to pick in",
    ("ci-retarget.yml", "dispatch-required-checks"): "the workflow's only job: no earlier job to pick in",
    ("ci-retry.yml", "retry"): "the workflow's only job, and it must answer when the pool is down",
    ("helm.yml", "publish"): "the workflow's only job: no earlier job to pick in",
    ("load.yml", "load"): "the workflow's only job: no earlier job to pick in",
    ("mail-conformance.yml", "conformance"): "the workflow's only job: no earlier job to pick in",
}
BUILDX = re.compile(r"docker/(?:setup-buildx-action|build-push-action)@|\bdocker buildx\b")


def workflows():
    for path in sorted(WORKFLOWS.glob("*.yml")):
        yield path.name, yaml.safe_load(path.read_text())


def picker_workflows():
    """The workflows that still pick their runners in an early job."""
    for name, document in workflows():
        if name not in GATED_WORKFLOWS:
            yield name, document


def gated_workflows():
    for name, document in workflows():
        if name in GATED_WORKFLOWS:
            yield name, document


def is_gate(job):
    return job.get("runs-on") == GATE_LABEL


def needs(job):
    value = job.get("needs", [])
    return [value] if isinstance(value, str) else list(value)


def pick_step(job):
    steps = job.get("steps", [])
    return steps[-1] if steps and steps[-1].get("id") == "runners" else None


def entries(step):
    """JOBS's entries, with each `${{ cond && 'a b' || '' }}` taken as present."""
    text = step["env"]["JOBS"]
    text = re.sub(r"\$\{\{[^}]*'([^']*)' \|\| '' \}\}", r"\1", text)
    return text.split()


def uses_buildx(job):
    return any(BUILDX.search(step.get("uses", "") + "\n" + step.get("run", "")) for step in job.get("steps", []))


def ancestors(jobs, job_id):
    seen = set()
    stack = needs(jobs[job_id])
    while stack:
        name = stack.pop()
        if name not in seen:
            seen.add(name)
            stack.extend(needs(jobs[name]))
    return seen


def matrix_values(job, axis):
    return [str(value) for value in job["strategy"]["matrix"][axis]]


class RunnerGate(unittest.TestCase):
    def test_no_job_names_the_pool(self):
        for name, document in workflows():
            # Only a pick step may name it: as the label it prefers, and one it accepts.
            self.assertNotIn(POOL, json.dumps({key: value for key, value in document.items() if key != "jobs"}))
            for job_id, job in document["jobs"].items():
                with self.subTest(workflow=name, job=job_id):
                    rest = dict(job, steps=[step for step in job.get("steps", []) if step is not pick_step(job)])
                    self.assertNotIn(POOL, json.dumps(rest))

    def test_every_job_runs_where_the_gate_said_or_on_github(self):
        for name, document in picker_workflows():
            for job_id, job in document["jobs"].items():
                with self.subTest(workflow=name, job=job_id):
                    if "uses" in job:
                        self.assertNotIn("runs-on", job)
                        continue
                    runs_on = job["runs-on"]
                    if (name, job_id) in KEPT_HOSTED:
                        self.assertEqual(runs_on, HOSTED, KEPT_HOSTED[(name, job_id)])
                        continue
                    match = GATED.match(runs_on)
                    self.assertIsNotNone(match, f"{runs_on!r} is neither gated nor listed in KEPT_HOSTED")
                    self.assertIn(match["picker"], needs(job), "the picking job must be one this job needs")
                    # The gate holds a runner for 120 s from the pick. Everything this job waits
                    # for has finished by the time its picker does, so it starts straight after.
                    jobs = document["jobs"]
                    self.assertLessEqual(
                        set(needs(job)), {match["picker"]} | ancestors(jobs, match["picker"]),
                        "this job waits for a job its picker does not: pick in the job directly before it",
                    )
                    picker = document["jobs"][match["picker"]]
                    step = pick_step(picker)
                    self.assertIsNotNone(step, f"{match['picker']} has no pick step")
                    if match["key"]:
                        expected = [match["key"]]
                    else:
                        self.assertEqual(match["format"], job_id)
                        expected = [f"{job_id}.{value}" for value in matrix_values(job, match["axis"])]
                    for key in expected:
                        # The gate is asked for the job's own id: the part before any ".".
                        self.assertEqual(key.split(".", 1)[0], job_id)
                        self.assertIn(key, entries(step), f"{match['picker']} does not pick for {key}")
                    if "strategy" in job and match["key"]:
                        self.fail("a matrix job needs one pick per leg, not one shared")

    def test_the_suite_takes_no_runner_and_no_caller_passes_one(self):
        # Its jobs ask their own gates, so a caller has nothing to pick for them.
        suite = yaml.safe_load((WORKFLOWS / "e2e-suite.yml").read_text())
        inputs = suite[True]["workflow_call"]["inputs"]
        self.assertFalse({"runner", "isolation-runner"} & set(inputs))
        self.assertFalse([key for key in inputs if "runner" in key])
        calls = 0
        for name, document in workflows():
            for job_id, job in document["jobs"].items():
                if job.get("uses") != "./.github/workflows/e2e-suite.yml":
                    continue
                calls += 1
                with self.subTest(workflow=name, job=job_id):
                    self.assertFalse([key for key in job.get("with", {}) if "runner" in key])
                    self.assertNotIn("runners", json.dumps(job))
        self.assertGreaterEqual(calls, 7)

    def test_picking_jobs_follow_the_gate_contract(self):
        pickers = 0
        for name, document in picker_workflows():
            for job_id, job in document["jobs"].items():
                step = pick_step(job)
                if step is None:
                    self.assertNotIn("runners.outputs", json.dumps(job.get("outputs", {})))
                    continue
                pickers += 1
                with self.subTest(workflow=name, job=job_id):
                    # The first picker of a workflow is the bootstrap, on GitHub's runners. A later
                    # one (Lint, for the job that waits for it) runs wherever it was itself sent.
                    if not needs(job):
                        self.assertEqual(job["runs-on"], HOSTED, "the first picking job is the bootstrap")
                    else:
                        self.assertTrue(job["runs-on"] == HOSTED or GATED.match(job["runs-on"]))
                    self.assertEqual(job["outputs"]["runners"], PICKER_OUTPUT)
                    self.assertEqual(step["name"], PICK_STEP)
                    self.assertEqual(
                        {key: step["env"][key] for key in ("ORG", "ACTOR", "AUTHOR", "SENDER", "GH_ACTOR")},
                        {
                            "ORG": "${{ github.repository_owner }}",
                            "ACTOR": "${{ github.triggering_actor }}",
                            "AUTHOR": "${{ github.event.pull_request.user.login }}",
                            "SENDER": "${{ github.event.sender.login }}",
                            "GH_ACTOR": "${{ github.actor }}",
                        },
                    )
                    script = step["run"]
                    for fragment in (
                        'for who in "$ACTOR" "$GH_ACTOR" "$SENDER" "$AUTHOR"; do',
                        '[ -z "$who" ] || [ "$who" = christhomas ] || ours=false',
                        "$ours || { echo ubuntu-24.04; return; }",
                        "curl -fsS --max-time 10 --get https://runner-gate.antimatter-studios.com/pick",
                        "--data-urlencode fallback=ubuntu-24.04",
                        '--data-urlencode "run=${{ github.run_id }}"',
                        '--data-urlencode "attempt=${{ github.run_attempt }}"',
                        '--data-urlencode "job=$1") || r=ubuntu-24.04',
                        'case "$r" in antimatter-vps-linux-x64|ubuntu-24.04) echo "$r" ;; *) echo ubuntu-24.04 ;; esac',
                        'pick "${entry%%.*}"',
                        'echo "runners=$runners" >> "$GITHUB_OUTPUT"',
                    ):
                        self.assertIn(fragment, script)
                    self.assertEqual(len(entries(step)), len(set(entries(step))), "each entry picks once")
        self.assertEqual(pickers, 4)

    def test_every_entry_picks_for_a_job_that_reads_it(self):
        for name, document in picker_workflows():
            text = (WORKFLOWS / name).read_text()
            for job_id, job in document["jobs"].items():
                step = pick_step(job)
                if step is None:
                    continue
                for entry in entries(step):
                    with self.subTest(workflow=name, entry=entry):
                        base, _, leg = entry.partition(".")
                        if base in document["jobs"] and "strategy" in document["jobs"][base]:
                            self.assertIn(f"format('{base}.{{0}}', matrix.", text)
                        else:
                            self.assertIn(f"outputs.runners || '{{}}')['{entry}']", text)

    def test_buildx_jobs_stay_on_github(self):
        found = 0
        for name, document in workflows():
            if name in GATED_WORKFLOWS:
                # No gated job uses buildx; GateJobs checks it stays that way.
                continue
            for job_id, job in document["jobs"].items():
                if uses_buildx(job):
                    found += 1
                    with self.subTest(workflow=name, job=job_id):
                        self.assertEqual(job["runs-on"], HOSTED)
        self.assertGreaterEqual(found, 2)


class GateJobs(unittest.TestCase):
    """CI, E2E and the suite: one `<job>-gate` per pooled job, in the shape the gate accepts."""

    def pooled(self):
        """Each job that runs where a gate says, with its gate."""
        for name, document in gated_workflows():
            jobs = document["jobs"]
            for job_id, job in jobs.items():
                if is_gate(job) or "uses" in job:
                    continue
                yield name, jobs, job_id, job, jobs.get(f"{job_id}-gate")

    def test_every_job_that_can_run_on_the_pool_has_a_gate_just_before_it(self):
        count = 0
        for name, jobs, job_id, job, gate in self.pooled():
            count += 1
            with self.subTest(workflow=name, job=job_id):
                self.assertIsNotNone(gate, f"{job_id} has no {job_id}-gate")
                order = list(jobs)
                self.assertEqual(order.index(f"{job_id}-gate") + 1, order.index(job_id))
        self.assertGreaterEqual(count, 28)

    def test_every_gate_job_is_named_for_the_job_it_gates(self):
        # The gate refuses any other name: `gate`, or `<job>-gate` for a job beside it.
        gates = 0
        for name, document in gated_workflows():
            jobs = document["jobs"]
            for job_id, job in jobs.items():
                if not is_gate(job):
                    self.assertFalse(job_id.endswith("-gate") or job_id == "gate", f"{job_id} is named as a gate")
                    continue
                gates += 1
                with self.subTest(workflow=name, gate=job_id):
                    self.assertTrue(job_id.endswith("-gate"))
                    gated = job_id.removesuffix("-gate")
                    self.assertIn(gated, jobs)
                    self.assertFalse(is_gate(jobs[gated]))
        self.assertGreaterEqual(gates, 28)

    def test_every_gate_has_the_shape_the_gate_accepts_and_no_secrets(self):
        for name, jobs, job_id, job, gate in self.pooled():
            with self.subTest(workflow=name, job=job_id):
                self.assertLessEqual(set(gate), {"needs", "if", "runs-on", "timeout-minutes", "outputs", "steps"})
                self.assertEqual(gate["runs-on"], GATE_LABEL)
                self.assertEqual(gate["timeout-minutes"], 1)
                self.assertEqual(gate["outputs"], {"runner": GATE_OUTPUT})
                self.assertEqual(len(gate["steps"]), 1)
                self.assertEqual(set(gate["steps"][0]), {"id", "run"})
                self.assertEqual(gate["steps"][0]["id"], "pick")
                self.assertRegex(gate["steps"][0]["run"], r"^runner-gate pick [\w-]+$")
                text = json.dumps(gate)
                for secret in ("secrets.", "github.token", "GITHUB_TOKEN", "_TOKEN"):
                    self.assertNotIn(secret, text)

    def test_every_gate_waits_for_and_runs_when_its_job_would(self):
        for name, jobs, job_id, job, gate in self.pooled():
            with self.subTest(workflow=name, job=job_id):
                self.assertEqual(needs(gate) + [f"{job_id}-gate"], needs(job))
                # The same condition: a gate never reserves a runner for a job that skips,
                # and a job that runs after failures has a gate that does too.
                self.assertEqual(gate.get("if"), job.get("if"))
                if "always()" in job.get("if", ""):
                    self.assertIn("always()", gate["if"])

    def test_every_gate_asks_for_the_class_its_job_needs(self):
        for name, jobs, job_id, job, gate in self.pooled():
            with self.subTest(workflow=name, job=job_id):
                asked = gate["steps"][0]["run"].removeprefix("runner-gate pick ")
                self.assertIn(asked, set(CLASSES.values()) | {"linux-x64-kvm"})
                steps = json.dumps(job.get("steps", []))
                if NEEDS_KVM.search(steps):
                    self.assertEqual(asked, "linux-x64-kvm", "a job that needs KVM must not be linux-x64")
                else:
                    # Every pooled job here was an Ubuntu x64 job, and none needs KVM.
                    self.assertEqual(asked, "linux-x64")

    def test_every_job_runs_where_its_own_gate_answered(self):
        for name, jobs, job_id, job, gate in self.pooled():
            with self.subTest(workflow=name, job=job_id):
                answer = f"needs.{job_id}-gate.outputs.runner"
                # A job that runs after failures runs even when its gate gave no answer.
                if STATUS_CHECK.search(job.get("if", "")):
                    self.assertEqual(job["runs-on"], f"${{{{ {answer} || '{FALLBACK}' }}}}")
                else:
                    self.assertEqual(job["runs-on"], f"${{{{ {answer} }}}}")

    def test_the_required_verdicts_run_after_failures_and_fall_back(self):
        found = set()
        for name, jobs, job_id, job, gate in self.pooled():
            if job.get("if", "").startswith("always()"):
                found.add((name, job_id))
                with self.subTest(workflow=name, job=job_id):
                    self.assertTrue(gate["if"].startswith("always()"))
                    self.assertTrue(job["runs-on"].endswith(f" || '{FALLBACK}' }}}}"))
        self.assertLessEqual({("ci.yml", "ci"), ("e2e.yml", "e2e"), ("e2e.yml", "e2e-proofs")}, found)

    def test_a_matrix_job_has_one_gate_for_every_leg(self):
        matrices = 0
        for name, jobs, job_id, job, gate in self.pooled():
            if "strategy" not in job:
                continue
            matrices += 1
            with self.subTest(workflow=name, job=job_id):
                self.assertNotIn("matrix", job["runs-on"])
                self.assertNotIn("strategy", gate)
        self.assertGreaterEqual(matrices, 3)

    def test_no_old_picker_remains(self):
        for name in GATED_WORKFLOWS:
            text = (WORKFLOWS / name).read_text()
            document = yaml.safe_load(text)
            with self.subTest(workflow=name):
                self.assertNotIn(PICK_STEP, text)
                self.assertNotIn("runner-gate.antimatter-studios.com", text)
                self.assertNotIn("steps.runners", text)
                self.assertNotRegex(text, r"fromJSON\([^)]*runners")
                self.assertNotRegex(text, r"(?m)^\s+(?:ORG|ACTOR|AUTHOR|SENDER|GH_ACTOR|JOBS):")
                for job_id, job in document["jobs"].items():
                    self.assertNotIn("runners", job.get("outputs", {}), job_id)
                    self.assertFalse(uses_buildx(job), f"{job_id} uses buildx, which the pool cannot run")

    def test_every_gated_workflow_job_is_pooled_or_a_call(self):
        # Every runs-on here fits a class, so nothing is left on a fixed label.
        for name, document in gated_workflows():
            for job_id, job in document["jobs"].items():
                with self.subTest(workflow=name, job=job_id):
                    if "uses" in job:
                        self.assertNotIn("runs-on", job)
                    elif not is_gate(job):
                        self.assertIn(f"{job_id}-gate", job["runs-on"])


class GatedJobsBringTheirTools(unittest.TestCase):
    """The pool's stock runner has git, curl, jq, python3 and Docker, and little else."""

    def gated_jobs(self):
        for name, document in workflows():
            for job_id, job in document["jobs"].items():
                if "uses" in job or job["runs-on"] in (HOSTED, GATE_LABEL):
                    continue
                yield name, job_id, job

    def first(self, steps, predicate):
        return next((index for index, step in enumerate(steps) if predicate(step)), None)

    def assert_before(self, steps, setup, consumer, message):
        use = self.first(steps, consumer)
        if use is None:
            return False
        ready = self.first(steps, setup)
        self.assertIsNotNone(ready, message)
        self.assertLess(ready, use, message)
        return True

    def test_tools_are_installed_before_their_first_use(self):
        counts = dict.fromkeys(("gh", "python", "cgo", "pnpm", "yq", "promtool"), 0)
        for name, job_id, job in self.gated_jobs():
            steps = job["steps"]
            with self.subTest(workflow=name, job=job_id):
                counts["gh"] += self.assert_before(
                    steps,
                    lambda step: step.get("uses") == "./.github/actions/setup-gh",
                    lambda step: re.search(r"(?m)(?:^\s*|[;&|(]\s*|\$\()gh\s", step.get("run", ""))
                    or "scripts/ci_example_proofs.sh" in step.get("run", ""),
                    "gh is used before it is installed",
                )
                counts["python"] += self.assert_before(
                    steps,
                    lambda step: step.get("uses") == "./.github/actions/setup-python",
                    lambda step: "python3" in step.get("run", "") or step.get("uses") == "./.github/actions/cache-pinned-tool",
                    "python3 is used before setup-python",
                )
                counts["cgo"] += self.assert_before(
                    steps,
                    lambda step: step.get("uses") == "./.github/actions/setup-cgo",
                    lambda step: re.search(r"go test -race\b", step.get("run", "")),
                    "the race detector needs a C compiler",
                )
                counts["pnpm"] += self.assert_before(
                    steps,
                    lambda step: step.get("uses", "").startswith("actions/setup-node@"),
                    lambda step: "corepack" in step.get("run", ""),
                    "corepack needs the Node setup-node installs",
                )
                counts["yq"] += self.assert_before(
                    steps,
                    lambda step: "scripts/ci/install-yq" in step.get("run", ""),
                    lambda step: "render-test.sh" in step.get("run", "") or "dane-resolver-test.sh" in step.get("run", ""),
                    "the chart's tests need yq",
                )
                counts["promtool"] += self.assert_before(
                    steps,
                    lambda step: "scripts/ci/install-promtool" in step.get("run", ""),
                    lambda step: "render-test.sh" in step.get("run", ""),
                    "the chart's alert tests need promtool",
                )
        for tool, count in counts.items():
            with self.subTest(tool=tool):
                self.assertGreater(count, 0, f"no gated job uses {tool}: the check above proves nothing")

    def test_corepack_comes_from_npm_after_node(self):
        for name, job_id, job in self.gated_jobs():
            for step in job["steps"]:
                if "corepack enable" in step.get("run", ""):
                    with self.subTest(workflow=name, job=job_id):
                        self.assertIn("npm install --global corepack@0.34.7", step["run"])

    def test_setup_gh_installs_a_gh_new_enough_for_attestations(self):
        action = yaml.safe_load((ROOT / ".github/actions/setup-gh/action.yml").read_text())
        script = action["runs"]["steps"][0]["run"]
        self.assertIn("command -v gh", script)
        self.assertIn("https://cli.github.com/packages", script)
        self.assertIn("githubcli-archive-keyring.gpg", script)


@unittest.skipUnless(shutil.which("jq") and shutil.which("bash"), "needs bash and jq")
class PickStepBehaviour(unittest.TestCase):
    """Runs pulumi.yml's pick step, the same as every picker's, with a stand-in curl."""

    def run_step(self, answer, identities, jobs="test test-api.1 test-api.2"):
        step = pick_step(yaml.safe_load((WORKFLOWS / "pulumi.yml").read_text())["jobs"]["preflight"])
        script = step["run"].replace("${{ github.run_id }}", "42").replace("${{ github.run_attempt }}", "1")
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            curl = directory / "curl"
            curl.write_text(
                "#!/usr/bin/env bash\n"
                'printf \'%s\\n\' "$*" >> "$CALLS"\n'
                f"{answer}\n"
            )
            curl.chmod(curl.stat().st_mode | stat.S_IXUSR)
            env = dict(
                os.environ,
                PATH=f"{directory}:{os.environ['PATH']}",
                CALLS=str(directory / "calls"),
                GITHUB_OUTPUT=str(directory / "output"),
                GITHUB_STEP_SUMMARY=str(directory / "summary"),
                ORG="rest-mail",
                JOBS=jobs,
                **identities,
            )
            subprocess.run(["bash", "-c", script], env=env, check=True, capture_output=True, text=True)
            output = (directory / "output").read_text().strip()
            calls = (directory / "calls").read_text().splitlines() if (directory / "calls").exists() else []
        self.assertTrue(output.startswith("runners="))
        return json.loads(output[len("runners="):]), calls

    OURS = {"ACTOR": "christhomas", "GH_ACTOR": "christhomas", "SENDER": "christhomas", "AUTHOR": "christhomas"}

    def test_an_answer_from_the_gate_is_used_per_entry_and_asked_by_job_id(self):
        runners, calls = self.run_step("echo antimatter-vps-linux-x64", self.OURS)
        self.assertEqual(runners, {"test": POOL, "test-api.1": POOL, "test-api.2": POOL})
        self.assertEqual(len(calls), 3)
        self.assertIn("job=test-api", calls[1])
        self.assertNotIn("job=test-api.1", calls[1])
        for call in calls:
            for part in ("run=42", "attempt=1", "org=rest-mail", "actor=christhomas", "author=christhomas",
                         f"prefer={POOL}", "fallback=ubuntu-24.04", "--max-time 10"):
                self.assertIn(part, call)

    def test_an_unexpected_answer_or_a_failure_falls_back(self):
        for answer in ("echo self-hosted", "echo '<html>'", "echo ubuntu-latest", "exit 22", "exit 28"):
            with self.subTest(answer=answer):
                runners, _ = self.run_step(answer, self.OURS)
                self.assertEqual(set(runners.values()), {FALLBACK})

    def test_anyone_else_on_the_run_never_asks_the_gate(self):
        for who in ("ACTOR", "GH_ACTOR", "SENDER", "AUTHOR"):
            for other in ("github-merge-queue[bot]", "someone-else"):
                with self.subTest(identity=who, value=other):
                    runners, calls = self.run_step("echo antimatter-vps-linux-x64", {**self.OURS, who: other})
                    self.assertEqual(set(runners.values()), {FALLBACK})
                    self.assertEqual(calls, [])

    def test_a_missing_identity_does_not_count_against_the_run(self):
        runners, calls = self.run_step("echo antimatter-vps-linux-x64", {**self.OURS, "AUTHOR": "", "SENDER": ""})
        self.assertEqual(set(runners.values()), {POOL})
        self.assertEqual(len(calls), 3)

    def test_no_entries_give_an_empty_map(self):
        runners, calls = self.run_step("echo antimatter-vps-linux-x64", self.OURS, jobs="")
        self.assertEqual((runners, calls), ({}, []))


if __name__ == "__main__":
    unittest.main()
