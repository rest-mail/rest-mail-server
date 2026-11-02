"""Every example of an operation the live run never sends is proven in process
(#1334), as every example that needs setup is (#1237)."""

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("example_proofs", ROOT / "scripts/example_proofs.py")
example_proofs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(example_proofs)


class ExampleProofs(unittest.TestCase):
    def run_proof_check(self, examples, *proofs):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            examples_path = root / "examples.json"
            examples_path.write_text(json.dumps(examples))
            proof_paths = []
            for index, proof in enumerate(proofs):
                path = root / f"proofs-{index}.json"
                path.write_text(json.dumps(proof))
                proof_paths.append(str(path))
            return subprocess.run(
                [sys.executable, str(ROOT / "scripts/example_proofs.py"),
                 str(examples_path), *proof_paths],
                capture_output=True, text=True, check=False,
            )

    def test_cli_rejects_every_unproven_requirement_family(self):
        for requires in (
            {"principal": "anonymous"},
            {"body": "over_cap"},
            {"limit": "rate"},
            {"fault": "database"},
            {"state": "two_factor_session"},
            {"state": "two_factor_session", "fault": "database"},
        ):
            with self.subTest(requires=requires):
                result = self.run_proof_check({"operation": {"refused": {"requires": requires}}}, {})
                needs = ", ".join(f"{kind} {value}" for kind, value in sorted(requires.items()))
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(
                    result.stdout,
                    f"operation/refused requires {needs}, and no in-process test proved it\n"
                    "0 of 1 examples that need setup are proven in process\n",
                )

    def test_cli_requires_the_exact_operation_and_pair(self):
        examples = {"operation": {
            "first": {"requires": {"fault": "database"}},
            "second": {"requires": {"body": "over_cap"}},
        }}
        result = self.run_proof_check(
            examples, {"operation": ["first", "another"], "another_operation": ["second"]},
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(
            result.stdout,
            "operation/second requires body over_cap, and no in-process test proved it\n"
            "1 of 2 examples that need setup are proven in process\n",
        )

    def test_cli_unions_runner_proofs_without_double_counting(self):
        examples = {"operation": {
            "first": {"requires": {"fault": "database"}},
            "second": {"requires": {"body": "over_cap"}},
            "live": {},
        }}
        result = self.run_proof_check(
            examples, {"operation": ["first"]}, {"operation": ["first", "second"]},
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "2 of 2 examples that need setup are proven in process\n")

    def test_every_pass_sends_what_the_primary_run_leaves_out(self):
        # Each operation the primary run leaves out is sent by the teardown
        # pass, so none goes unsent.
        self.assertEqual(example_proofs.never_sent(ROOT), frozenset())

    def test_an_unsent_operations_examples_need_proofs(self):
        root = Path(tempfile.mkdtemp())
        (root / "tests/contract").mkdir(parents=True)
        (root / "vertrag.yml").write_text("a: 1\nexclude-operation-id:\n  - sentByAPass\n  - neverSent\n\nb: 2\n")
        (root / "tests/contract/teardown.py").write_text('OPERATIONS = ("sentByAPass",)\n')
        unsent = example_proofs.never_sent(root)
        self.assertEqual(unsent, frozenset({"neverSent"}))
        examples = {"neverSent": {"found": {}, "gone": {}}, "live": {"found": {}}}
        self.assertEqual(example_proofs.unproven(examples, {"neverSent": ["found"]}, unsent),
                         [("neverSent", "gone", {"sent": "never"})])


if __name__ == "__main__":
    unittest.main()
