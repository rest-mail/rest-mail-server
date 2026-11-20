"""The service-container guard reads the real workflows, and bites on the real mistakes.

Two shaped regressions are pinned here, both of which cost this repository a run:

  * a service without `--oom-score-adj 1000` cannot be exec'd on the ARC pool, so its
    health check never passes and the shard dies before a test;
  * a `#` inside the folded `options` scalar is passed to docker verbatim and fails with
    `invalid reference format`.

A guard that silently matches nothing is decoration, so the real tree is asserted to
contain services, and the fix for each shape is asserted to satisfy the checker.
"""

from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import check_service_containers  # noqa: E402
from check_service_containers import (  # noqa: E402
    REQUIRED_FLAG,
    load_workflows,
    service_count,
    service_problems,
)

GOOD_OPTIONS = f"""--health-cmd "pg_isready -U restmail"
--health-interval 10s
--health-retries 10
{REQUIRED_FLAG}"""


def workflow(*services) -> dict:
    return {
        "jobs": {
            "shard": {
                "runs-on": "ubuntu-24.04",
                "services": {name: config for name, config in services},
            }
        }
    }


class TheRealWorkflowsAskForTheFlag(unittest.TestCase):
    def test_every_service_container_carries_the_flag(self):
        workflows = load_workflows()
        problems = service_problems(workflows)
        self.assertEqual([], problems, "\n".join(problems))

    def test_the_guard_saw_the_services_it_claims_to_check(self):
        # If a refactor stops the checker finding `services`, the assertion above would
        # pass on an empty list for ever. ci.yml has four today.
        workflows = load_workflows()
        self.assertGreaterEqual(service_count(workflows), 4)


class AServiceWithoutTheFlagIsRefused(unittest.TestCase):
    def test_no_options_at_all_is_refused(self):
        problems = service_problems(
            {"ci.yml": workflow(("postgres", {"image": "postgres:16-alpine"}))}
        )
        self.assertEqual(1, len(problems), problems)
        self.assertIn("postgres", problems[0])
        self.assertIn(REQUIRED_FLAG, problems[0])

    def test_options_without_the_flag_are_refused(self):
        problems = service_problems(
            {
                "ci.yml": workflow(
                    (
                        "postgres",
                        {
                            "image": "postgres:16-alpine",
                            "options": "--health-cmd \"pg_isready -U restmail\"",
                        },
                    )
                )
            }
        )
        self.assertEqual(1, len(problems), problems)
        self.assertIn("every docker exec", problems[0])

    def test_a_comment_inside_the_options_is_refused(self):
        # This is the mistake that made every shard fail with `invalid reference format`.
        problems = service_problems(
            {
                "ci.yml": workflow(
                    (
                        "postgres",
                        {
                            "image": "postgres:16-alpine",
                            "options": f"# a runner with an empty store pulls first\n{GOOD_OPTIONS}",
                        },
                    )
                )
            }
        )
        self.assertEqual(1, len(problems), problems)
        self.assertIn("verbatim", problems[0])

    def test_a_service_that_is_not_a_mapping_is_named_not_ignored(self):
        problems = service_problems({"ci.yml": workflow(("postgres", "postgres:16-alpine"))})
        self.assertEqual(1, len(problems), problems)
        self.assertIn("not a mapping", problems[0])

    def test_a_job_without_services_is_not_a_problem(self):
        problems = service_problems({"ci.yml": {"jobs": {"build": {"runs-on": "ubuntu-24.04"}}}})
        self.assertEqual([], problems)

    def test_the_fixed_shape_passes(self):
        # The positive control: the checker is not simply refusing everything.
        problems = service_problems(
            {
                "ci.yml": workflow(
                    ("postgres", {"image": "postgres:16-alpine", "options": GOOD_OPTIONS})
                )
            }
        )
        self.assertEqual([], problems, problems)


if __name__ == "__main__":
    unittest.main()
