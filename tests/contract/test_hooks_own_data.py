"""Moved exchanges start clean and make their own target; links survive a reset (#1385)."""

import base64
import copy
import importlib.util
import json
import os
import re
import sys
import threading
import time
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
IMPORT = re.compile(r"^/api/v1/accounts/(\d+)/messages/import$")


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
            self.server.accounts.clear()
            self.server.serial = 3
            return self.answer(200, {"status": "reset_complete"})
        if self.path == "/api/v1/admin/test/seed":
            return self.answer(200, {"status": "seed_complete"})
        if self.path == "/api/v1/accounts":
            self.server.serial += 1
            self.server.accounts[self.server.serial] = (
                body["address"],
                self.headers["Authorization"],
            )
            return self.answer(201, {"id": self.server.serial})
        if IMPORT.fullmatch(self.path):
            self.server.imports.append((self.path, body))
            return self.answer(200, {"id": len(self.server.imports), "imported": True})
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
        self.server.accounts = {}
        self.server.imports = []
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
        self.stub.stash.update(domain_id=3, domain="created.test")

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
            "sendMessage": "a_sending_policy_for_the_send",
            "sendTestEmail": "a_mailbox_at_each_test_send_address",
            "getMessage": "on_a_message_of_its_own",
            "updateMessage": "on_a_message_of_its_own",
            "getRawMessage": "on_a_message_of_its_own",
            "forwardMessage": "on_a_message_of_its_own",
            "listAttachments": "on_a_message_of_its_own",
            "getMessageTrace": "on_a_message_of_its_own",
            "deleteMessage": "on_a_message_of_its_own",
            "verifyMessageSeal": "on_a_message_of_its_own",
            "getThread": "on_a_thread_of_its_own",
            "saveDraft": "a_mailbox_for_the_draft",
            "updateDraft": "on_a_draft_of_its_own",
            "sendDraft": "send_a_draft_of_its_own",
            "setDKIMKey": "a_dkim_domain_of_its_own",
            "deleteDKIMKey": "a_dkim_domain_of_its_own",
            "getDKIMRotation": "a_dkim_domain_of_its_own",
            "setDKIMRotation": "a_dkim_domain_of_its_own",
            "createDomain": "create_a_domain_that_is_not_there",
            "deleteDomain": "delete_only_the_created_domain",
            "domainDNSCheck": "a_domain_setting_of_its_own",
            "getDomainRelay": "a_domain_setting_of_its_own",
            "listSuppressions": "a_domain_setting_of_its_own",
            "mtaStsPolicyFile": "ask_for_the_created_domains_policy",
            "createAccount": "a_mailbox_to_administer",
            "listMailboxes": "a_mailbox_to_administer",
            "getMailbox": "a_mailbox_to_administer",
            "updateMailbox": "a_mailbox_to_administer",
            "deleteMailbox": "a_mailbox_to_administer",
            "endMailboxSessions": "a_mailbox_to_administer",
            "listThreads": "on_the_admins_mailbox_after_a_reset",
            "setThreadDue": "on_the_admins_mailbox_after_a_reset",
            "snoozeThread": "on_the_admins_mailbox_after_a_reset",
            "importMessage": "on_the_admins_mailbox_after_a_reset",
            "updatePipeline": "update_a_pipeline_of_its_own",
            "listPipelines": "pipeline_reads_after_a_reset",
            "listPipelineLogs": "pipeline_reads_after_a_reset",
            "getPipelineAnalytics": "pipeline_reads_after_a_reset",
            "testFilter": "filter_requests_after_a_reset",
            "listCustomFilters": "filter_requests_after_a_reset",
            "validateCustomFilter": "filter_requests_after_a_reset",
        }
        # entrypoint.py registers setDomainRelay's (below).
        self.assertEqual(set(hooks) | {"setDomainRelay"}, set(own_data.MOVED))
        for operation, hook in hooks.items():
            self.assertIn((operation, hook), self.registered)

    def test_the_pipeline_operations_reset_in_their_first_hook(self):
        # It runs before the coverage placeholder's hook, so that one reads the
        # path it leaves.
        for operation, hook in (
            ("updatePipeline", "update_a_pipeline_of_its_own"),
            ("listPipelines", "pipeline_reads_after_a_reset"),
            ("listPipelineLogs", "pipeline_reads_after_a_reset"),
            ("getPipelineAnalytics", "pipeline_reads_after_a_reset"),
        ):
            names = [h for o, h in self.registered if o == operation]
            self.assertEqual(names[0], hook, operation)
        self.assertFalse(hasattr(self.hooks, "a_live_pipeline_for_the_link"))
        self.assertFalse(hasattr(self.hooks, "the_domain_since_the_reset"))

    def test_the_domain_settings_reset_in_their_first_hook(self):
        self.assertIn(
            'before("setDomainRelay")(shared["a_domain_setting_of_its_own"])',
            (ROOT / "tests/contract/entrypoint.py").read_text(),
        )
        self.assertNotIn(
            "setDomainRelay", {o for o, _ in self.registered}
        )
        # It runs first, so later hooks of the operation read the path it leaves.
        for operation in ("domainDNSCheck", "getDomainRelay", "listSuppressions"):
            names = [h for o, h in self.registered if o == operation]
            self.assertEqual(names[0], "a_domain_setting_of_its_own", operation)
        self.assertFalse(hasattr(self.hooks, "a_live_domain_for_the_link"))
        self.assertNotIn("linked_domain_ids", self.hooks.SURVIVES_A_RESET)

    def test_without_the_switch_nothing_resets_or_moves(self):
        self.the_run_created_its_domain()
        stash = copy.deepcopy(self.stub.stash)
        cases = (
            (
                self.hooks.get_a_domain_of_its_own,
                self.transaction(200, "/api/v1/admin/domains/3"),
            ),
            (
                self.hooks.a_domain_setting_of_its_own,
                self.transaction(200, "/api/v1/admin/domains/3/relay"),
            ),
            (
                self.hooks.delete_only_the_created_domain,
                self.transaction(422, "/api/v1/admin/domains/0"),
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

    def test_a_reset_forgets_all_but_the_sessions(self):
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

    def test_each_domain_setting_success_gets_a_domain_of_its_own(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        for path in (
            "/api/v1/admin/domains/3/relay",
            "/api/v1/admin/domains/3/dns?x=1",
            "/api/v1/admin/domains/1/suppressions",
        ):
            transaction = self.transaction(200, path)
            headers = copy.deepcopy(transaction["request"])
            self.hooks.a_domain_setting_of_its_own(transaction)
            # The reset left the seed's three domains, so the new one is 4.
            self.assertEqual(
                transaction["fullPath"], re.sub(r"/[13]/", "/4/", path, count=1)
            )
            self.assertEqual(transaction["request"], headers)
            self.assertEqual(self.server.domains[4], self.hooks.OWN_DOMAIN)
            self.assertNotIn("domain_id", self.stub.stash)
        self.assertEqual(self.resets(), 3)

    def test_domain_setting_refusals_reset_and_keep_their_ids(self):
        self.clean_starts_on()
        for status, path in (
            (422, "/api/v1/admin/domains/1/relay"),
            (422, "/api/v1/admin/domains/0/dns"),
            (422, "/api/v1/admin/domains/not-an-integer/suppressions"),
            (404, "/api/v1/admin/domains/2147483647/relay"),
        ):
            transaction = self.transaction(status, path)
            before = copy.deepcopy(transaction)
            self.hooks.a_domain_setting_of_its_own(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.resets(), 4)
        self.assertFalse(
            [call for call in self.server.calls if call[1] == "/api/v1/admin/domains"]
        )

    def test_the_create_and_the_delete_start_clean(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        create = self.transaction(422, "/api/v1/admin/domains")
        create["request"]["body"] = json.dumps({"name": ""})
        before = copy.deepcopy(create)
        self.hooks.create_a_domain_that_is_not_there(create)
        self.assertEqual(create, before)
        self.assertEqual(set(self.stub.stash), {"setup_admin_tokens"})
        for status, path in (
            (422, "/api/v1/admin/domains/0"),
            (404, "/api/v1/admin/domains/2147483647"),
        ):
            transaction = self.transaction(status, path)
            self.hooks.delete_only_the_created_domain(transaction)
            self.assertEqual(transaction["fullPath"], path)
            self.assertNotIn("skip", transaction)
        delete = self.transaction(204, "/api/v1/admin/domains/1", name="/x > Delete")
        delete["name"] = delete["name"].replace(" > application/json", "")
        self.hooks.delete_only_the_created_domain(delete)
        self.assertEqual(delete["fullPath"], "/api/v1/admin/domains/4")
        self.assertTrue(self.server.domains[4].startswith("contract-delete-"))
        self.assertEqual(self.resets(), 4)

    def test_each_dkim_exchange_resets_and_gets_a_domain_of_its_own(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        for path, status in (
            ("/api/v1/admin/dkim/3", 200),
            ("/api/v1/admin/dkim/3/rotation?x=1", 200),
        ):
            transaction = self.transaction(status, path)
            headers = copy.deepcopy(transaction["request"])
            self.hooks.a_dkim_domain_of_its_own(transaction)
            # The reset left the seed's three domains, so the new one is 4.
            self.assertEqual(transaction["fullPath"], path.replace("/3", "/4"))
            self.assertEqual(transaction["request"], headers)
            self.assertEqual(self.server.domains[4], self.hooks.OWN_DOMAIN)
            self.assertEqual(self.server.keys, {})
            self.assertLessEqual(set(self.stub.stash), set(self.hooks.SURVIVES_A_RESET))
            self.assertNotIn("domain_id", self.stub.stash)
        self.assertEqual(self.resets(), 2)

    def test_the_dkim_delete_gets_the_key_it_deletes(self):
        self.clean_starts_on()
        transaction = self.transaction(204, "/api/v1/admin/dkim/7", name="/x > Delete")
        transaction["name"] = transaction["name"].replace(" > application/json", "")
        self.hooks.a_dkim_domain_of_its_own(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/admin/dkim/4")
        self.assertIn("PRIVATE KEY", self.server.keys[4]["private_key"])
        self.assertEqual(self.server.keys[4]["selector"], "contract")

    def test_dkim_refusals_keep_their_ids_after_the_reset(self):
        self.clean_starts_on()
        for path in (
            "/api/v1/admin/dkim/0",
            "/api/v1/admin/dkim/2147483647/rotation",
            "/api/v1/admin/dkim/not-an-integer",
        ):
            transaction = self.transaction(404, path)
            self.hooks.a_dkim_domain_of_its_own(transaction)
            self.assertEqual(transaction["fullPath"], path)
        self.assertEqual(self.resets(), 3)
        self.assertNotIn(4, self.server.domains)
        self.assertEqual(self.server.keys, {})

    def test_without_the_switch_a_dkim_exchange_is_unchanged(self):
        self.the_run_created_its_domain()
        transaction = self.transaction(204, "/api/v1/admin/dkim/3")
        before = copy.deepcopy(transaction)
        self.hooks.a_dkim_domain_of_its_own(transaction)
        self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def two_factor_session(self):
        origin = f"http://127.0.0.1:{self.server.server_port}"
        self.stub.stash["mfa_admin_sessions"] = {
            origin: {"token": "two-factor-session", "at": time.time()}
        }

    def test_the_mailbox_operations_reset_in_their_first_hook(self):
        # It runs first, so the created account's hook and the two-factor swap
        # see the clean start and the path it leaves.
        for operation in (
            "createAccount",
            "listMailboxes",
            "getMailbox",
            "updateMailbox",
            "deleteMailbox",
            "endMailboxSessions",
        ):
            names = [h for o, h in self.registered if o == operation]
            self.assertEqual(names[0], "a_mailbox_to_administer", operation)

    def test_each_mailbox_exchange_resets_and_gets_an_account_of_its_own(self):
        self.clean_starts_on()
        self.two_factor_session()
        self.stub.stash["created_account"] = {"id": 9, "body": {"address": "x@y"}}
        for path, status in (
            ("/api/v1/admin/mailboxes/9", 200),
            ("/api/v1/admin/mailboxes/1", 422),
            ("/api/v1/admin/mailboxes/9", 204),
            ("/api/v1/admin/mailboxes/1/sessions?x=1", 204),
        ):
            transaction = self.transaction(status, path)
            request = copy.deepcopy(transaction["request"])
            self.hooks.a_mailbox_to_administer(transaction)
            account = max(self.server.accounts)
            self.assertEqual(
                transaction["fullPath"],
                re.sub(r"/mailboxes/\d+", f"/mailboxes/{account:d}", path),
            )
            address, session = self.server.accounts[account]
            self.assertRegex(address, r"^contract-own-mailbox-\d+@restmail\.test$")
            # Making an account takes the two-factor session; the request's
            # own credential and body stay as they are.
            self.assertEqual(session, "Bearer two-factor-session")
            self.assertEqual(transaction["request"], request)
            self.assertLessEqual(set(self.stub.stash), set(self.hooks.SURVIVES_A_RESET))
        self.assertEqual(self.resets(), 4)

    def test_mailbox_refusals_creates_and_lists_reset_and_keep_their_paths(self):
        self.clean_starts_on()
        for path in (
            "/api/v1/admin/mailboxes/0",
            "/api/v1/admin/mailboxes/2147483647",
            "/api/v1/admin/mailboxes/2147483647/sessions",
            "/api/v1/admin/mailboxes/not-an-integer",
            "/api/v1/admin/mailboxes?domain_id=1",
            "/api/v1/accounts",
        ):
            transaction = self.transaction(404, path)
            self.hooks.a_mailbox_to_administer(transaction)
            self.assertEqual(transaction["fullPath"], path)
        self.assertEqual(self.resets(), 6)
        self.assertEqual(self.server.accounts, {})

    def test_without_the_switch_a_mailbox_exchange_is_unchanged(self):
        self.stub.stash["created_account"] = {"id": 9, "body": {"address": "x@y"}}
        stash = copy.deepcopy(self.stub.stash)
        transaction = self.transaction(204, "/api/v1/admin/mailboxes/9")
        before = copy.deepcopy(transaction)
        self.hooks.a_mailbox_to_administer(transaction)
        self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])
        self.assertEqual(self.stub.stash, stash)

    def test_the_thread_and_import_operations_reset_in_their_first_hook(self):
        # It runs first, so the due date's and the snooze's thread hooks
        # (registered by entrypoint.py, after this file) and the import's own
        # hook see the clean start and the account it leaves in the path.
        for operation in ("listThreads", "setThreadDue", "snoozeThread", "importMessage"):
            names = [h for o, h in self.registered if o == operation]
            self.assertEqual(names[0], "on_the_admins_mailbox_after_a_reset", operation)

    def test_each_thread_and_import_exchange_resets_and_goes_to_the_admins_mailbox(self):
        self.clean_starts_on()
        self.hooks.mailbox_id = lambda origin, address: (
            4 if address == self.hooks.ADMIN else None
        )
        for path, status in (
            ("/api/v1/accounts/1/threads/%3Cdue%2F2026%40example.net%3E/due", 200),
            ("/api/v1/accounts/1/threads/%3Cthread%2F2026%40example.net%3E/snooze", 422),
            ("/api/v1/accounts/12/messages/import", 200),
            ("/api/v1/accounts/1/messages/import", 400),
        ):
            transaction = self.transaction(status, path)
            request = copy.deepcopy(transaction["request"])
            self.hooks.on_the_admins_mailbox_after_a_reset(transaction)
            self.assertEqual(
                transaction["fullPath"], re.sub(r"/accounts/\d+/", "/accounts/4/", path)
            )
            self.assertEqual(transaction["request"], request)
            self.assertLessEqual(set(self.stub.stash), set(self.hooks.SURVIVES_A_RESET))
        self.assertEqual(self.resets(), 4)
        # Only the list of threads awaiting a reply gets a message made for it.
        self.assertEqual(self.server.imports, [])

    def test_the_awaiting_list_gets_a_thread_of_its_own_after_the_reset(self):
        self.clean_starts_on()
        self.hooks.mailbox_id = lambda origin, address: (
            4 if address == self.hooks.ADMIN else None
        )
        transaction = self.transaction(200, "/api/v1/accounts/1/threads?state=awaiting&limit=1")
        self.hooks.on_the_admins_mailbox_after_a_reset(transaction)
        self.assertEqual(
            transaction["fullPath"], "/api/v1/accounts/4/threads?state=awaiting&limit=1"
        )
        self.assertEqual(self.resets(), 1)
        [(path, body)] = self.server.imports
        self.assertEqual(path, "/api/v1/accounts/4/messages/import")
        self.assertEqual(body["folder"], "Sent")
        raw = base64.b64decode(body["raw_message"]).decode()
        self.assertIn(f"From: {self.hooks.ADMIN}\r\n", raw)
        # The reset came first, and the message after it.
        reset = [c[1] for c in self.server.calls].index("/api/v1/admin/test/reset")
        made = [c[1] for c in self.server.calls].index(path)
        self.assertLess(reset, made)

    def test_thread_and_import_refusals_reset_and_keep_their_ids(self):
        self.clean_starts_on()
        self.hooks.mailbox_id = lambda origin, address: self.fail("looked up an account")
        for path, status in (
            ("/api/v1/accounts/0/threads?state=awaiting", 422),
            ("/api/v1/accounts/2147483647/messages/import", 404),
            ("/api/v1/accounts/not-an-integer/threads/x/snooze", 422),
            ("/api/v1/accounts/1/threads/missing/due", 404),
            ("/api/v1/accounts/1/threads?state=awaiting", 404),
        ):
            transaction = self.transaction(status, path)
            self.hooks.on_the_admins_mailbox_after_a_reset(transaction)
            self.assertEqual(transaction["fullPath"], path)
        self.assertEqual(self.resets(), 5)
        self.assertEqual(self.server.imports, [])

    def test_without_the_switch_a_thread_or_import_exchange_is_unchanged(self):
        stash = copy.deepcopy(self.stub.stash)
        transaction = self.transaction(200, "/api/v1/accounts/1/threads?state=awaiting")
        before = copy.deepcopy(transaction)
        self.hooks.on_the_admins_mailbox_after_a_reset(transaction)
        self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])
        self.assertEqual(self.stub.stash, stash)

    def test_the_policy_file_asks_for_a_domain_of_its_own_after_a_reset(self):
        self.clean_starts_on()
        self.the_run_created_its_domain()
        for status, active in ((404, False), (200, True)):
            transaction = self.transaction(status, "/.well-known/mta-sts.txt")
            self.hooks.ask_for_the_created_domains_policy(transaction)
            self.assertEqual(
                transaction["request"]["headers"]["Host"],
                "mta-sts." + self.hooks.OWN_DOMAIN,
            )
            self.assertEqual(self.server.domains[4], self.hooks.OWN_DOMAIN)
            self.assertIs(self.server.policies[4]["active"], active)
            self.assertNotIn("domain", self.stub.stash)
        self.assertEqual(self.resets(), 2)

    def test_without_the_switch_the_policy_file_asks_for_the_created_domain(self):
        self.the_run_created_its_domain()
        transaction = self.transaction(404, "/.well-known/mta-sts.txt")
        self.hooks.ask_for_the_created_domains_policy(transaction)
        self.assertEqual(
            transaction["request"]["headers"]["Host"], "mta-sts.created.test"
        )
        self.assertEqual(self.server.calls, [])

    def test_the_created_domain_is_kept_for_the_links(self):
        data = {"id": 3, "name": "created.test"}
        self.stub.get_json = lambda *_: {"data": data}
        self.hooks.hooks = self.stub
        self.hooks.keep_the_created_domain({})
        self.assertEqual(self.stub.stash, {
            "setup_admin_tokens": self.stub.stash["setup_admin_tokens"],
            "domain_id": 3,
            "domain": "created.test",
        })


if __name__ == "__main__":
    unittest.main()
