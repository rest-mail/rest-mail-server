"""CI survives a registry hiccup, and only that (#1847)."""

import fnmatch
import os
import re
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SHIM = ROOT / "scripts" / "ci" / "docker-retry" / "docker"
WORKFLOWS = ROOT / ".github" / "workflows"
ENABLE_STEP = 'echo "$GITHUB_WORKSPACE/scripts/ci/docker-retry" >> "$GITHUB_PATH"'

HUB_EOF = (
    "docker: Error response from daemon: "
    'Head "https://registry-1.docker.io/v2/library/postgres/manifests/16-alpine": '
    'Get "https://auth.docker.io/token?scope=x": EOF'
)
BUILD_EOF = (
    "ERROR: failed to build: failed to solve: failed to authorize: "
    'failed to fetch oauth token: Post "https://auth.docker.io/token": EOF'
)

# A stand-in for the real docker: each call takes the next "<status> <stderr>"
# line from $SCRIPT, records its arguments and stdin, and prints to stdout.
FAKE_DOCKER = """#!/usr/bin/env bash
n=$(cat "$COUNT" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$COUNT"
echo "$*" >> "$CALLS"
if [ ! -t 0 ] && [ "${READ_STDIN:-}" = 1 ]; then cat >> "$CALLS"; fi
echo "stdout of call $n"
line=$(sed -n "${n}p" "$SCRIPT")
[ -n "$line" ] || line=$(tail -n 1 "$SCRIPT")
message=${line#* }
[ "$message" = "$line" ] || echo "$message" >&2
exit "${line%% *}"
"""


class DockerRetryTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)
        fake = self.dir / "docker"
        fake.write_text(FAKE_DOCKER)
        fake.chmod(fake.stat().st_mode | stat.S_IEXEC)

    def docker(self, script, *args, stdin="", env=None):
        """Run the shim against the fake, which replies with `script` in order."""
        (self.dir / "script").write_text("\n".join(script) + "\n")
        result = subprocess.run(
            [str(SHIM), *args],
            input=stdin,
            text=True,
            capture_output=True,
            check=False,
            env={
                "PATH": f"{SHIM.parent}:{self.dir}:{os.environ['PATH']}",
                "SCRIPT": str(self.dir / "script"),
                "COUNT": str(self.dir / "count"),
                "CALLS": str(self.dir / "calls"),
                "DOCKER_RETRY_DELAYS": "0 0 0",
                **(env or {}),
            },
        )
        calls = self.dir / "calls"
        return result, calls.read_text().splitlines() if calls.exists() else []

    def test_the_seen_docker_hub_failure_is_retried(self):
        result, calls = self.docker(
            [f"125 {HUB_EOF}", "0"], "run", "-d", "postgres:16-alpine"
        )
        self.assertEqual(result.returncode, 0)
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[1], "run -d postgres:16-alpine")
        self.assertIn(HUB_EOF, result.stderr)
        self.assertIn("transient registry failure", result.stderr)
        self.assertEqual(result.stdout, "stdout of call 1\nstdout of call 2\n")

    def test_each_transient_form_is_retried(self):
        forms = {
            "EOF": 'Error response from daemon: Get "https://ghcr.io/v2/": EOF',
            "TLS": 'Error response from daemon: Get "https://ghcr.io/v2/": '
            "net/http: TLS handshake timeout",
            "timeout": 'Error response from daemon: Get "https://ghcr.io/v2/": '
            "net/http: request canceled while waiting for connection "
            "(Client.Timeout exceeded while awaiting headers)",
            "reset": 'Error response from daemon: Get "https://ghcr.io/v2/": '
            "read tcp 1.2.3.4:5->6.7.8.9:443: read: connection reset by peer",
            "dns": 'Error response from daemon: Get "https://ghcr.io/v2/": '
            "dial tcp: lookup ghcr.io: Temporary failure in name resolution",
            "503": "Error response from daemon: received unexpected HTTP status: "
            "503 Service Unavailable",
        }
        for name, message in forms.items():
            with self.subTest(name):
                (self.dir / "count").write_text("0")
                (self.dir / "calls").write_text("")
                result, calls = self.docker([f"1 {message}", "0"], "pull", "x")
                self.assertEqual(result.returncode, 0)
                self.assertEqual(len(calls), 2)

    def test_a_build_retries_a_failed_base_image_fetch(self):
        result, calls = self.docker([f"1 {BUILD_EOF}", "0"], "build", "-t", "x", ".")
        self.assertEqual(result.returncode, 0)
        self.assertEqual(len(calls), 2)

    def test_a_failure_that_is_not_transient_is_not_retried(self):
        for name, script, args in (
            (
                "missing image",
                (
                    "1 Error response from daemon: manifest for x:1 not found: "
                    "manifest unknown"
                ),
                ("pull", "x:1"),
            ),
            (
                "refused login",
                (
                    '1 Error response from daemon: Head "https://ghcr.io/v2/x": '
                    "unauthorized: authentication required"
                ),
                ("pull", "x:1"),
            ),
            (
                "failed build step",
                (
                    '1 ERROR: failed to solve: process "/bin/sh -c go mod download" '
                    "did not complete successfully: exit code: 1"
                ),
                ("build", "."),
            ),
            (
                "build step output naming the same error",
                '1 #12 3.1 ERROR: Get "https://proxy.golang.org/x": EOF',
                ("build", "."),
            ),
            ("404", "1 ERROR: unexpected status: 404 Not Found", ("build", ".")),
        ):
            with self.subTest(name):
                (self.dir / "count").write_text("0")
                (self.dir / "calls").write_text("")
                result, calls = self.docker([script, "0"], *args)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(len(calls), 1)

    def test_a_container_that_prints_the_same_words_is_not_a_pull(self):
        result, calls = self.docker([f"3 {HUB_EOF}", "0"], "run", "image", "cmd")
        self.assertEqual(result.returncode, 3)
        self.assertEqual(len(calls), 1)

    def test_a_registry_that_stays_down_still_fails(self):
        result, calls = self.docker([f"125 {HUB_EOF}"], "run", "image")
        self.assertEqual(result.returncode, 125)
        self.assertEqual(len(calls), 4)

    def test_other_commands_pass_straight_through(self):
        result, calls = self.docker(
            [f"7 {HUB_EOF}", "0"],
            "exec",
            "-i",
            "c",
            "cat",
            stdin="body\n",
            env={"READ_STDIN": "1"},
        )
        self.assertEqual(result.returncode, 7)
        self.assertEqual(calls, ["exec -i c cat", "body"])

    def test_a_build_from_stdin_is_not_read_twice(self):
        result, calls = self.docker(
            [f"1 {BUILD_EOF}", "0"], "build", "-", stdin="FROM x\n"
        )
        self.assertEqual(result.returncode, 1)
        self.assertEqual(len(calls), 1)

    def test_it_finds_the_real_docker_rather_than_itself(self):
        result, calls = self.docker(["0"], "pull", "x")
        self.assertEqual(result.returncode, 0)
        self.assertEqual(calls, ["pull x"])


