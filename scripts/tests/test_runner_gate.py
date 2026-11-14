"""Every workflow asks runner-gate.antimatter-studios.com for its runners, the same way.

The antimatter-vps pool has four runners. A job that names that pool directly waits in
GitHub's queue until one is free and takes it from the jobs that asked for it, so no job
names it: an early job of each workflow asks the gate once per job (and per matrix leg),
and each job runs where it was told, or on GitHub's runners when there is no answer.
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
PICK_STEP = "Pick runners (antimatter-vps runners while free, else GitHub's)"
PICKER_OUTPUT = "${{ steps.runners.outputs.runners || '{}' }}"
GATED = re.compile(
    r"^\$\{\{ (?:(?P<guard>github\.event_name == 'pull_request') && )?"
    r"fromJSON\(needs\.(?P<picker>[\w-]+)\.outputs\.runners\)"
    r"\[(?:'(?P<key>[\w.-]+)'|format\('(?P<format>[\w-]+)\.\{0\}', matrix\.(?P<axis>\w+)\))\]"
    r" \|\| 'ubuntu-latest' \}\}$"
)
REUSABLE_INPUT = re.compile(r"^\$\{\{ inputs\.(?P<input>[\w-]+) \|\| 'ubuntu-latest' \}\}$")
CALLER_RUNNER = re.compile(
    r"^\$\{\{ fromJSON\(needs\.(?P<picker>[\w-]+)\.outputs\.runners\)\['(?P<key>[\w.-]+)'\] \|\| 'ubuntu-latest' \}\}$"
)

# Jobs that stay on GitHub's runners, and why. A job missing from here must be gated, so a
# new job is either gated or says here why it cannot be.
KEPT_HOSTED = {
    ("ci.yml", "preflight"): "picks the runners for the rest (bootstrap)",
    ("ci.yml", "ci"): "the required aggregate: cheap, always(), must never wait for a runner",
    ("e2e.yml", "preflight"): "the first job: nothing before it could pick for it",
    ("e2e.yml", "changes"): "picks the runners for the rest (bootstrap)",
    ("e2e.yml", "e2e"): "the required aggregate: cheap, always(), must never wait for a runner",
    ("e2e.yml", "e2e-proofs"): "waits for the whole suite: no job before it picks within the 120 s reservation",
    ("ci.yml", "build"): "waits for every test job: no job before it picks within the 120 s reservation",
    ("ci.yml", "test-report"): "waits for every test job: no job before it picks within the 120 s reservation",
    ("pulumi.yml", "preflight"): "picks the runners for the rest (bootstrap)",
    ("release-cli.yml", "preflight"): "picks the runners for the rest (bootstrap)",
    ("scheduled.yml", "dependency-review"): "picks the runners for the rest (bootstrap)",
    ("email-explained.yml", "preflight"): "the only other job uses docker buildx, so nothing to pick for",
    ("email-explained.yml", "publish"): "docker/build-push-action and buildx, which the pool cannot run",
    ("docker.yml", "build-and-push"): "docker/build-push-action and buildx, which the pool cannot run",
    ("cluster-e2e.yml", "install"): "kind needs a privileged Docker the pool's podman sidecar does not give",
    ("contract-compat.yml", "newest-main"): "the workflow's only job: no earlier job to pick in",
    ("contract-faults.yml", "finding-issues"): "its workflow has no early job to pick in",
    ("contract-fuzz.yml", "finding-issues"): "its workflow has no early job to pick in",
    ("bump-vertrag.yml", "bump"): "the workflow's only job: no earlier job to pick in",
    ("ci-labels.yml", "rerun-contract-gate"): "the workflow's only job: no earlier job to pick in",
    ("helm.yml", "publish"): "the workflow's only job: no earlier job to pick in",
    ("load.yml", "load"): "the workflow's only job: no earlier job to pick in",
    ("mail-conformance.yml", "conformance"): "the workflow's only job: no earlier job to pick in",
}
# Reusable-workflow calls that pass no runner: their workflow has no early job to pick in.
CALLS_KEPT_HOSTED = {
    ("contract-faults.yml", "faults"),
    ("contract-fuzz.yml", "fuzz"),
}
BUILDX = re.compile(r"docker/(?:setup-buildx-action|build-push-action)@|\bdocker buildx\b")


def workflows():
    for path in sorted(WORKFLOWS.glob("*.yml")):
        yield path.name, yaml.safe_load(path.read_text())


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
        for name, document in workflows():
            for job_id, job in document["jobs"].items():
                with self.subTest(workflow=name, job=job_id):
                    if "uses" in job:
                        self.assertNotIn("runs-on", job)
                        continue
                    runs_on = job["runs-on"]
                    if (name, job_id) in KEPT_HOSTED:
                        self.assertEqual(runs_on, HOSTED, KEPT_HOSTED[(name, job_id)])
                        continue
                    if "workflow_call" in document[True]:
                        match = REUSABLE_INPUT.match(runs_on)
                        self.assertIsNotNone(match, f"{runs_on!r} must come from an input, else ubuntu-latest")
                        self.assertIn(match["input"], document[True]["workflow_call"]["inputs"])
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

    def test_reusable_suite_callers_pass_the_runner_their_gate_picked(self):
        suite = yaml.safe_load((WORKFLOWS / "e2e-suite.yml").read_text())
        inputs = {}
        for job_id, job in suite["jobs"].items():
            inputs[REUSABLE_INPUT.match(job["runs-on"])["input"]] = job_id
        self.assertEqual(inputs, {"runner": "suite", "isolation-runner": "js-filter-isolation"})
        calls = 0
        for name, document in workflows():
            for job_id, job in document["jobs"].items():
                if job.get("uses") != "./.github/workflows/e2e-suite.yml":
                    continue
                calls += 1
                with self.subTest(workflow=name, job=job_id):
                    passed = {key: value for key, value in job.get("with", {}).items() if key in inputs}
                    if (name, job_id) in CALLS_KEPT_HOSTED:
                        self.assertEqual(passed, {})
                        continue
                    self.assertIn("runner", passed)
                    if job["with"].get("contract-phases", "gate") == "gate":
                        self.assertIn("isolation-runner", passed)
                    for input_name, value in passed.items():
                        match = CALLER_RUNNER.match(value)
                        self.assertIsNotNone(match, value)
                        self.assertIn(match["picker"], needs(job))
                        jobs = document["jobs"]
                        self.assertLessEqual(set(needs(job)), {match["picker"]} | ancestors(jobs, match["picker"]))
                        # Asked for by the job id inside e2e-suite.yml: GITHUB_JOB on the runner.
                        self.assertEqual(match["key"].split(".", 1)[0], inputs[input_name])
                        step = pick_step(document["jobs"][match["picker"]])
                        self.assertIn(match["key"], entries(step))
        self.assertGreaterEqual(calls, 6)

    def test_picking_jobs_follow_the_gate_contract(self):
        pickers = 0
        for name, document in workflows():
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
                        "$ours || { echo ubuntu-latest; return; }",
                        "curl -fsS --max-time 10 --get https://runner-gate.antimatter-studios.com/pick",
                        "--data-urlencode fallback=ubuntu-latest",
                        '--data-urlencode "run=${{ github.run_id }}"',
                        '--data-urlencode "attempt=${{ github.run_attempt }}"',
                        '--data-urlencode "job=$1") || r=ubuntu-latest',
                        'case "$r" in antimatter-vps-linux-x64|ubuntu-latest) echo "$r" ;; *) echo ubuntu-latest ;; esac',
                        'pick "${entry%%.*}"',
                        'echo "runners=$runners" >> "$GITHUB_OUTPUT"',
                    ):
                        self.assertIn(fragment, script)
                    self.assertEqual(len(entries(step)), len(set(entries(step))), "each entry picks once")
        self.assertEqual(pickers, 6)

    def test_every_entry_picks_for_a_job_that_reads_it(self):
        for name, document in workflows():
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
                            self.assertIn(f"outputs.runners)['{entry}']", text)

    def test_buildx_jobs_stay_on_github(self):
        found = 0
        for name, document in workflows():
            for job_id, job in document["jobs"].items():
                if uses_buildx(job):
                    found += 1
                    with self.subTest(workflow=name, job=job_id):
                        self.assertEqual(job["runs-on"], HOSTED)
        self.assertGreaterEqual(found, 2)


class GatedJobsBringTheirTools(unittest.TestCase):
    """The pool's stock runner has git, curl, jq, python3 and Docker, and little else."""

    def gated_jobs(self):
        for name, document in workflows():
            for job_id, job in document["jobs"].items():
                if "uses" in job or job["runs-on"] == HOSTED:
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
        counts = dict.fromkeys(("gh", "python", "cgo", "pnpm", "yq"), 0)
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
    """Runs ci.yml's pick step with a stand-in curl."""

    def run_step(self, answer, identities, jobs="test test-api.1 test-api.2"):
        step = pick_step(yaml.safe_load((WORKFLOWS / "ci.yml").read_text())["jobs"]["preflight"])
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
                         f"prefer={POOL}", "fallback=ubuntu-latest", "--max-time 10"):
                self.assertIn(part, call)

    def test_an_unexpected_answer_or_a_failure_falls_back(self):
        for answer in ("echo self-hosted", "echo '<html>'", "exit 22", "exit 28"):
            with self.subTest(answer=answer):
                runners, _ = self.run_step(answer, self.OURS)
                self.assertEqual(set(runners.values()), {HOSTED})

    def test_anyone_else_on_the_run_never_asks_the_gate(self):
        for who in ("ACTOR", "GH_ACTOR", "SENDER", "AUTHOR"):
            for other in ("github-merge-queue[bot]", "someone-else"):
                with self.subTest(identity=who, value=other):
                    runners, calls = self.run_step("echo antimatter-vps-linux-x64", {**self.OURS, who: other})
                    self.assertEqual(set(runners.values()), {HOSTED})
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
