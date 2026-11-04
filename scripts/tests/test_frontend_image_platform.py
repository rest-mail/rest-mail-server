"""The web apps' images install and build on the runner's own platform (#1850).

`pnpm install` under QEMU can crash and then wait forever, which hung the arm64
builds in docker.yml. Their output is plain files, so nothing but the serving
stage needs the target's platform.
"""

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DOCKER_WORKFLOW = ROOT / ".github" / "workflows" / "docker.yml"


def stages(app):
    """Each stage's name and the FROM line that starts it."""
    text = (ROOT / app / "Dockerfile").read_text(encoding="utf-8")
    return {
        line.rsplit(" AS ", 1)[1]: line for line in re.findall(r"(?m)^FROM .*$", text)
    }


class FrontendImagePlatformTests(unittest.TestCase):
    def test_install_runs_on_the_build_platform(self):
        for app in ("webmail", "admin"):
            with self.subTest(app):
                self.assertIn("--platform=$BUILDPLATFORM", stages(app)["deps"])

    def test_the_build_stays_on_the_install_stage(self):
        for app in ("webmail", "admin"):
            with self.subTest(app):
                self.assertEqual(stages(app)["builder"], "FROM deps AS builder")

    def test_only_the_serving_stage_takes_the_target_platform(self):
        for app in ("webmail", "admin"):
            with self.subTest(app):
                self.assertNotIn("--platform", stages(app)["prod"])
                dockerfile = (ROOT / app / "Dockerfile").read_text(encoding="utf-8")
                self.assertIn("COPY --from=builder /app/dist ", dockerfile)

    def test_both_platforms_are_still_published(self):
        self.assertIn(
            "platforms: linux/amd64,linux/arm64",
            DOCKER_WORKFLOW.read_text(encoding="utf-8"),
        )


if __name__ == "__main__":
    unittest.main()
