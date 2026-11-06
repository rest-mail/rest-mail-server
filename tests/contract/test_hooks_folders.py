"""Folder exchanges start clean in the own-data phase, and nothing names the
rename probes by a count carried between exchanges (#1385)."""

import io
import json
import unittest
from unittest.mock import patch

from test_hooks_delete_domain import load_hooks

ACCOUNT = "/api/v1/accounts/{}/folders"


class Folders(unittest.TestCase):
    def setUp(self):
        self.module, self.hooks, registered = load_hooks()
        for operation in ("createFolder", "renameFolder", "deleteFolder"):
            self.assertIn(operation, registered)
        self.hooks.get_json = lambda transaction, which="request": json.loads(
            transaction[which]["body"]
        )
        self.made = []

        def fixture_call(transaction, method, path, body=None, expect=(200, 201, 204)):
            self.made.append((method, path, body))
            return {"name": body["name"]} if body else None

        patch.object(self.module, "fixture_call", fixture_call).start()
        self.start_clean = patch.object(self.module, "start_clean").start()
        self.mailbox = patch.object(self.module, "a_mailbox_of_its_own", return_value=16).start()
        patch.object(self.module, "admin_token", return_value="setup").start()
        # The rename hook removes an earlier rename's destination: none here.
        self.deleted = []

        def urlopen(request, timeout=10):
            self.deleted.append((request.get_method(), request.full_url))
            raise self.module.urllib.error.HTTPError(request.full_url, 404, "", {}, io.BytesIO())

        patch.object(self.module.urllib.request, "urlopen", urlopen).start()
        self.addCleanup(patch.stopall)

    def transaction(self, path, status, body=None, name="Rename a folder"):
        return {
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": "8080",
            "fullPath": path,
            "name": f"{path} > {name} > {status} > application/json",
            "request": {"body": json.dumps(body)},
        }

    def clean_starts_on(self):
        patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"}).start()

    def test_without_a_reset_each_rename_gets_a_new_folder_and_no_count(self):
        paths = []
        for _ in range(2):
            transaction = self.transaction(ACCOUNT.format(1) + "/Work", 200, {"name": "Done"})
            self.module.give_each_rename_probe_a_folder(transaction)
            paths.append(transaction["fullPath"])
        self.assertEqual(len(set(paths)), 2)
        for path, (method, parent, body) in zip(paths, self.made):
            self.assertEqual((method, parent), ("POST", ACCOUNT.format(1)))
            self.assertEqual(path, ACCOUNT.format(1) + "/" + body["name"])
            self.assertTrue(body["name"].startswith("contract-rename-"))
        self.assertNotIn("rename_probe", self.hooks.stash)
        self.start_clean.assert_not_called()
        self.mailbox.assert_not_called()

    def test_without_a_reset_a_delete_is_left_alone(self):
        transaction = self.transaction(ACCOUNT.format(1) + "/Work", 204, name="Delete a folder")
        original = json.loads(json.dumps(transaction))
        self.module.a_folder_to_delete_of_its_own(transaction)
        self.assertEqual(transaction, original)
        self.assertEqual(self.made, [])
        self.start_clean.assert_not_called()

    def test_after_a_reset_a_create_goes_to_a_mailbox_of_its_own_as_documented(self):
        self.clean_starts_on()
        self.hooks.stash["created_folders"] = {"stale": True}
        for account, sent in (("1", "16"), ("0", "0"), ("2147483647", "2147483647"), ("x", "x")):
            with self.subTest(account=account):
                transaction = self.transaction(ACCOUNT.format(account), 201, {"name": "Receipts"})
                self.module.remove_a_previous_folder_probe(transaction)
                self.start_clean.assert_called_once_with(transaction)
                self.start_clean.reset_mock()
                self.assertEqual(transaction["fullPath"], ACCOUNT.format(sent))
                self.assertEqual(json.loads(transaction["request"]["body"]), {"name": "Receipts"})
        # Nothing is looked up or removed first: the reset removed it.
        self.assertEqual(self.made, [])
        self.assertEqual(self.deleted, [])

    def test_after_a_reset_every_rename_starts_clean_in_a_mailbox_of_its_own(self):
        self.clean_starts_on()
        for account, status in (("1", 200), ("0", 422)):
            with self.subTest(account=account):
                transaction = self.transaction(ACCOUNT.format(account) + "/Work", status, {"name": "Done"})
                self.module.give_each_rename_probe_a_folder(transaction)
                self.start_clean.assert_called_once_with(transaction)
                self.start_clean.reset_mock()
        self.assertEqual(len(self.made), 1)
        method, parent, body = self.made[0]
        self.assertEqual((method, parent), ("POST", ACCOUNT.format(16)))
        self.assertEqual(self.mailbox.call_count, 1)

    def test_after_a_reset_a_delete_that_succeeds_gets_a_folder_of_its_own(self):
        self.clean_starts_on()
        transaction = self.transaction(
            ACCOUNT.format(1) + "/Renamed?x=1", 204, name="Delete a folder"
        )
        self.module.a_folder_to_delete_of_its_own(transaction)
        self.start_clean.assert_called_once_with(transaction)
        [(method, parent, body)] = self.made
        self.assertEqual((method, parent), ("POST", ACCOUNT.format(16)))
        self.assertTrue(body["name"].startswith("contract-delete-"))
        self.assertEqual(transaction["fullPath"], ACCOUNT.format(16) + "/" + body["name"] + "?x=1")

    def test_after_a_reset_refused_deletes_keep_their_folders(self):
        self.clean_starts_on()
        for account, folder, status, sent in (
            ("1", "Missing", 404, "16"),
            ("1", "Archive", 422, "16"),
            ("0", "Work", 422, "0"),
            ("2147483647", "Work", 404, "2147483647"),
        ):
            with self.subTest(account=account, folder=folder):
                transaction = self.transaction(
                    ACCOUNT.format(account) + "/" + folder, status, name="Delete a folder"
                )
                self.module.a_folder_to_delete_of_its_own(transaction)
                self.start_clean.assert_called_once_with(transaction)
                self.start_clean.reset_mock()
                self.assertEqual(transaction["fullPath"], ACCOUNT.format(sent) + "/" + folder)
        self.assertEqual(self.made, [])


class MailboxOfItsOwn(unittest.TestCase):
    def test_it_is_made_on_a_domain_of_its_own_with_a_second_factor(self):
        module, _, _ = load_hooks()
        calls = []

        class Response(io.BytesIO):
            def __enter__(self):
                return self

            def __exit__(self, *args):
                return False

        def urlopen(request, timeout=10):
            calls.append(request)
            return Response(b'{"data":{"id":16}}')

        with patch.object(module, "a_domain_of_its_own") as domain, patch.object(
            module, "mfa_admin_token", return_value="second-factor"
        ), patch.object(module.urllib.request, "urlopen", urlopen):
            transaction = {"protocol": "http:", "host": "h", "port": "1"}
            self.assertEqual(module.a_mailbox_of_its_own(transaction), 16)
        domain.assert_called_once_with(transaction)
        [request] = calls
        self.assertEqual((request.get_method(), request.full_url), ("POST", "http://h:1/api/v1/accounts"))
        self.assertEqual(request.get_header("Authorization"), "Bearer second-factor")
        self.assertEqual(
            json.loads(request.data), {"address": "contract-own-folders@" + module.OWN_DOMAIN}
        )


if __name__ == "__main__":
    unittest.main()
