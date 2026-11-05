"""A chart version is free only when the registry says that exact tag is absent."""

import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("check_chart_publish", ROOT / "scripts/check_chart_publish.py")
publish = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(publish)
REGISTRY = "oci://ghcr.io/rest-mail/charts/restmail"
MISSING = "Error: ghcr.io/rest-mail/charts/restmail:0.42.1: not found\n"


class ChartPublishGate(unittest.TestCase):
    def run_gate(self, status, stderr="", stdout=""):
        result = subprocess.CompletedProcess([], status, stdout, stderr)
        with patch.object(publish.subprocess, "run", return_value=result) as command:
            output = io.StringIO()
            with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                code = publish.main(["--registry", REGISTRY, "--version", "0.42.1"])
            self.assertEqual(command.call_args.args[0],
                             ["helm", "show", "chart", REGISTRY, "--version", "0.42.1"])
            return code, output.getvalue()

    def test_published_version_is_refused_before_any_push(self):
        code, output = self.run_gate(0, stdout="version: 0.42.1\n")
        self.assertEqual(code, 1)
        self.assertIn("already published", output)

    def test_only_the_exact_missing_tag_is_unpublished(self):
        code, output = self.run_gate(1, stderr=MISSING)
        self.assertEqual(code, 0, output)
        self.assertIn("0.42.1 is unpublished", output)

    def test_registry_and_authentication_failures_do_not_allow_a_push(self):
        for error in ("unauthorized", "connection reset by peer", "", "host not found", "Error: another/chart:0.42.1: not found", MISSING + "another error"):
            with self.subTest(error=error):
                code, output = self.run_gate(1, stderr=error)
                self.assertEqual(code, 1, output)
                self.assertIn("could not establish", output)

    def test_local_command_reads_the_checked_in_chart_version(self):
        replies = [subprocess.CompletedProcess([], 0, "version: 0.42.1\n", ""),
                   subprocess.CompletedProcess([], 1, "", MISSING)]
        with patch.dict(publish.os.environ, {}, clear=True), patch.object(publish.subprocess, "run", side_effect=replies) as command:
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(publish.main([]), 0)
            self.assertEqual(command.call_args_list[0].args[0],
                             ["helm", "show", "chart", str(ROOT / "helm/restmail")])

    def test_timeout_fails_closed(self):
        with patch.object(publish.subprocess, "run", side_effect=subprocess.TimeoutExpired("helm", 60)):
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(publish.main(["--version", "0.42.1"]), 1)

    def test_publication_uses_the_same_local_gate(self):
        workflow = (ROOT / ".github/workflows/helm.yml").read_text()
        self.assertIn("run: python3 scripts/check_chart_publish.py", workflow)


if __name__ == "__main__":
    unittest.main()
