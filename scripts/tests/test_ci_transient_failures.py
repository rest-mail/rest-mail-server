"""CI survives a registry hiccup, and only that (#1847)."""

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


if __name__ == "__main__":
    unittest.main()