def jobs(workflow):
    """Each job's name and text, with comments dropped."""
    text = "\n".join(
        line
        for line in workflow.read_text().splitlines()
        if not line.lstrip().startswith("#")
    )
    _, found, body = text.partition("\njobs:\n")
    if not found:
        return {}
    parts = re.split(r"(?m)^(?=  [\w-]+:\n)", body)
    return {part.split(":", 1)[0].strip(): part for part in parts if part.strip()}


class WorkflowWiringTests(unittest.TestCase):
    def test_every_job_that_starts_images_itself_turns_the_retry_on(self):
        pulling = re.compile(
            r"\bdocker (run|pull|build|create)\b|\bchore (e2e|instance|testbed):"
        )
        seen = []
        for workflow in sorted(WORKFLOWS.glob("*.yml")):
            for name, job in jobs(workflow).items():
                if not pulling.search(job):
                    continue
                seen.append(f"{workflow.name}:{name}")
                with self.subTest(workflow=workflow.name, job=name):
                    self.assertIn(ENABLE_STEP, job)
                    self.assertLess(
                        job.index("actions/checkout@"), job.index(ENABLE_STEP)
                    )
        # The scan must keep finding the jobs that dropped queue entries.
        self.assertIn("ci.yml:first-start", seen)
        self.assertIn("e2e-suite.yml:suite", seen)

    def test_the_image_build_job_has_a_bound(self):
        job = jobs(WORKFLOWS / "docker.yml")["build-and-push"]
        minutes = re.search(r"(?m)^    timeout-minutes: (\d+)$", job)
        self.assertIsNotNone(minutes, "a hung build would hold a runner for six hours")
        self.assertLess(int(minutes.group(1)), 60)


# upload-artifact does not retry a dropped connection, so each use decides what one
# does to its job (#1896). Add a new artifact to one of these sets.
SECOND_TRY = " (second try)"
READ_BY_PEOPLE = {
    "go-imap-statement-coverage",
    "go-imap-fuzz-findings",
    "api-contract-changes",
    "api-compatibility-newest-main",
    "mail-conformance",
    "coverage-report",
    "e2e-container-logs",
    "js-filter-isolation-logs",
    "cluster-report",
    "api-gap-report",
    "load-results",
}
# People read these, and a later job reads them too if they arrive. Each download
# carries on when the artifact is missing, because the job has an answer for a run
# with no report: contract_tier_issues.py files the run as not finished and closes
# nothing. So a lost upload still cannot fail a job, and it is not tried again.
READ_BY_A_LATER_JOB_THAT_CAN_DO_WITHOUT = {"e2e-container-logs"}
READ_BY_A_LATER_JOB = {
    "example-proofs-rest",
    "coverage-part-rest",
    "example-proofs-api-${{ matrix.shard }}",
    "coverage-part-api-${{ matrix.shard }}",
    "example-proofs-handlers-${{ matrix.shard }}",
    "coverage-part-handlers-${{ matrix.shard }}",
    "live-example-proofs",
}


