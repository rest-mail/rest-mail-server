"""Generated signing-key fixtures must never replace named error examples."""

import re
import tempfile
import unittest
from pathlib import Path

import probes
import yaml

ROOT = Path(__file__).resolve().parents[2]


class ProbeConfig(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.output = Path(directory.name) / "probes.yml"
        self.report = Path(directory.name) / "coverage.xml"
        self.source = ROOT / "vertrag.yml"
        self.before = self.source.read_text()
        self.base = yaml.safe_load(self.before)
        self.assertEqual(probes.main(self.source, self.output, self.report), 0)
        self.config = yaml.safe_load(self.output.read_text())

    def test_full_examples_keep_their_surface_gate_and_have_no_pins(self):
        self.assertEqual(self.source.read_text(), self.before)
        self.assertEqual(self.base["surface"]["require"], 100)
        self.assertFalse(self.base.get("fuzz", {}).get("pin"))
        self.assertNotIn("surface", self.config)

    def test_generated_run_preserves_checks_and_all_nonrefusal_transactions(self):
        for key in (
            "endpoint",
            "auth",
            "checks",
            "exclude-operation-id",
            "hooks-worker-timeout",
        ):
            self.assertEqual(self.config[key], self.base[key])
        self.assertEqual(
            self.config["exclude-matching"],
            self.base["exclude-matching"] + [probes.REFUSALS],
        )
        pattern = re.compile(probes.REFUSALS)
        for status in [
            "200",
            "201",
            "204",
            "301",
            "400",
            "401",
            "403",
            "404",
            "422",
            "429",
            "500",
        ]:
            with self.subTest(status=status):
                self.assertEqual(
                    bool(
                        pattern.search(
                            "/path > summary > " + status + " > application/json"
                        )
                    ),
                    status.startswith("4"),
                )

    def test_only_opaque_key_is_held_and_paths_survive_generated_config_location(self):
        self.assertEqual(
            self.config["fuzz"], {"pin": {"private_key": probes.DKIM_KEY.read_text()}}
        )
        self.assertEqual(self.config["spec"], str(ROOT / "openapi.yaml"))
        self.assertEqual(
            self.config["hookfiles"], str(ROOT / "tests/contract/entrypoint.py")
        )
        self.assertEqual(self.config["reporter"], ["cli", "junit"])
        self.assertEqual(self.config["output"], ["", str(self.report.resolve())])

    def test_workflow_enforces_unpinned_examples_before_coverage_and_uses_coverage_report(
        self,
    ):
        # CI's E2E job runs `chore contract`, the same command a person runs,
        # and it runs tests/contract/run.sh gate, then the secret scan (#627).
        workflow = (ROOT / ".github/workflows/e2e-suite.yml").read_text()
        self.assertIn("run: chore contract", workflow)
        chores = (ROOT / "chores.yml").read_text()
        task = chores[
            chores.index("\n  contract:\n") : chores.index("\n  contract:run:\n")
        ]
        self.assertLess(
            task.index("tests/contract/run.sh gate"),
            task.index("tests/contract/secret_scan.py"),
        )
        script = (ROOT / "tests/contract/run.sh").read_text()
        examples = script.index("--sequence --phases examples")
        generated = script.index("python3 -B tests/contract/probes.py")
        coverage = script.index("--sequence --phases coverage")
        repeated = script.index("python3 -B tests/contract/repeat_fixtures.py")
        queue = script.index("chore test:contract-fixtures", repeated)
        self.assertLess(examples, repeated)
        self.assertLess(repeated, queue)
        self.assertLess(queue, generated)
        self.assertLess(examples, generated)
        self.assertLess(generated, coverage)
        # The coverage gate judges the description this server serves (#627).
        self.assertIn(
            'gate.py "$CONTRACT_SPEC" vertrag.yml "$out/coverage.xml" tests/contract/coverage-baseline.txt',
            script,
        )
        # The first command must fail the job immediately on any example or
        # surface failure; only coverage findings may defer to the ratchet.
        command = (
            script[script.rindex("\n", 0, examples) : generated].strip().splitlines()[0]
        )
        self.assertTrue(command.startswith('"$CONTRACT_VERTRAG" run'), command)
        self.assertNotIn("||", command)
        self.assertIn("set -euo pipefail", script)


if __name__ == "__main__":
    unittest.main()
