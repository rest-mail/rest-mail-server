import copy
import os
import unittest
from unittest.mock import patch

from test_hooks_delete_domain import load_hooks


class AuthorizationSendPacingTests(unittest.TestCase):
    """The authorization phase makes each message cell's resource with a real
    send by the administrator, then sends the cell. Unpaced, run 45732937231
    sent about 40 in two seconds; the outbound pipeline's rate_limit filter
    held every send past 20 a minute (202, held), so ten message cells were
    unsent and forward as admin failed 202 for 201 (#2099)."""

    def test_authorization_sends_are_paced_without_rewriting_the_request(self):
        module, _, registered = load_hooks()
        for operation in ("sendMessage", "sendDraft", "forwardMessage"):
            self.assertIn(operation, registered)

        transaction = {
            "request": {
                "headers": {"Idempotency-Key": "ract-send-1"},
                "body": '{"from":"admin@restmail.test","to":["admin@restmail.test"]}',
            }
        }
        original = copy.deepcopy(transaction)
        for authorization, expected in (("1", 1), ("0", 0)):
            with (
                self.subTest(authorization=authorization),
                patch.dict(
                    os.environ,
                    CONTRACT_AUTHORIZATION=authorization,
                    CONTRACT_COVERAGE="0",
                    CONTRACT_FUZZ="0",
                ),
                patch.object(module.rate_limits, "pace_sends") as pace,
            ):
                module.pace_the_coverage_sends(transaction)
                self.assertEqual(pace.call_count, expected)
                self.assertEqual(transaction, original)


if __name__ == "__main__":
    unittest.main()
