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
        # it; a hook that replaced it would test a request no pair describes. The
        # one hook it has resets and, for a success, prepares the address's
        # domain (own-data, #1385); it changes nothing in the request.
        hooks = types.ModuleType("vertrag_hooks")
        registered = []
        hooks.before = lambda pattern: (
            lambda function: registered.append((pattern, function.__name__)) or function
        )
        hooks.after = lambda pattern: lambda function: function
        spec = importlib.util.spec_from_file_location(
            "contract_autodiscover_hooks", ROOT / "tests/contract/hooks.py"
        )
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(module)
        self.assertEqual(
            [name for pattern, name in registered if pattern == "microsoftAutodiscover"],
            ["public_reads_after_a_reset"],
        )
        body = "<Autodiscover><Request>"
        path = "/autodiscover/autodiscover.xml?emailaddress=user%40example.com"
        for reset in ("", "1"):
            sent = {
                "protocol": "http:", "host": "api", "port": "8080",
                "fullPath": path, "name": "x > 200 > application/xml",
                "request": {"body": body, "headers": {}},
            }
            with patch.dict("os.environ", {"CONTRACT_RESET_SEED": reset}), patch.dict(
                module.public_reads_after_a_reset.__globals__,
                {"start_clean": lambda t: None, "an_active_domain_for_autoconfig": lambda t: None},
            ):
                module.public_reads_after_a_reset(sent)
            self.assertEqual(sent["fullPath"], path)
            self.assertEqual(sent["request"], {"body": body, "headers": {}})

if __name__ == "__main__":
    unittest.main()
