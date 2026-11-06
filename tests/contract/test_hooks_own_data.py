"""Moved exchanges start clean and make their own target; links survive a reset (#1385)."""

import copy
import importlib.util
import json
import os
import re
import sys
import threading
import types
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "tests/contract"))
import own_data

POLICY = re.compile(r"^/api/v1/admin/domains/(\d+)/mta-sts$")
KEY = re.compile(r"^/api/v1/admin/dkim/(\d+)$")


class FixtureAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def answer(self, status, value=None):
        payload = json.dumps({"data": value}).encode() if value is not None else b""
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self):
        raw = self.rfile.read(int(self.headers["Content-Length"] or 0))
        body = json.loads(raw) if raw else None
        self.server.calls.append(("POST", self.path, body))
        if self.path == "/api/v1/admin/test/reset":
            if self.server.reset_failure:
                return self.answer(self.server.reset_failure)
            self.server.domains = {1: "restmail.test"}
            self.server.policies.clear()
            self.server.keys.clear()
            self.server.serial = 3
            return self.answer(200, {"status": "reset_complete"})
        if self.path == "/api/v1/admin/test/seed":
            return self.answer(200, {"status": "seed_complete"})
        if self.path != "/api/v1/admin/domains":
            return self.answer(404)
        if body["name"] in self.server.domains.values():
            return self.answer(409)
        self.server.serial += 1
        self.server.domains[self.server.serial] = body["name"]
        self.answer(201, {"id": self.server.serial, "name": body["name"]})

    def do_PUT(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.calls.append(("PUT", self.path, body))
        for pattern, store in ((POLICY, self.server.policies), (KEY, self.server.keys)):
            match = pattern.fullmatch(self.path)
            if match and int(match[1]) in self.server.domains:
                store[int(match[1])] = body
                return self.answer(200, body)
        self.answer(404)


class OwnDataHooks(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureAPI)
        self.server.calls = []
        self.server.domains = {1: "restmail.test", 3: "created.test"}
        self.server.policies = {}
        self.server.keys = {}
        self.server.serial = 40
        self.server.reset_failure = None
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.stub = types.ModuleType("vertrag_hooks")
        self.stub.stash = {}
        self.registered = []
        self.stub.before = lambda name: (
            lambda fn: self.registered.append((name, fn.__name__)) or fn
        )
        self.stub.after = lambda _: lambda fn: fn
        self.stub.get_json = lambda transaction, which="request": json.loads(
            transaction[which]["body"]
        )
        spec = importlib.util.spec_from_file_location(
            "contract_own_data_hooks", ROOT / "tests/contract/hooks.py"
        )
        self.hooks = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": self.stub}):
            spec.loader.exec_module(self.hooks)
        origin = f"http://127.0.0.1:{self.server.server_port}"
        self.stub.stash["setup_admin_tokens"] = {origin: "setup-session"}

    def clean_starts_on(self):
        patcher = patch.dict(os.environ, {"CONTRACT_RESET_SEED": "1"})
        patcher.start()
        self.addCleanup(patcher.stop)

    def transaction(self, status, path, name="/x > X"):
        return {
            "name": f"{name} > {status} > application/json",
            "protocol": "http:",
            "host": "127.0.0.1",
            "port": str(self.server.server_port),
            "fullPath": path,
            "request": {
                "headers": {"Authorization": "Bearer original-session"},
                "body": "null",
            },
            "expected": {"statusCode": str(status)},
        }

    def the_run_created_its_domain(self):
        self.stub.stash.update(
            domain_id=3, domain="created.test", linked_domain_ids=["3"]
        )

    def resets(self):
        return sum(call[1] == "/api/v1/admin/test/reset" for call in self.server.calls)

    def test_each_moved_operation_starts_clean(self):
        # The hook each moved operation resets in.
        hooks = {
            "getDomain": "get_a_domain_of_its_own",
            "setMtaStsPolicy": "prepare_mta_sts_policy_example",
            "getMtaStsPolicy": "prepare_mta_sts_policy_example",
            "deleteMtaStsPolicy": "prepare_mta_sts_policy_example",
            "createPipeline": "a_pipeline_on_the_created_domain",
            "testPipeline": "test_the_created_pipeline",
            "deletePipeline": "delete_a_pipeline_of_its_own",
            "createAlias": "remove_a_previous_alias_probe",
            "getAlias": "on_an_alias_of_its_own",
            "updateAlias": "on_an_alias_of_its_own",
            "deleteAlias": "on_an_alias_of_its_own",
            "createCustomFilter": "a_custom_filter_on_the_created_domain",
            "getCustomFilter": "on_a_custom_filter_of_its_own",
            "updateCustomFilter": "on_a_custom_filter_of_its_own",
            "testCustomFilter": "on_a_custom_filter_of_its_own",
            "deleteCustomFilter": "on_a_custom_filter_of_its_own",
            "createFolder": "remove_a_previous_folder_probe",
            "renameFolder": "give_each_rename_probe_a_folder",
            "deleteFolder": "a_folder_to_delete_of_its_own",
            "updateDomain": "a_certificate_before_going_live",
            "inviteAccount": "invite_an_account_without_credentials",
            "acceptInvitation": "redeem_a_fresh_invitation",
            "createGrant": "a_grant_operation_from_a_clean_start",
            "deleteGrant": "delete_a_grant_made_for_it",
            "activateGrant": "a_grant_operation_from_a_clean_start",
            "deactivateGrant": "a_grant_operation_from_a_clean_start",
            "makeGrantEligible": "a_grant_operation_from_a_clean_start",
            "listGrants": "a_grant_operation_from_a_clean_start",
            "listMyGrants": "a_grant_operation_from_a_clean_start",
            "listAllowlist": "list_sender_rules_from_a_clean_start",
            "listBlocklist": "list_sender_rules_from_a_clean_start",
            "addToAllowlist": "remove_a_previous_sender_rule_probe",
            "addToBlocklist": "remove_a_previous_sender_rule_probe",
            "removeFromAllowlist": "on_a_sender_rule_of_its_own",
            "removeFromBlocklist": "on_a_sender_rule_of_its_own",
            "createContact": "remove_a_previous_contact_probe",
            "updateContact": "on_a_contact_of_its_own",
            "deleteContact": "on_a_contact_of_its_own",
            "blockSender": "block_a_new_and_an_existing_sender",
            "listContacts": "a_contact_operation_from_a_clean_start",
            "importContacts": "a_contact_operation_from_a_clean_start",
            "suggestContacts": "a_contact_operation_from_a_clean_start",
            "getContactReplyTimeSetting": "a_contact_operation_from_a_clean_start",
            "setContactReplyTimeSetting": "a_contact_operation_from_a_clean_start",
        }
        self.assertEqual(set(hooks), set(own_data.MOVED))
        for operation, hook in hooks.items():
            self.assertIn((operation, hook), self.registered)

    def test_each_unmoved_follower_gets_a_live_domain(self):
        moved_itself = set(own_data.MOVED) | {
            "createDomain",
            "deleteDomain",
            "mtaStsPolicyFile",
        }
        followers = set(own_data.FOLLOWS_THE_CREATED_DOMAIN) - moved_itself
        self.assertEqual(
            followers - {"setDomainRelay"},
            {o for o, h in self.registered if h == "a_live_domain_for_the_link"},
        )
        self.assertIn(
            'before("setDomainRelay")(shared["a_live_domain_for_the_link"])',
            (ROOT / "tests/contract/entrypoint.py").read_text(),
        )
        # It runs first, so later hooks of the operation read the path it leaves.
        for operation in followers - {"setDomainRelay"}:
            names = [h for o, h in self.registered if o == operation]
            self.assertEqual(names[0], "a_live_domain_for_the_link", operation)

    def test_without_the_switch_nothing_resets_or_moves(self):
        self.the_run_created_its_domain()
        stash = copy.deepcopy(self.stub.stash)
        cases = (
            (
                self.hooks.get_a_domain_of_its_own,
                self.transaction(200, "/api/v1/admin/domains/3"),
            ),
            (
                self.hooks.a_live_domain_for_the_link,
                self.transaction(200, "/api/v1/admin/domains/3/relay"),
            ),
        )
        for hook, transaction in cases:
            before = copy.deepcopy(transaction)
            hook(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])
        self.assertEqual(self.stub.stash, stash)

    def test_get_makes_its_own_domain_after_the_reset(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        transaction = self.transaction(200, "/api/v1/admin/domains/3?keep=yes")
        headers = copy.deepcopy(transaction["request"])
        self.hooks.get_a_domain_of_its_own(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/4?keep=yes")
        self.assertEqual(transaction["request"], headers)
        self.assertEqual(
            [call[1] for call in self.server.calls],
            [
                "/api/v1/admin/test/reset",
                "/api/v1/admin/test/seed",
                "/api/v1/admin/domains",
            ],
        )
        self.assertNotIn(3, self.server.domains)

    def test_refusals_reset_and_keep_their_ids(self):
        self.clean_starts_on()
        for status, path in (
            (404, "/api/v1/admin/domains/2147483647"),
            (422, "/api/v1/admin/domains/0"),
        ):
            transaction = self.transaction(status, path)
            before = copy.deepcopy(transaction)
            self.hooks.get_a_domain_of_its_own(transaction)
            self.assertEqual(transaction, before)
        for status, path in (
            (404, "/api/v1/admin/domains/2147483647/mta-sts"),
            (404, "/api/v1/admin/domains/2147483646/mta-sts"),
            (422, "/api/v1/admin/domains/1/mta-sts"),
        ):
            transaction = self.transaction(status, path)
            before = copy.deepcopy(transaction)
            self.hooks.prepare_mta_sts_policy_example(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.resets(), 5)
        self.assertFalse(
            [call for call in self.server.calls if call[1] == "/api/v1/admin/domains"]
        )

    def test_policy_create_starts_without_one_and_the_others_with_one(self):
        self.clean_starts_on()
        for status, policy in ((201, False), (200, True), (204, True)):
            transaction = self.transaction(status, "/api/v1/admin/domains/3/mta-sts")
            self.hooks.prepare_mta_sts_policy_example(transaction)
            self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/4/mta-sts")
            self.assertEqual(4 in self.server.policies, policy)
        self.assertEqual(self.resets(), 3)
        self.assertNotIn("mta_sts_create_domains", self.stub.stash)

    def test_a_failed_reset_stops_the_exchange(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        self.server.reset_failure = 500
        stash = copy.deepcopy(self.stub.stash)
        for hook, path in (
            (self.hooks.get_a_domain_of_its_own, "/api/v1/admin/domains/3"),
            (
                self.hooks.prepare_mta_sts_policy_example,
                "/api/v1/admin/domains/3/mta-sts",
            ),
        ):
            transaction = self.transaction(200, path)
            before = copy.deepcopy(transaction)
            with self.assertRaisesRegex(RuntimeError, "resetTestData failed: HTTP 500"):
                hook(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.stub.stash, stash)
        self.assertFalse(
            [call for call in self.server.calls if call[1] == "/api/v1/admin/domains"]
        )

    def test_a_reset_forgets_all_but_the_sessions_and_the_links(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        self.stub.stash.update(
            mfa_admin_sessions={"o": {"token": "t", "at": 1}},
            created_sender_rules={"/x/allowlist": {"id": 1, "body": {}}},
            pipeline_id=9,
            probe_domain_id=8,
            policy_absent_domains={"o": 7},
        )
        self.hooks.get_a_domain_of_its_own(
            self.transaction(422, "/api/v1/admin/domains/0")
        )
        self.assertEqual(set(self.stub.stash), set(self.hooks.SURVIVES_A_RESET))
        self.assertEqual(self.stub.stash["clean_starts"], 1)

    def test_the_stale_key_example_runs_on_a_live_domain(self):
        transaction = self.transaction(200, "/api/v1/admin/domains/7/dns?x=1")
        self.hooks.the_stale_key_example_on_a_live_domain(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/1/dns?x=1")
        for status, path in (
            (404, "/api/v1/admin/domains/7/dns"),
            (200, "/api/v1/admin/domains/7/relay"),
            (200, "/api/v1/admin/domains/6/dns"),
        ):
            transaction = self.transaction(status, path)
            self.hooks.the_stale_key_example_on_a_live_domain(transaction)
            self.assertEqual(transaction["fullPath"], path)

    def test_a_link_gets_one_stand_in_per_reset_and_other_ids_stay(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        self.hooks.get_a_domain_of_its_own(
            self.transaction(422, "/api/v1/admin/domains/0")
        )
        for path in (
            "/api/v1/admin/domains/3/relay",
            "/api/v1/admin/domains/3/dns?x=1",
            "/api/v1/admin/dkim/3/rotation",
        ):
            transaction = self.transaction(200, path)
            self.hooks.a_live_domain_for_the_link(transaction)
            self.assertEqual(transaction["fullPath"], path.replace("/3", "/4"))
        self.assertEqual(
            sum(call[1] == "/api/v1/admin/domains" for call in self.server.calls), 1
        )
        self.assertEqual(self.stub.stash["domain_id"], 4)
        self.assertEqual(self.stub.stash["domain"], "contract-linked-domain.test")
        for path in (
            "/api/v1/admin/domains/1/relay",
            "/api/v1/admin/domains/0/dns",
            "/api/v1/admin/domains/not-an-integer/relay",
            "/api/v1/admin/domains/2147483647/relay",
        ):
            transaction = self.transaction(422, path)
            self.hooks.a_live_domain_for_the_link(transaction)
            self.assertEqual(transaction["fullPath"], path)
        # The next reset removes the stand-in too. A link that carried its id, as
        # $request.path does, gets the next one, whatever id that has.
        self.hooks.get_a_domain_of_its_own(
            self.transaction(422, "/api/v1/admin/domains/0")
        )
        self.server.serial = 8
        transaction = self.transaction(200, "/api/v1/admin/domains/4/relay")
        self.hooks.a_live_domain_for_the_link(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/domains/9/relay")
        self.assertNotIn(4, self.server.domains)

    def test_a_linked_key_is_set_again_before_its_delete(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        self.hooks.get_a_domain_of_its_own(
            self.transaction(422, "/api/v1/admin/domains/0")
        )
        transaction = self.transaction(204, "/api/v1/admin/dkim/3")
        self.hooks.a_live_domain_for_the_link(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/dkim/4")
        self.assertIn("PRIVATE KEY", self.server.keys[4]["private_key"])
        self.assertEqual(self.server.keys[4]["selector"], "contract")

    def test_the_policy_file_asks_for_a_domain_that_exists_after_a_reset(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        self.hooks.get_a_domain_of_its_own(
            self.transaction(422, "/api/v1/admin/domains/0")
        )
        transaction = self.transaction(404, "/.well-known/mta-sts.txt")
        self.hooks.ask_for_the_created_domains_policy(transaction)
        self.assertEqual(
            transaction["request"]["headers"]["Host"],
            "mta-sts.contract-linked-domain.test",
        )

    def test_the_created_domain_is_kept_for_the_links(self):
        data = {"id": 3, "name": "created.test"}
        self.stub.get_json = lambda *_: {"data": data}
        self.hooks.hooks = self.stub
        self.hooks.keep_the_created_domain({})
        self.assertEqual(self.stub.stash["linked_domain_ids"], ["3"])


if __name__ == "__main__":
    unittest.main()