def steps(job):
    """A job's steps as text, each with its first field indented like the others."""
    found = re.split(r"(?m)^      - ", job)[1:]
    return ["        " + step for step in found]


def step_field(step, name):
    match = re.search(rf"(?m)^        {re.escape(name)}: (.+)$", step)
    return match.group(1) if match else ""


def step_with(step):
    return step[step.index("        with:\n") :].rstrip()


def artifact(step):
    return re.search(r"(?m)^          name: (.+)$", step).group(1)


def uploads():
    """Every first try at an upload as (workflow:job, its step, the step after it)."""
    found = []
    for workflow in sorted(WORKFLOWS.glob("*.yml")):
        for name, job in jobs(workflow).items():
            listed = steps(job) + [""]
            for step, following in zip(listed, listed[1:]):
                if "uses: actions/upload-artifact@" not in step:
                    continue
                if not step_field(step, "name").endswith(SECOND_TRY):
                    found.append((f"{workflow.name}:{name}", step, following))
    return found


class ArtifactUploadTests(unittest.TestCase):
    def test_each_upload_says_who_reads_it(self):
        names = [artifact(step) for _, step, _ in uploads()]
        self.assertTrue(names)
        self.assertEqual(set(names), READ_BY_PEOPLE | READ_BY_A_LATER_JOB)

    def test_no_first_try_can_fail_its_job(self):
        for where, step, _ in uploads():
            with self.subTest(where=where, artifact=artifact(step)):
                self.assertEqual(step_field(step, "continue-on-error"), "true")

    def test_an_upload_only_people_read_is_not_tried_again(self):
        for where, step, following in uploads():
            if artifact(step) in READ_BY_PEOPLE:
                with self.subTest(where=where, artifact=artifact(step)):
                    self.assertFalse(step_field(following, "name").endswith(SECOND_TRY))

    def test_an_upload_a_later_job_reads_is_tried_again_and_then_fails_the_job(self):
        for where, step, following in uploads():
            if artifact(step) not in READ_BY_A_LATER_JOB:
                continue
            with self.subTest(where=where, artifact=artifact(step)):
                self.assertEqual(
                    step_field(following, "name"),
                    f"{step_field(step, 'name')}{SECOND_TRY}",
                )
                self.assertEqual(step_field(following, "continue-on-error"), "")
                condition = step_field(following, "if")
                self.assertIn(
                    f"steps.{step_field(step, 'id')}.outcome == 'failure'", condition
                )
                self.assertIn(step_field(step, "if"), condition)
                self.assertEqual(step_field(following, "uses"), step_field(step, "uses"))
                self.assertEqual(step_with(following), step_with(step))
                # The reset can come after the artifact finalized.
                self.assertIn("          overwrite: true", step_with(step))

    def downloads(self):
        """Every download as (workflow:job, its step, the artifact or pattern)."""
        found = []
        for workflow in sorted(WORKFLOWS.glob("*.yml")):
            for name, job in jobs(workflow).items():
                for step in steps(job):
                    if "uses: actions/download-artifact@" in step:
                        for wanted in re.findall(
                            r"(?m)^          (?:name|pattern): (.+)$", step
                        ):
                            found.append((f"{workflow.name}:{name}", step, wanted))
        return found

    def test_a_job_downloads_only_what_is_tried_again(self):
        wanted = re.findall(
            r"--pattern '([^']+)'", (ROOT / "scripts/ci_example_proofs.sh").read_text()
        )
        wanted += [
            pattern
            for _, _, pattern in self.downloads()
            if pattern not in READ_BY_A_LATER_JOB_THAT_CAN_DO_WITHOUT
        ]
        self.assertEqual(
            set(wanted), {"example-proofs-*", "coverage-part-*", "live-example-proofs"}
        )
        for pattern in wanted:
            with self.subTest(pattern=pattern):
                self.assertTrue(fnmatch.filter(READ_BY_A_LATER_JOB, pattern))
                self.assertFalse(fnmatch.filter(READ_BY_PEOPLE, pattern))

    def test_a_job_that_reads_what_is_not_tried_again_carries_on_without_it(self):
        self.assertLessEqual(READ_BY_A_LATER_JOB_THAT_CAN_DO_WITHOUT, READ_BY_PEOPLE)
        seen = set()
        for where, step, pattern in self.downloads():
            if pattern in READ_BY_A_LATER_JOB_THAT_CAN_DO_WITHOUT:
                seen.add(where)
                with self.subTest(where=where, artifact=pattern):
                    self.assertEqual(step_field(step, "continue-on-error"), "true")
        self.assertEqual(
            seen,
            {
                "contract-fuzz.yml:finding-issues",
                "contract-faults.yml:finding-issues",
            },
        )


if __name__ == "__main__":
    unittest.main()
