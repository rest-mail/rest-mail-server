import os
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
API_SHARDS = tuple(f"example-proofs-api-{shard}" for shard in range(1, 5))
OTHER_PRODUCERS = ("example-proofs-rest", "example-proofs-handlers-1")


class E2EProofShards(unittest.TestCase):
    def run_judge(self, *, missing=None, empty=None):
        workflow = yaml.safe_load((ROOT / ".github/workflows/e2e.yml").read_text())
        command = next(
            step["run"]
            for step in workflow["jobs"]["e2e-proofs"]["steps"]
            if step["name"] == "Every example that needs setup is proven by some runner"
        )
        with tempfile.TemporaryDirectory() as directory:
            runner = Path(directory)
            proof_files = []
            for producer in (*API_SHARDS, *OTHER_PRODUCERS):
                if producer == missing:
                    continue
                proof = runner / "proofs" / producer / f"{producer}.json"
                proof.parent.mkdir(parents=True)
                proof.write_text("" if producer == empty else "[]\n")
                proof_files.append(str(proof))
            fake_python = runner / "python3"
            fake_python.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
            fake_python.chmod(0o755)
            result = subprocess.run(
                ["bash", "-c", command],
                check=False,
                cwd=ROOT,
                capture_output=True,
                text=True,
                timeout=600,
                env={
                    **os.environ,
                    "PATH": f"{runner}{os.pathsep}{os.environ['PATH']}",
                    "RUNNER_TEMP": str(runner),
                    "GITHUB_STEP_SUMMARY": str(runner / "summary"),
                },
            )
            expected = [
                "scripts/example_proofs.py",
                "--live",
                str(runner / "live-proofs"),
                "openapi-example.json",
                *sorted(proof_files),
            ]
            return result, expected

    def test_all_shards_and_additional_producers_reach_the_judge(self):
        result, expected = self.run_judge()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), expected)

    def test_other_producers_cannot_replace_a_missing_api_shard(self):
        for shard, producer in enumerate(API_SHARDS, start=1):
            with self.subTest(shard=shard):
                result, _ = self.run_judge(missing=producer)
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertIn(f"internal/api shard {shard}", result.stderr)
                self.assertEqual(result.stdout, "")

    def test_empty_api_proof_does_not_reach_the_judge(self):
        result, _ = self.run_judge(empty=API_SHARDS[2])
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("internal/api shard 3", result.stderr)
        self.assertEqual(result.stdout, "")
