"""The test data operations start clean in the own-data run (#1334): each
exchange resets first, the reset must remove a domain made after that reset,
and the restore restores a snapshot taken through the API after it and must
put that data back."""

import json
import os
import unittest
from unittest.mock import Mock, patch

from test_hooks_pipeline import load_hooks

OPERATIONS = {
    "resetTestData": "/api/v1/admin/test/reset",
    "seedTestData": "/api/v1/admin/test/seed",
    "snapshotTestData": "/api/v1/admin/test/snapshot",
    "restoreTestSnapshot": "/api/v1/admin/test/snapshot/restore",
}

SEEDED = {
    "domains": [{"name": "restmail.test"}],
    "mailboxes": [{"address": "admin@restmail.test"}, {"address": "eve@restmail.test"}],
    "aliases": [{"source_address": "postmaster@restmail.test"}],
    "messages": [{}],
    "queue": [],
    "pipelines": [],
    "bans": [],
    "snapshot_at": "2026-11-04T00:00:00Z",
}


def transaction(path, name, body=None):
    return {
        "protocol": "http:", "host": "api", "port": "8080",
        "fullPath": path, "name": name,
        "request": {"body": json.dumps(body), "headers": {"Authorization": "Bearer session"}},
    }


def named(path, status):
    return f"{path} > summary > {status} > application/json"


class CleanStart(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.before = self.hooks.the_test_data_operations_after_a_reset
        self.after_hook = self.hooks.the_wipe_did_what_it_says
        self.globals = self.before.__globals__
        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        self.start_clean = Mock()
        names["start_clean"] = self.start_clean
        self.made = []
        self.snapshots = [SEEDED]

        def fixture_call(transaction, method, path, body=None, expect=(200, 201, 204)):
            self.made.append((method, path, body))
            if path == "/api/v1/admin/test/snapshot":
                return self.snapshots.pop(0) if len(self.snapshots) > 1 else self.snapshots[0]
            return {"id": 7}

        names["fixture_call"] = fixture_call
        self.stash = self.globals["hooks"].stash
        self.stash.clear()
        self.clean(True)

    def clean(self, on):
        env = patch.dict("os.environ")
        env.start()
        self.addCleanup(env.stop)
        os.environ.pop("CONTRACT_RESET_SEED", None)
        if on:
            os.environ["CONTRACT_RESET_SEED"] = "1"

    def set_json(self, sent, body):
        sent["request"]["body"] = json.dumps(body)

    def send(self, sent, status="200"):
        with patch.object(self.globals["hooks"], "set_json", self.set_json, create=True):
            self.before(sent)
        sent["real"] = {"statusCode": status, "body": "{}"}
        self.after_hook(sent)
        return sent

    def test_each_exchange_resets_first_and_a_refusal_keeps_its_request(self):
        for operation, path in OPERATIONS.items():
            for status in ("200", "422"):
                with self.subTest(operation=operation, status=status):
                    self.start_clean.reset_mock()
                    sent = transaction(path, named(path, status), {"unexpected": True})
                    with patch.object(self.globals["hooks"], "set_json", self.set_json, create=True):
                        self.before(sent)
                    self.start_clean.assert_called_once_with(sent)
                    self.assertEqual(sent["fullPath"], path)
                    if status != "200" or operation not in ("resetTestData", "restoreTestSnapshot"):
                        self.assertEqual(json.loads(sent["request"]["body"]), {"unexpected": True})

    def test_the_reset_must_remove_the_domain_made_after_its_reset(self):
        path = OPERATIONS["resetTestData"]
        sent = self.send(transaction(path, named(path, "200"), {}))
        self.assertIn(("POST", "/api/v1/admin/domains",
                       {"name": self.globals["OWN_DOMAIN"], "server_type": "traditional"}), self.made)
        self.assertNotIn("fail", sent)

        # A reset that left it fails.
        self.made.clear()
        left = dict(SEEDED, domains=SEEDED["domains"] + [{"name": self.globals["OWN_DOMAIN"]}])
        self.snapshots = [left]
        sent = self.send(transaction(path, named(path, "200"), {}))
        self.assertIn("left contract-own-domain.test", sent["fail"])

        # So does one that did not seed restmail.test again.
        self.snapshots = [dict(SEEDED, domains=[])]
        sent = self.send(transaction(path, named(path, "200"), {}))
        self.assertIn("did not seed restmail.test", sent["fail"])

    def test_the_restore_restores_a_snapshot_taken_after_its_reset(self):
        path = OPERATIONS["restoreTestSnapshot"]
        sent = self.send(transaction(path, named(path, "200"), {}))
        self.assertEqual(json.loads(sent["request"]["body"]), SEEDED)
        self.assertEqual([m for m in self.made if m[1] == "/api/v1/admin/test/snapshot"],
                         [("POST", "/api/v1/admin/test/snapshot", None)] * 2)
        self.assertNotIn("fail", sent)

    def test_a_restore_that_did_not_put_the_data_back_fails(self):
        path = OPERATIONS["restoreTestSnapshot"]
        cleared = {key: [] for key in ("domains", "mailboxes", "aliases", "messages", "queue", "pipelines", "bans")}
        self.snapshots = [SEEDED, cleared]
        sent = self.send(transaction(path, named(path, "200"), {}))
        self.assertEqual(
            sent["fail"],
            "the restore did not put back what its snapshot held (aliases, domains, mailboxes, messages)",
        )

    def test_a_snapshot_with_no_domains_stops_the_restore(self):
        path = OPERATIONS["restoreTestSnapshot"]
        self.snapshots = [{"domains": []}]
        with self.assertRaises(RuntimeError):
            self.before(transaction(path, named(path, "200"), {}))

    def test_a_200_with_nothing_made_for_it_fails(self):
        for operation in ("resetTestData", "restoreTestSnapshot"):
            with self.subTest(operation=operation):
                path = OPERATIONS[operation]
                sent = transaction(path, named(path, "200"), {})
                sent["real"] = {"statusCode": "200", "body": "{}"}
                self.after_hook(sent)
                self.assertIn("nothing was made", sent["fail"])

    def test_refusals_and_other_statuses_are_not_judged(self):
        for operation in ("resetTestData", "restoreTestSnapshot"):
            with self.subTest(operation=operation):
                path = OPERATIONS[operation]
                sent = self.send(transaction(path, named(path, "422"), {"unexpected": True}), "422")
                self.assertNotIn("fail", sent)
                # A 200 answered otherwise is the status check's to fail.
                sent = transaction(path, named(path, "200"), {})
                with patch.object(self.globals["hooks"], "set_json", self.set_json, create=True):
                    self.before(sent)
                sent["real"] = {"statusCode": "500", "body": "{}"}
                self.after_hook(sent)
                self.assertNotIn("fail", sent)

    def test_a_run_with_no_reset_changes_nothing(self):
        self.clean(False)
        for operation, path in OPERATIONS.items():
            with self.subTest(operation=operation):
                sent = self.send(transaction(path, named(path, "200"), {}))
                self.start_clean.assert_not_called()
                self.assertEqual(self.made, [])
                self.assertEqual(json.loads(sent["request"]["body"]), {})
                self.assertNotIn("fail", sent)


if __name__ == "__main__":
    unittest.main()
