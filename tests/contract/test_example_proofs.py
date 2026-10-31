"""Every example of an operation the live run never sends is proven in process
(#1334), as every example that needs setup is (#1237)."""

import importlib.util
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("example_proofs", ROOT / "scripts/example_proofs.py")
example_proofs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(example_proofs)


class ExampleProofs(unittest.TestCase):
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
