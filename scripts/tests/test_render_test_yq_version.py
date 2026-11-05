"""The chart render test names a yq too old for it, instead of blaming the chart.

yq 4.45.4 and older evaluate the test's expressions differently. Each fails on a
chart that is correct, with a message that reads like a fault in the chart:
4.44.3 says `production: / may escalate privileges`, and 4.45.4 says
`a volume this test does not look into: restmail-api`.
"""

import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[2] / "helm/restmail/tests/render-test.sh"
MIKEFARAH = "yq (https://github.com/mikefarah/yq/) version v{}"


class RenderTestYqVersionTests(unittest.TestCase):
    def run_with_yq(self, version_line):
        """Run the script with a fake yq, and a fake helm that records its calls."""
        with tempfile.TemporaryDirectory() as tools:
            tools = Path(tools)
            calls = tools / "helm-calls"
            self.write_tool(tools / "yq", f'echo "{version_line}"\n')
            self.write_tool(tools / "helm", f'echo called >> "{calls}"\nexit 1\n')
            result = subprocess.run(
                ["bash", str(SCRIPT)],
                capture_output=True,
                text=True,
                timeout=600,
                env={**os.environ, "PATH": f"{tools}{os.pathsep}{os.environ['PATH']}"},
            )
            return result, calls.exists()

    @staticmethod
    def write_tool(path, body):
        path.write_text("#!/bin/sh\n" + body)
        path.chmod(path.stat().st_mode | stat.S_IXUSR)

    def assert_refused(self, version_line, found):
        result, reached_helm = self.run_with_yq(version_line)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("4.46.1", result.stderr)
        self.assertIn(found, result.stderr)
        self.assertFalse(reached_helm, "the test started on a yq it cannot trust")

    def assert_accepted(self, version_line):
        _, reached_helm = self.run_with_yq(version_line)
        self.assertTrue(reached_helm, f"{version_line} did not reach the chart")

    def test_a_yq_older_than_the_test_can_use_is_named_before_any_render(self):
        for version in ("4.44.3", "4.45.1", "4.45.4", "4.5.0"):
            with self.subTest(version=version):
                self.assert_refused(MIKEFARAH.format(version), f"v{version}")

    def test_the_oldest_yq_that_passes_and_every_later_one_go_on(self):
        # 4.100 is the case a comparison of the version as text gets wrong.
        for version in ("4.46.1", "4.47.2", "4.54.1", "4.100.0"):
            with self.subTest(version=version):
                self.assert_accepted(MIKEFARAH.format(version))

    def test_another_tool_called_yq_is_still_refused(self):
        self.assert_refused("yq 3.4.3", "yq 3.4.3")
        self.assert_refused(MIKEFARAH.format("3.4.1"), "v3.4.1")


if __name__ == "__main__":
    unittest.main()
