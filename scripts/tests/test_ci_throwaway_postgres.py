"""Hold CI's throwaway test databases to running without durability (#1979)."""

import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github/workflows"
SCRIPT = ROOT / "scripts/ci/throwaway-postgres"
STEP = 'scripts/ci/throwaway-postgres "${{ job.services.postgres.id }}"'
SETTINGS = ("fsync", "synchronous_commit", "full_page_writes")

# Every job with a PostgreSQL service, and so every one that must have the step. A new one
# has to be named here, which makes someone decide what its database stands in for.
THROWAWAY = {"ci.yml": {"test", "test-api"}}


def jobs(workflow):
    """Each job's name and text, from the workflow's top-level `jobs:` section."""
    start = re.search(r"^jobs:$", workflow, re.MULTILINE)
    if start is None:
        return {}
    section = workflow[start.end():]
    matches = list(re.finditer(r"^  ([\w-]+):$", section, re.MULTILINE))
    return {
        match[1]: section[match.end():matches[index + 1].start() if index + 1 < len(matches) else len(section)]
        for index, match in enumerate(matches)
    }


def has_postgres_service(job):
    return re.search(r"^\s+image: postgres:", job, re.MULTILINE) is not None


def faults(workflow):
    """The jobs whose database would run with durability, and why."""
    found = {}
    for name, job in jobs(workflow).items():
        if not has_postgres_service(job):
            continue
        step = job.find(f"run: {STEP}")
        first_test = job.find("go test")
        if step < 0:
            found[name] = "no step sets the database's durability off"
        elif first_test < 0 or first_test < step:
            found[name] = "the tests start before the step does"
    return found


class EachTestJobHoldsTheSetting(unittest.TestCase):
    def setUp(self):
        self.ci = (WORKFLOWS / "ci.yml").read_text()

    def test_every_job_with_a_postgres_service_is_named_and_has_the_step(self):
        for path in sorted(WORKFLOWS.glob("*.yml")):
            with self.subTest(workflow=path.name):
                workflow = path.read_text()
                self.assertEqual(faults(workflow), {})
                named = {name for name, job in jobs(workflow).items() if has_postgres_service(job)}
                self.assertEqual(named, THROWAWAY.get(path.name, set()))

    def test_nothing_that_stands_in_for_an_install_loses_its_durability(self):
        for path in sorted(WORKFLOWS.glob("*.yml")):
            with self.subTest(workflow=path.name):
                using = {name for name, job in jobs(path.read_text()).items() if "throwaway-postgres" in job}
                self.assertEqual(using, THROWAWAY.get(path.name, set()))

    def test_a_job_that_loses_the_step_is_named(self):
        removed, count = re.subn(rf"^ +run: {re.escape(STEP)}\n", "", self.ci, flags=re.MULTILINE)
        self.assertEqual(count, 2)
        self.assertEqual(set(faults(removed)), {"test", "test-api"})

    def test_a_job_that_runs_the_step_after_its_tests_is_named(self):
        moved = self.ci.replace(f"run: {STEP}", "run: go test ./... # before", 1) + f"\n        run: {STEP}\n"
        self.assertIn("test", faults(moved))

    def test_a_job_that_has_a_database_and_no_tests_is_named(self):
        workflow = textwrap.dedent(f"""\
            jobs:
              other:
                services:
                  postgres:
                    image: postgres:16-alpine
                steps:
                  - run: {STEP}
            """)
        self.assertEqual(set(faults(workflow)), {"other"})


class TheScriptReadsTheSettingBack(unittest.TestCase):
    """Runs the real script against a stand-in docker, which keeps what psql was told."""

    FAKE_DOCKER = textwrap.dedent("""\
        #!/usr/bin/env python3
        import json, os, sys
        state_path = os.environ["FAKE_STATE"]
        state = json.load(open(state_path))
        sql = sys.argv[sys.argv.index("-c") + 1]
        state["calls"].append(sql)
        code = 0
        if sql in state["broken"]:
            code = 3
        elif sql.startswith("ALTER SYSTEM SET "):
            name, value = sql.removeprefix("ALTER SYSTEM SET ").split(" = ")
            state["pending"][name] = value
        elif sql == "SELECT pg_reload_conf()":
            state["reloaded"] = True
            print("t")
        elif sql.startswith("SHOW "):
            name = sql.removeprefix("SHOW ")
            state["reads"][name] = state["reads"].get(name, 0) + 1
            applied = state["reloaded"] and state["reads"][name] > state["slow_reads"] and name not in state["refused"]
            print(state["pending"].get(name, "on") if applied else "on")
        json.dump(state, open(state_path, "w"))
        sys.exit(code)
        """)

    def run_script(self, *arguments, broken=(), refused=(), slow_reads=0):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        bin_dir = Path(directory.name)
        docker = bin_dir / "docker"
        docker.write_text(self.FAKE_DOCKER)
        # So that a setting that never applies does not cost this test its real waits.
        pause = bin_dir / "sleep"
        pause.write_text("#!/bin/sh\nexit 0\n")
        for path in (docker, pause):
            path.chmod(path.stat().st_mode | stat.S_IXUSR)
        state = bin_dir / "state.json"
        state.write_text(json.dumps({
            "calls": [],
            "pending": {},
            "reads": {},
            "reloaded": False,
            "broken": list(broken),
            "refused": list(refused),
            "slow_reads": slow_reads,
        }))
        result = subprocess.run(
            [str(SCRIPT), *arguments],
            capture_output=True,
            text=True,
            env={**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}", "FAKE_STATE": str(state)},
        )
        return result, json.loads(state.read_text())

    def test_it_turns_each_setting_off_reloads_and_reads_each_back(self):
        result, state = self.run_script("container-id")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            state["calls"],
            [f"ALTER SYSTEM SET {name} = off" for name in SETTINGS]
            + ["SELECT pg_reload_conf()"]
            + [f"SHOW {name}" for name in SETTINGS],
        )
        self.assertEqual(result.stdout.splitlines(), [f"{name} = off" for name in SETTINGS])

    def test_a_reload_that_takes_a_moment_is_waited_for(self):
        result, state = self.run_script("container-id", slow_reads=3)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state["reads"], {name: 4 for name in SETTINGS})

    def test_a_setting_that_never_applies_fails_and_is_named(self):
        for name in SETTINGS:
            with self.subTest(setting=name):
                result, _ = self.run_script("container-id", refused=[name])
                self.assertEqual(result.returncode, 1)
                self.assertIn(f"::error::{name} is still 'on'", result.stderr)

    def test_a_database_that_refuses_any_statement_fails_the_step(self):
        for sql in (
            "ALTER SYSTEM SET fsync = off",
            "ALTER SYSTEM SET full_page_writes = off",
            "SELECT pg_reload_conf()",
            "SHOW synchronous_commit",
        ):
            with self.subTest(sql=sql):
                result, _ = self.run_script("container-id", broken=[sql])
                self.assertEqual(result.returncode, 1)
                self.assertIn("::error::", result.stderr)

    def test_it_needs_the_container(self):
        result, state = self.run_script()
        self.assertEqual(result.returncode, 2)
        self.assertEqual(state["calls"], [])


if __name__ == "__main__":
    unittest.main()
