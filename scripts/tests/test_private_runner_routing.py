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
