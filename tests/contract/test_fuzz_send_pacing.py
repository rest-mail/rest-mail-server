import copy
import os
import unittest
from unittest.mock import patch

from test_hooks_delete_domain import load_hooks


class FuzzSendPacingTests(unittest.TestCase):
    def test_generated_sends_are_paced_without_rewriting_the_request(self):
        module, _, registered = load_hooks()
        for operation in ("sendMessage", "sendDraft", "forwardMessage"):
            self.assertIn(operation, registered)

        transaction = {
            "request": {
                "headers": {"Idempotency-Key": "generated"},
                "body": '{"from":"sender@example.test","to":[]}',
            }
        }
        original = copy.deepcopy(transaction)
        for coverage, fuzz, expected in (("0", "1", 1), ("1", "0", 1), ("0", "0", 0)):
            with (
                self.subTest(coverage=coverage, fuzz=fuzz),
                patch.dict(os.environ, CONTRACT_COVERAGE=coverage, CONTRACT_FUZZ=fuzz),
                patch.object(module.rate_limits, "pace_sends") as pace,
            ):
                module.pace_the_coverage_sends(transaction)
                self.assertEqual(pace.call_count, expected)
                self.assertEqual(transaction, original)
