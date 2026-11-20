"""Where Email, explained's outside-link check runs (#2120, AC02b).

`node check.mjs --external` asks other people's sites, which change on their own
schedule, so it runs in the daily scheduled workflow and when the guide is
published, and never in the pull request gate. The scheduled run also switches
on the checker's live tests, so they cannot sit skipped there.
"""

from pathlib import Path
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
EXTERNAL = "node check.mjs --external"


def workflow(name):
    document = yaml.safe_load((ROOT / ".github/workflows" / name).read_text())
    # PyYAML reads the key `on` as True.
    document["on"] = document.pop(True, document.get("on"))
    return document


def steps_running(job, text):
    return [step for step in job["steps"] if text in step.get("run", "")]


class OutsideLinkCheckPlacement(unittest.TestCase):
    def test_the_scheduled_workflow_runs_it_and_the_live_tests(self):
        document = workflow("scheduled.yml")
        self.assertIn("schedule", document["on"])
        job = document["jobs"]["guide-links"]
        checks = steps_running(job, EXTERNAL)
        self.assertEqual(len(checks), 1)
        self.assertNotIn("if", checks[0])
        live = [step for step in job["steps"] if step.get("env", {}).get("EMAIL_EXPLAINED_LIVE_LINKS") == "1"]
        self.assertEqual(len(live), 1, "the live tests are skipped unless EMAIL_EXPLAINED_LIVE_LINKS=1")
        self.assertIn("node --test check.test.mjs", live[0]["run"])
        self.assertEqual(live[0].get("working-directory"), "scripts/email-explained")
        issue = next(step for step in job["steps"] if step.get("name") == "Open or update the issue")
        self.assertIn("'^(broken|refused|exception)'", issue["run"], "the issue must list every kind of failure")

    def test_publishing_the_guide_runs_it_but_a_pull_request_does_not(self):
        document = workflow("email-explained.yml")
        self.assertIn("push", document["on"])
        job = document["jobs"]["publish"]
        checks = steps_running(job, EXTERNAL)
        self.assertEqual(len(checks), 1)
        self.assertEqual(checks[0].get("if"), "github.event_name != 'pull_request'")
        build = next(index for index, step in enumerate(job["steps"]) if "node bundle.mjs" in step.get("run", ""))
        self.assertLess(job["steps"].index(checks[0]), build, "a guide with failing outside links must not be published")

    def test_the_pull_request_gate_does_not_run_it(self):
        for name in ("ci.yml",):
            for job_id, job in workflow(name)["jobs"].items():
                with self.subTest(workflow=name, job=job_id):
                    self.assertEqual(steps_running(job, "--external"), [])
                    for step in job.get("steps", []):
                        self.assertNotIn("EMAIL_EXPLAINED_LIVE_LINKS", step.get("env", {}))


if __name__ == "__main__":
    unittest.main()
