import json
import os
import shlex
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[2]


class TopologyContractSelectionTests(unittest.TestCase):
    def invoke(self, arguments):
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        command = tasks["contract:topology"]["cmds"][0]
        command = command.replace("{{.CLI_ARGS}}", shlex.join(arguments))
        with tempfile.TemporaryDirectory(prefix="restmail-topology-contract-") as temporary:
            directory = Path(temporary)
            executable = directory / "chore"
            record = directory / "invocation.json"
            executable.write_text(
                f"#!{sys.executable}\n"
                "import json, os, sys\n"
                "from pathlib import Path\n"
                "Path(os.environ['TOPOLOGY_CONTRACT_RECORD']).write_text(json.dumps({"
                "'arguments': sys.argv[1:], "
                "'skip_pairs': os.environ.get('CONTRACT_SKIP_PAIRS')}))\n"
            )
            executable.chmod(0o755)
            environment = {
                **os.environ,
                "CHORE_BIN": str(executable),
                "TOPOLOGY_CONTRACT_RECORD": str(record),
            }
            result = subprocess.run(
                ["bash", "-eu", "-o", "pipefail"],
                input=command,
                env=environment,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            invocation = json.loads(record.read_text())
        self.assertEqual(invocation["skip_pairs"], "true")
        return invocation["arguments"]

    def test_operation_selection_reaches_the_child_after_the_separator(self):
        arguments = ["--operation-id", "getServerCapabilities"]
        self.assertEqual(self.invoke(arguments), ["contract", "--", *arguments])

    def test_multiple_selectors_are_preserved(self):
        arguments = ["--operation-id", "getDomain", "--tag", "Domains"]
        self.assertEqual(self.invoke(arguments), ["contract", "--", *arguments])

    def test_an_unselected_run_has_no_selection_arguments(self):
        self.assertIn(self.invoke([]), (["contract"], ["contract", "--"]))


if __name__ == "__main__":
    unittest.main()
