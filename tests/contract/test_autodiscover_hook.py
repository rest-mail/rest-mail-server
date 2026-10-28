"""Autodiscover's XML requests are sent as the example pairs write them (#1232)."""

import importlib.util
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


class ContractAutodiscoverHookTests(unittest.TestCase):
    def test_no_hook_rewrites_the_autodiscover_request(self):
        # The generated description carries each pair's XML body, and Vertrag sends
        # it; a hook that replaced it would test a request no pair describes.
        hooks = types.ModuleType("vertrag_hooks")
        registered = []
        hooks.before = lambda pattern: (
            lambda function: registered.append(pattern) or function
        )
        hooks.after = lambda pattern: lambda function: function
        spec = importlib.util.spec_from_file_location(
            "contract_autodiscover_hooks", ROOT / "tests/contract/hooks.py"
        )
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(module)
        self.assertNotIn("microsoftAutodiscover", registered)


if __name__ == "__main__":
    unittest.main()
