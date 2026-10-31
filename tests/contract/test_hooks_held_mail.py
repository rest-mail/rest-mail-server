"""deleteQuarantine and clearSuppression act on the rows the contract fixtures
hold for them, and only for their found examples (#1334)."""

import io
import json
import unittest
from unittest.mock import patch

from test_hooks_two_factor_admin import load_hooks, session, transaction


def answer(data):
    return io.BytesIO(json.dumps({"data": data}).encode())


class HeldMailHookTests(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, self.registered = load_hooks()
        self.urlopen = patch.object(self.module.urllib.request, "urlopen").start()
        self.addCleanup(patch.stopall)

    def answers(self, *bodies):
        self.urlopen.return_value.__enter__.side_effect = [answer(b) for b in bodies]

    def test_both_are_hooked(self):
        self.assertIn("delete_the_fixture_quarantined_message", self.registered["deleteQuarantine"])
        self.assertIn("clear_the_fixture_suppression", self.registered["clearSuppression"])

    def test_found_deletes_the_newest_unreleased_message(self):
        self.answers([{"id": 7, "released": False}, {"id": 9, "released": True}, {"id": 8, "released": False}])
        t = transaction("/api/v1/accounts/1/quarantine/41", session(self.module.ADMIN))
        self.module.delete_the_fixture_quarantined_message(t)
        self.assertEqual(t["fullPath"], "/api/v1/accounts/1/quarantine/8")
        request = self.urlopen.call_args.args[0]
        self.assertEqual(request.full_url, "http://api:8080/api/v1/accounts/1/quarantine?limit=100")

    def test_no_held_message_is_an_error(self):
        self.answers([{"id": 9, "released": True}])
        t = transaction("/api/v1/accounts/1/quarantine/41", session(self.module.ADMIN))
        with self.assertRaises(RuntimeError):
            self.module.delete_the_fixture_quarantined_message(t)

    def test_other_messages_and_principals_are_sent_as_they_are(self):
        for path, authorization in (("/api/v1/accounts/1/quarantine/2147483647", session(self.module.ADMIN)),
                                    ("/api/v1/accounts/1/quarantine/41", None),
                                    ("/api/v1/accounts/1/quarantine/41", session("contract-ordinary@restmail.test"))):
            with self.subTest(path=path, authorization=authorization):
                t = transaction(path, authorization)
                self.module.delete_the_fixture_quarantined_message(t)
                self.assertEqual(t["fullPath"], path)
        self.urlopen.assert_not_called()

    def test_found_clears_the_newest_fixture_suppression(self):
        self.answers([{"id": 4, "name": "restmail.test"}],
                     [{"id": 3, "address": "contract-suppressed-1@nowhere.invalid"},
                      {"id": 6, "address": "someone@else.test"},
                      {"id": 5, "address": "contract-suppressed-2@nowhere.invalid"}])
        t = transaction("/api/v1/admin/domains/1/suppressions/1", session(self.module.ADMIN))
        self.module.clear_the_fixture_suppression(t)
        self.assertEqual(t["fullPath"], "/api/v1/admin/domains/4/suppressions/5")

    def test_other_suppressions_are_sent_as_they_are(self):
        for sid in ("2147483647", "not-an-integer", "3"):
            with self.subTest(sid=sid):
                t = transaction("/api/v1/admin/domains/1/suppressions/" + sid, session(self.module.ADMIN))
                self.module.clear_the_fixture_suppression(t)
                self.assertEqual(t["fullPath"], "/api/v1/admin/domains/1/suppressions/" + sid)
        self.urlopen.assert_not_called()


if __name__ == "__main__":
    unittest.main()
