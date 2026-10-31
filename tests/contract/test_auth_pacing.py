"""Contract auth traffic stays within the API's shared token bucket."""

import copy
import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


class AuthPacing(unittest.TestCase):
    def test_examples_and_probes_share_a_sustainable_auth_rate(self):
        hooks = types.ModuleType("vertrag_hooks")
        registered = []
        hooks.before = lambda pattern: lambda function: registered.append((pattern, function)) or function
        hooks.after = lambda pattern: lambda function: function
        spec = importlib.util.spec_from_file_location("paced_contract_hooks", ROOT / "tests/contract/hooks.py")
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(module)
        paced = {pattern: function for pattern, function in registered
                 if function is module.pace_auth_requests}
        self.assertEqual(set(paced), {
            "login", "refreshToken", "linkAccount", "createAppPassword", "protocolSignIn", "enrollTwoFactor",
            "confirmTwoFactor", "recoverAccount", "acceptInvitation", "resetAdmin",
        })
        owner = {pattern for pattern, function in registered if function is module.pace_owner_requests}
        self.assertEqual(owner, {"ownerChallenge", "ownerToken", "ownerStatus"})
        with patch.object(module.time, "sleep") as sleep:
            module.pace_owner_requests({})
        sleep.assert_called_once_with(1.1)

        # Begin with an exhausted bucket, then send more than its burst of 15
        # across different operations: all of them charge the same 1 rps bucket.
        tokens = 0

        def refill(seconds):
            nonlocal tokens
            tokens = min(15, tokens + seconds)

        with patch.object(module.time, "sleep", side_effect=refill):
            for _ in range(4):
                for operation, pace in paced.items():
                    for prefix in ("", "coverage: "):
                        transaction = {
                            "operationId": operation,
                            "name": prefix + "/fixture > Example > 200 > application/json",
                            "request": {"body": '{"protocol":"imap"}',
                                        "headers": {"Authorization": "Basic fixture"}},
                        }
                        original = copy.deepcopy(transaction)
                        pace(transaction)
                        self.assertEqual(transaction, original)
                        self.assertGreaterEqual(tokens, 1, "auth request would receive 429")
                        tokens -= 1


if __name__ == "__main__":
    unittest.main()
