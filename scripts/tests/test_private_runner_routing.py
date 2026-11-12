"""Keep executable GitHub Actions jobs on the owner's private runner pool."""

from pathlib import Path
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
RUNNER_POOL = "antimatter-vps-linux-x64"


class PrivateRunnerRouting(unittest.TestCase):
    def test_every_executable_job_uses_the_private_pool(self):
        executable_jobs = 0
        for workflow in sorted((ROOT / ".github/workflows").glob("*.yml")):
            document = yaml.safe_load(workflow.read_text())
            for name, job in document["jobs"].items():
                with self.subTest(workflow=workflow.name, job=name):
                    if "uses" in job:
                        self.assertNotIn("runs-on", job)
                    else:
                        executable_jobs += 1
                        self.assertEqual(job.get("runs-on"), RUNNER_POOL)
        self.assertGreater(executable_jobs, 0)

    def test_python_consumers_bootstrap_before_the_first_use(self):
        consumers = 0
        for workflow in sorted((ROOT / ".github/workflows").glob("*.yml")):
            document = yaml.safe_load(workflow.read_text())
            for name, job in document["jobs"].items():
                steps = job.get("steps", [])
                for index, step in enumerate(steps):
                    command = step.get("run", "")
                    if not (
                        "python3" in command
                        or "scripts/ci/install-goreleaser" in command
                        or "scripts/ci/install-golangci-lint" in command
                        or step.get("uses") == "./.github/actions/cache-pinned-tool"
                    ):
                        continue
                    consumers += 1
                    with self.subTest(workflow=workflow.name, job=name):
                        bootstrap = [
                            item for item in steps[:index]
                            if item.get("uses") == "./.github/actions/setup-python"
                        ]
                        self.assertEqual(len(bootstrap), 1)
                        self.assertNotIn("if", bootstrap[0])
                    break
        self.assertGreater(consumers, 0)

    def test_python_bootstrap_provides_pip_and_the_local_gate_dependencies(self):
        action = ROOT / ".github/actions/setup-python/action.yml"
        self.assertTrue(action.is_file(), "Python consumers need an explicit bootstrap")
        steps = yaml.safe_load(action.read_text())["runs"]["steps"]
        self.assertEqual(
            steps[0]["uses"],
            "actions/setup-python@ece7cb06caefa5fff74198d8649806c4678c61a1",
        )
        self.assertEqual(steps[0]["with"]["python-version"], "3.13")
        self.assertEqual(
            steps[1]["run"],
            "python3 -m pip install --disable-pip-version-check PyYAML==6.0.3 idna==3.11",
        )
        self.assertEqual(steps[1]["shell"], "bash")

    def test_pnpm_is_enabled_only_after_node_is_installed(self):
        consumers = 0
        for workflow in sorted((ROOT / ".github/workflows").glob("*.yml")):
            document = yaml.safe_load(workflow.read_text())
            for name, job in document["jobs"].items():
                steps = job.get("steps", [])
                for index, step in enumerate(steps):
                    if "corepack enable pnpm" not in step.get("run", ""):
                        continue
                    consumers += 1
                    with self.subTest(workflow=workflow.name, job=name):
                        setup = [
                            item for item in steps[:index]
                            if item.get("uses", "").startswith("actions/setup-node@")
                        ]
                        self.assertEqual(len(setup), 1)
                        self.assertEqual(setup[0]["with"]["node-version"], "22")
        self.assertEqual(consumers, 3)
