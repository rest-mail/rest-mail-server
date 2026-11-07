"""Grant exchanges start clean in the own-data phase, refusals included, and
deleteGrant makes the grant it deletes, and its holder, after the reset (#1385)."""

import io
import json
import unittest
from unittest.mock import patch

from test_hooks_delete_domain import load_hooks

GRANTS = "/api/v1/admin/grants"

# The grant operations whose examples need nothing made for them, with a path
# each of their examples is sent to.
NOTHING_MADE = {
    "createGrant": GRANTS,
    "activateGrant": GRANTS + "/2147483647/activate",
    "deactivateGrant": GRANTS + "/0/deactivate",
    "makeGrantEligible": GRANTS + "/2147483647/make-eligible",
    "listGrants": GRANTS + "?limit=not-an-integer",
    "listMyGrants": GRANTS + "/mine",
}


class Grants(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, self.registered = load_hooks()
        self.events = []
        self.start_clean = patch.object(
            self.module,
            "start_clean",
            side_effect=lambda transaction: self.events.append("reset"),
        ).start()
        patch.object(self.module, "mfa_admin_token", return_value="two-factor").start()
        self.serial = 40

        def urlopen(request, timeout=10):
            self.events.append((request.get_method(), request.full_url.split("8080", 1)[1]))
            self.serial += 1
            return io.BytesIO(json.dumps({"data": {"id": self.serial}}).encode())

        patch.object(self.module.urllib.request, "urlopen", urlopen).start()
        self.addCleanup(patch.stopall)

    def transaction(self, path):
        return {
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": "8080",
            "fullPath": path,
            "name": path,
            "request": {"body": "{}", "headers": {}},
        }

    def clean_starts_on(self):
        patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"}).start()

    def test_each_grant_operation_has_a_hook(self):
        # test_hooks_own_data.py names the hook each one resets in.
        for operation in list(NOTHING_MADE) + ["deleteGrant"]:
            self.assertIn(operation, self.registered)

    def test_without_a_reset_nothing_resets(self):
        for operation, path in NOTHING_MADE.items():
            with self.subTest(operation=operation):
                transaction = self.transaction(path)
                self.module.a_grant_operation_from_a_clean_start(transaction)
                self.assertEqual(transaction["fullPath"], path)
        made = self.transaction(GRANTS + "/1")
        self.module.delete_a_grant_made_for_it(made)
        self.start_clean.assert_not_called()
        self.assertEqual(made["fullPath"], GRANTS + "/42")

    def test_each_exchange_starts_clean_and_changes_nothing_else(self):
        self.clean_starts_on()
        for operation, path in NOTHING_MADE.items():
            with self.subTest(operation=operation):
                self.events.clear()
                transaction = self.transaction(path)
                self.module.a_grant_operation_from_a_clean_start(transaction)
                self.assertEqual(self.events, ["reset"])
                self.assertEqual(transaction["fullPath"], path)
                self.assertEqual(transaction["request"]["body"], "{}")

    def test_the_deleted_grant_and_its_holder_are_made_after_the_reset(self):
        self.clean_starts_on()
        transaction = self.transaction(GRANTS + "/1")
        self.module.delete_a_grant_made_for_it(transaction)
        self.assertEqual(
            self.events,
            ["reset", ("POST", "/api/v1/accounts"), ("POST", GRANTS)],
        )
        self.assertEqual(transaction["fullPath"], GRANTS + "/42")

    def test_the_delete_refusals_start_clean_and_keep_their_ids(self):
        self.clean_starts_on()
        for grant_id in ("0", "9", "2147483647"):
            with self.subTest(id=grant_id):
                self.events.clear()
                transaction = self.transaction(GRANTS + "/" + grant_id)
                self.module.delete_a_grant_made_for_it(transaction)
                self.assertEqual(self.events, ["reset"])
                self.assertEqual(transaction["fullPath"], GRANTS + "/" + grant_id)

    def test_nothing_is_carried_in_the_stash(self):
        self.clean_starts_on()
        before = dict(self.hooks.stash)
        for path in NOTHING_MADE.values():
            self.module.a_grant_operation_from_a_clean_start(self.transaction(path))
        self.module.delete_a_grant_made_for_it(self.transaction(GRANTS + "/1"))
        self.assertEqual(dict(self.hooks.stash), before)


if __name__ == "__main__":
    unittest.main()
