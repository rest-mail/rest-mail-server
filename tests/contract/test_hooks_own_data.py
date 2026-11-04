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
from urllib.parse import parse_qs

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "tests/contract"))
import own_data

POLICY = re.compile(r"^/api/v1/admin/domains/(\d+)/mta-sts$")
KEY = re.compile(r"^/api/v1/admin/dkim/(\d+)$")
IMPORT = re.compile(r"^/api/v1/accounts/(\d+)/messages/import$")
SETTING = re.compile(r"^/api/v1/accounts/(\d+)/(sieve|vacation)$")


# The account settings operations account_settings_of_its_own is registered for
# in hooks.py, and the two entrypoint.py registers it for after their own hook.
ACCOUNT_SETTINGS = (
    "getVacation",
    "setVacation",
    "disableVacation",
    "getSieveScript",
    "putSieveScript",
    "deleteSieveScript",
    "validateSieveScript",
    "getAccountSendLimits",
    "setAccountSendLimits",
    "getQuota",
)
QUIET_HOURS = ("getQuietHours", "setQuietHours")
CREDENTIALS = re.compile(r"^/api/v1/accounts/\d+/(credentials|api-keys|client-secrets)$")
FOLDERS = re.compile(r"^/api/v1/accounts/(\d+)/folders$")


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
            self.server.queue.clear()
            self.server.tls_reports.clear()
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
        if self.path == "/.well-known/smtp-tlsrpt":
            self.server.tls_reports.append(body)
            return self.answer(201, {"stored": True})
        if IMPORT.fullmatch(self.path):
            self.server.imports.append((self.path, body))
            return self.answer(200, {"id": len(self.server.imports), "imported": True})
        if self.path == "/api/v1/messages/send":
            self.server.serial += 1
            for recipient in body["to"]:
                self.server.queue[self.server.serial] = [recipient, "pending", self.headers["Authorization"]]
            return self.answer(201, {"id": self.server.serial})
        bounce = re.fullmatch(r"/api/v1/admin/queue/(\d+)/bounce", self.path)
        if bounce and int(bounce[1]) in self.server.queue:
            self.server.queue[int(bounce[1])][1] = "bounced"
            return self.answer(200, {"status": "bounced"})
        if CREDENTIALS.fullmatch(self.path) or self.path == "/api/v1/admin/bans":
            self.server.serial += 1
            self.server.made.append((self.path, body, self.headers["Authorization"]))
            return self.answer(201, {"id": self.server.serial})
        if FOLDERS.fullmatch(self.path):
            self.server.folders.append((self.path, body))
            return self.answer(201, {"name": body["name"]})
        if self.path != "/api/v1/admin/domains":
            return self.answer(404)
        if body["name"] in self.server.domains.values():
            return self.answer(409)
        self.server.serial += 1
        self.server.domains[self.server.serial] = body["name"]
        self.answer(201, {"id": self.server.serial, "name": body["name"]})

    def do_GET(self):
        self.server.calls.append(("GET", self.path, None))
        path, _, query = self.path.partition("?")
        if path != "/api/v1/admin/queue":
            return self.answer(404)
        wanted = parse_qs(query)["recipient"]
        items = [
            {"id": entry, "recipient": recipient, "status": status}
            for entry, (recipient, status, _) in self.server.queue.items()
            if [recipient] == wanted
        ]
        self.answer(200, {"items": items, "total": len(items)})

    def do_PUT(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.calls.append(("PUT", self.path, body))
        for pattern, store in ((POLICY, self.server.policies), (KEY, self.server.keys)):
            match = pattern.fullmatch(self.path)
            if match and int(match[1]) in self.server.domains:
                store[int(match[1])] = body
                return self.answer(200, body)
        match = SETTING.fullmatch(self.path)
        if match and int(match[1]) in self.server.accounts:
            self.server.settings[(int(match[1]), match[2])] = body
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
        self.server.settings = {}
        self.server.queue = {}
        self.server.made = []
        self.server.folders = []
        self.server.tls_reports = []
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
            **{operation: "account_settings_of_its_own" for operation in ACCOUNT_SETTINGS},
            "listQueue": "queued_and_held_mail_of_its_own",
            "getQueueStats": "queued_and_held_mail_of_its_own",
            "getQueueEntry": "queued_and_held_mail_of_its_own",
            "retryQueueEntry": "queued_and_held_mail_of_its_own",
            "bounceQueueEntry": "queued_and_held_mail_of_its_own",
            "deleteQueueEntry": "queued_and_held_mail_of_its_own",
            "bulkRetryQueue": "queued_and_held_mail_of_its_own",
            "bulkBounceQueue": "queued_and_held_mail_of_its_own",
            "bulkDeleteQueue": "queued_and_held_mail_of_its_own",
            "listHeldMessages": "queued_and_held_mail_of_its_own",
            "releaseHeldMessage": "queued_and_held_mail_of_its_own",
            "listScheduledMessages": "queued_and_held_mail_of_its_own",
            "cancelScheduledMessage": "queued_and_held_mail_of_its_own",
            "listCredentials": "a_credential_to_manage",
            "createAppPassword": "a_credential_to_manage",
            "revokeCredential": "a_credential_to_manage",
            "listAPIKeys": "a_credential_to_manage",
            "createAPIKey": "a_credential_to_manage",
            "updateAPIKey": "a_credential_to_manage",
            "revokeAPIKey": "a_credential_to_manage",
            "listAdminAPIKeys": "a_credential_to_manage",
            "listClientSecrets": "a_credential_to_manage",
            "createClientSecret": "a_credential_to_manage",
            "revokeClientSecret": "a_credential_to_manage",
            "listBans": "a_ban_to_manage",
            "createBan": "a_ban_to_manage",
            "deleteBan": "a_ban_to_manage",
            "unbanIP": "a_ban_to_manage",
            "updatePipeline": "update_a_pipeline_of_its_own",
            "listPipelines": "pipeline_reads_after_a_reset",
            "listPipelineLogs": "pipeline_reads_after_a_reset",
            "getPipelineAnalytics": "pipeline_reads_after_a_reset",
            "listRoles": "server_settings_after_a_reset",
            "listCapabilities": "server_settings_after_a_reset",
            "getServerLimits": "server_settings_after_a_reset",
            "updateServerLimits": "server_settings_after_a_reset",
            "getServerCapabilities": "server_settings_after_a_reset",
            "updateServerCapabilities": "server_settings_after_a_reset",
            "listDmarcReports": "report_and_audit_reads_after_a_reset",
            "listTlsReports": "report_and_audit_reads_after_a_reset",
            "receiveTlsReport": "report_and_audit_reads_after_a_reset",
            "listAuditEntries": "report_and_audit_reads_after_a_reset",
            "verifyAuditLog": "report_and_audit_reads_after_a_reset",
            "getDeliveryLogs": "report_and_audit_reads_after_a_reset",
            "getDashboardStats": "report_and_audit_reads_after_a_reset",
            "listFolders": "a_mailbox_read_after_a_reset",
            "listMessages": "a_mailbox_read_after_a_reset",
            "searchMessages": "a_mailbox_read_after_a_reset",
            "getAttachment": "on_an_attachment_of_its_own",
            "checkMailboxAddress": "delivery_after_a_reset",
            "deliverMessage": "delivery_after_a_reset",
            "restmailDiscover": "delivery_after_a_reset",
            "restmailCapabilities": "delivery_after_a_reset",
            "restmailCheckMailbox": "delivery_after_a_reset",
            "restmailDeliver": "delivery_after_a_reset",
            "testFilter": "filter_requests_after_a_reset",
            "listCustomFilters": "filter_requests_after_a_reset",
            "validateCustomFilter": "filter_requests_after_a_reset",
            "getHealth": "public_reads_after_a_reset",
            "describeServer": "public_reads_after_a_reset",
            "apiRoot": "public_reads_after_a_reset",
            "swaggerUI": "public_reads_after_a_reset",
            "openapiSpec": "public_reads_after_a_reset",
            "openapiDocument": "public_reads_after_a_reset",
            "prometheusMetrics": "public_reads_after_a_reset",
            "mozillaAutoconfig": "public_reads_after_a_reset",
            "mozillaAutoconfigWellKnown": "public_reads_after_a_reset",
            "microsoftAutodiscover": "public_reads_after_a_reset",
            "listDomains": "domain_administration_after_a_reset",
            "listAliases": "domain_administration_after_a_reset",
            "listDKIMKeys": "domain_administration_after_a_reset",
            "listCertificates": "domain_administration_after_a_reset",
            "createCertificate": "domain_administration_after_a_reset",
            "getCertificate": "domain_administration_after_a_reset",
            "deleteCertificate": "domain_administration_after_a_reset",
            "linkAccount": "account_links_and_the_token_exchange_after_a_reset",
            "unlinkAccount": "account_links_and_the_token_exchange_after_a_reset",
            "issueClientCredentialsToken": "account_links_and_the_token_exchange_after_a_reset",
            "login": "sign_in_and_recovery_after_a_reset",
            "logout": "sign_in_and_recovery_after_a_reset",
            "refreshToken": "sign_in_and_recovery_after_a_reset",
            "protocolSignIn": "sign_in_and_recovery_after_a_reset",
            "resetAdmin": "sign_in_and_recovery_after_a_reset",
            "recoverAccount": "sign_in_and_recovery_after_a_reset",
            "ownerChallenge": "sign_in_and_recovery_after_a_reset",
            "ownerToken": "sign_in_and_recovery_after_a_reset",
            "ownerStatus": "sign_in_and_recovery_after_a_reset",
            "getTwoFactorStatus": "sign_in_and_recovery_after_a_reset",
            "enrollTwoFactor": "sign_in_and_recovery_after_a_reset",
            "confirmTwoFactor": "sign_in_and_recovery_after_a_reset",
            "disableTwoFactor": "sign_in_and_recovery_after_a_reset",
            "changeOwnPassword": "sign_in_and_recovery_after_a_reset",
            "requestRecoveryEmail": "sign_in_and_recovery_after_a_reset",
            "redeemRecoveryEmail": "sign_in_and_recovery_after_a_reset",
            "getRecoveryEmail": "sign_in_and_recovery_after_a_reset",
            "registerRecoveryEmail": "sign_in_and_recovery_after_a_reset",
            "recoveryEmailLink": "sign_in_and_recovery_after_a_reset",
            "verifyRecoveryEmail": "sign_in_and_recovery_after_a_reset",
            "getAccount": "the_last_free_operations_after_a_reset",
            "listCalendarEvents": "the_last_free_operations_after_a_reset",
            "respondToCalendar": "the_last_free_operations_after_a_reset",
            "accountEvents": "the_last_free_operations_after_a_reset",
            "probeServices": "the_last_free_operations_after_a_reset",
            "verifyDelivery": "the_last_free_operations_after_a_reset",
            "clearSuppression": "clear_a_suppression_made_after_the_reset",
            "getDmarcReport": "read_a_dmarc_report_delivered_after_the_reset",
            "listDmarcReports": "list_a_dmarc_report_delivered_after_the_reset",
            "resetTestData": "the_test_data_operations_after_a_reset",
            "seedTestData": "the_test_data_operations_after_a_reset",
            "snapshotTestData": "the_test_data_operations_after_a_reset",
            "restoreTestSnapshot": "the_test_data_operations_after_a_reset",
        }
        # entrypoint.py registers setDomainRelay's and the quiet hours' (below).
        self.assertEqual(
            set(hooks) | {"setDomainRelay"} | set(QUIET_HOURS), set(own_data.MOVED)
        )
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

    SERVER_SETTINGS = (
        ("listRoles", "/api/v1/admin/roles"),
        ("listCapabilities", "/api/v1/admin/capabilities"),
        ("getServerLimits", "/api/v1/admin/server-limits"),
        ("updateServerLimits", "/api/v1/admin/server-limits"),
        ("getServerCapabilities", "/api/v1/admin/server-capabilities"),
        ("updateServerCapabilities", "/api/v1/admin/server-capabilities"),
    )

    def test_the_server_settings_reset_in_their_first_hook(self):
        # It runs before the two-factor administrator's hook, which only
        # changes who sends the request.
        for operation, _ in self.SERVER_SETTINGS:
            names = [h for o, h in self.registered if o == operation]
            self.assertEqual(names[0], "server_settings_after_a_reset", operation)

    def test_each_server_setting_exchange_only_resets(self):
        self.clean_starts_on()
        for status in (200, 401, 403, 422):
            for operation, path in self.SERVER_SETTINGS:
                self.stub.stash["left_behind"] = operation
                exchange = self.transaction(status, path)
                before = copy.deepcopy(exchange)
                calls = len(self.server.calls)
                self.hooks.server_settings_after_a_reset(exchange)
                # The request is sent as it was, after a reset and seed and
                # nothing made, and nothing from earlier is kept.
                self.assertEqual(exchange, before, operation)
                self.assertEqual(
                    [call[1] for call in self.server.calls[calls:]],
                    ["/api/v1/admin/test/reset", "/api/v1/admin/test/seed"],
                    operation,
                )
                self.assertNotIn("left_behind", self.stub.stash)
                self.assertLessEqual(
                    set(self.stub.stash), set(self.hooks.SURVIVES_A_RESET)
                )

    def test_the_server_settings_without_a_reset_change_nothing(self):
        for operation, path in self.SERVER_SETTINGS:
            exchange = self.transaction(200, path)
            before = copy.deepcopy(exchange)
            self.hooks.server_settings_after_a_reset(exchange)
            self.assertEqual(exchange, before, operation)
        self.assertEqual(self.server.calls, [])

    def test_the_report_and_audit_reads_reset_in_their_first_hook(self):
        # Ahead of the two-factor administrator's, the TLS report pacing and
        # the hosting of the reported domains, so they see the clean start.
        for operation in (
            "listDmarcReports",
            "listTlsReports",
            "receiveTlsReport",
            "listAuditEntries",
            "verifyAuditLog",
            "getDeliveryLogs",
            "getDashboardStats",
        ):
            names = [h for o, h in self.registered if o == operation]
            self.assertEqual(names[0], "report_and_audit_reads_after_a_reset", operation)

    def test_a_tls_report_listing_gets_one_received_after_its_reset(self):
        self.clean_starts_on()
        self.stub.stash["tls_reports_seen"] = 1
        self.server.tls_reports.append({"report-id": "earlier"})
        transaction = self.transaction(200, "/api/v1/admin/tls-reports?limit=5")
        before = copy.deepcopy(transaction)
        self.hooks.report_and_audit_reads_after_a_reset(transaction)
        self.assertEqual(transaction, before)
        self.assertEqual(
            [call[1] for call in self.server.calls],
            [
                "/api/v1/admin/test/reset",
                "/api/v1/admin/test/seed",
                "/.well-known/smtp-tlsrpt",
            ],
        )
        self.assertEqual(self.server.tls_reports, [self.hooks.OWN_TLS_REPORT])
        self.assertEqual(
            self.server.tls_reports[0]["policies"][0]["policy"]["policy-domain"],
            "restmail.test",
        )
        self.assertNotIn("tls_reports_seen", self.stub.stash)

    def test_the_other_report_and_audit_exchanges_only_reset(self):
        self.clean_starts_on()
        cases = (
            (200, "/api/v1/admin/dmarc-reports"),
            (422, "/api/v1/admin/dmarc-reports?limit=0"),
            (422, "/api/v1/admin/tls-reports?limit=0"),
            (201, "/.well-known/smtp-tlsrpt"),
            (422, "/.well-known/smtp-tlsrpt"),
            (200, "/api/v1/admin/audit"),
            (403, "/api/v1/admin/audit/verify"),
            (200, "/api/v1/admin/logs/delivery"),
            (200, "/api/v1/admin/stats"),
        )
        for status, path in cases:
            transaction = self.transaction(status, path)
            before = copy.deepcopy(transaction)
            self.hooks.report_and_audit_reads_after_a_reset(transaction)
            self.assertEqual(transaction, before, path)
        self.assertEqual(self.resets(), len(cases))
        self.assertEqual(
            {call[1] for call in self.server.calls},
            {"/api/v1/admin/test/reset", "/api/v1/admin/test/seed"},
        )
        self.assertEqual(self.server.tls_reports, [])

    def test_the_report_and_audit_reads_change_nothing_without_the_switch(self):
        stash = copy.deepcopy(self.stub.stash)
        for status, path in (
            (200, "/api/v1/admin/tls-reports"),
            (201, "/.well-known/smtp-tlsrpt"),
            (200, "/api/v1/admin/audit"),
        ):
            transaction = self.transaction(status, path)
            before = copy.deepcopy(transaction)
            self.hooks.report_and_audit_reads_after_a_reset(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])
        self.assertEqual(self.stub.stash, stash)

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

    def setting_transaction(self, method, status, path):
        transaction = self.transaction(status, path)
        transaction["request"]["method"] = method
        return transaction

    def test_the_account_settings_reset_in_their_first_hook(self):
        # It runs first, so the sieve install's hook sees the clean start and
        # the path it leaves.
        for operation in ACCOUNT_SETTINGS:
            names = [h for o, h in self.registered if o == operation]
            self.assertEqual(names[0], "account_settings_of_its_own", operation)
        # The quiet hours' own hook finds a live account for the examples' 1
        # and fixes their time zone, so this one comes after it.
        entrypoint = (ROOT / "tests/contract/entrypoint.py").read_text()
        registration = 'vertrag_hooks.before(operation)(shared["account_settings_of_its_own"])'
        self.assertLess(
            entrypoint.index('quiet["register_quiet_hours_hooks"]'),
            entrypoint.index(registration),
        )
        for operation in QUIET_HOURS:
            self.assertIn(f'"{operation}"', entrypoint)
            self.assertNotIn(operation, {o for o, _ in self.registered})

    def test_each_account_setting_resets_and_gets_an_account_of_its_own(self):
        self.clean_starts_on()
        self.two_factor_session()
        cases = (
            # method, status, path, the setting made on the account
            ("GET", 200, "/api/v1/accounts/1/sieve", "sieve"),
            ("DELETE", 200, "/api/v1/accounts/1/sieve", "sieve"),
            ("DELETE", 204, "/api/v1/accounts/1/vacation", "vacation"),
            ("PUT", 201, "/api/v1/accounts/1/sieve", None),
            ("PUT", 422, "/api/v1/accounts/1/sieve", None),
            ("POST", 200, "/api/v1/accounts/1/sieve/validate", None),
            ("GET", 200, "/api/v1/accounts/1/vacation", None),
            ("PUT", 422, "/api/v1/accounts/1/vacation", None),
            ("GET", 200, "/api/v1/accounts/1/quiet-hours", None),
            ("PUT", 200, "/api/v1/accounts/7/quiet-hours", None),
            ("GET", 200, "/api/v1/accounts/1/send-limits?x=1", None),
            ("PUT", 422, "/api/v1/accounts/1/send-limits", None),
            ("GET", 200, "/api/v1/accounts/1/quota", None),
        )
        for method, status, path, made in cases:
            self.server.settings.clear()
            transaction = self.setting_transaction(method, status, path)
            request = copy.deepcopy(transaction["request"])
            self.hooks.account_settings_of_its_own(transaction)
            account = max(self.server.accounts)
            self.assertEqual(
                transaction["fullPath"],
                re.sub(r"/accounts/\d+", f"/accounts/{account:d}", path),
            )
            address, session = self.server.accounts[account]
            self.assertRegex(address, r"^contract-own-settings-\d+@restmail\.test$")
            # Making an account takes the two-factor session; the request's
            # own credential and body stay as they are.
            self.assertEqual(session, "Bearer two-factor-session")
            self.assertEqual(transaction["request"], request)
            expected = {(account, made)} if made else set()
            self.assertEqual(set(self.server.settings), expected, (method, status, path))
            if made == "sieve":
                self.assertEqual(
                    self.server.settings[(account, "sieve")], self.hooks.A_SIEVE_SCRIPT
                )
            if made == "vacation":
                self.assertIs(self.server.settings[(account, "vacation")]["enabled"], True)
            self.assertLessEqual(set(self.stub.stash), set(self.hooks.SURVIVES_A_RESET))
        self.assertEqual(self.resets(), len(cases))

    def test_account_setting_refusals_reset_and_keep_their_ids(self):
        self.clean_starts_on()
        for method, status, path in (
            ("GET", 422, "/api/v1/accounts/0/send-limits"),
            ("GET", 404, "/api/v1/accounts/2147483647/vacation"),
            ("DELETE", 404, "/api/v1/accounts/2147483647/sieve"),
            ("PUT", 404, "/api/v1/accounts/2147483647/quiet-hours"),
        ):
            transaction = self.setting_transaction(method, status, path)
            self.hooks.account_settings_of_its_own(transaction)
            self.assertEqual(transaction["fullPath"], path)
        self.assertEqual(self.server.accounts, {})
        self.assertEqual(self.resets(), 4)

    def test_without_the_switch_account_settings_change_nothing(self):
        stash = copy.deepcopy(self.stub.stash)
        transaction = self.setting_transaction("GET", 200, "/api/v1/accounts/1/sieve")
        before = copy.deepcopy(transaction)
        self.hooks.account_settings_of_its_own(transaction)
        self.assertEqual(transaction, before)
        self.assertEqual(self.stub.stash, stash)
        self.assertEqual(self.server.calls, [])

    QUEUE_OPERATIONS = (
        "listQueue",
        "getQueueStats",
        "getQueueEntry",
        "retryQueueEntry",
        "bounceQueueEntry",
        "deleteQueueEntry",
        "bulkRetryQueue",
        "bulkBounceQueue",
        "bulkDeleteQueue",
        "listHeldMessages",
        "releaseHeldMessage",
        "listScheduledMessages",
        "cancelScheduledMessage",
    )

    def test_the_queue_operations_reset_in_their_first_hook(self):
        # It runs first, so the fixture queue hooks and the scheduled
        # message's see the clean start and the path it leaves.
        for operation in self.QUEUE_OPERATIONS:
            names = [h for o, h in self.registered if o == operation]
            self.assertEqual(names[0], "queued_and_held_mail_of_its_own", operation)

    def test_each_queue_entry_success_gets_an_entry_of_its_own(self):
        self.clean_starts_on()
        for path, status, state in (
            ("/api/v1/admin/queue/1", 200, "pending"),
            ("/api/v1/admin/queue/1", 204, "pending"),
            ("/api/v1/admin/queue/7/retry", 200, "pending"),
            ("/api/v1/admin/queue/1/bounce?x=1", 200, "pending"),
            # retry's already_bounced: refused for that, not for a gone entry.
            ("/api/v1/admin/queue/5/retry", 400, "bounced"),
        ):
            transaction = self.transaction(status, path)
            request = copy.deepcopy(transaction["request"])
            self.hooks.queued_and_held_mail_of_its_own(transaction)
            entry = max(self.server.queue)
            recipient, made, session = self.server.queue[entry]
            self.assertEqual(
                transaction["fullPath"],
                re.sub(r"/queue/\d+", f"/queue/{entry:d}", path),
            )
            self.assertRegex(recipient, r"^contract-own-queue-\d+@queue\.invalid$")
            self.assertEqual(made, state)
            # The fixture admin's setup session sends it; the request's own
            # credential and body stay as they are.
            self.assertEqual(session, "Bearer setup-session")
            self.assertEqual(transaction["request"], request)
            self.assertLessEqual(set(self.stub.stash), set(self.hooks.SURVIVES_A_RESET))
        self.assertEqual(self.resets(), 5)

    def test_the_queue_list_shows_an_entry_of_its_own(self):
        self.clean_starts_on()
        transaction = self.transaction(200, "/api/v1/admin/queue?limit=20&offset=0")
        self.hooks.queued_and_held_mail_of_its_own(transaction)
        (recipient, _, _), = self.server.queue.values()
        self.assertEqual(
            transaction["fullPath"],
            "/api/v1/admin/queue?recipient=" + recipient.replace("@", "%40"),
        )

    def test_a_bulk_empty_filter_gets_an_entry_and_the_documented_filter_none(self):
        self.clean_starts_on()
        for body, made in (
            ({"filter": {}}, 1),
            ({"filter": {"domain": "example.test", "status": "deferred"}}, 0),
        ):
            self.server.queue.clear()
            transaction = self.transaction(200, "/api/v1/admin/queue/bulk-retry")
            transaction["request"]["body"] = json.dumps(body)
            self.hooks.queued_and_held_mail_of_its_own(transaction)
            self.assertEqual(len(self.server.queue), made)
            self.assertEqual(transaction["fullPath"], "/api/v1/admin/queue/bulk-retry")

    def test_the_held_list_names_an_account_of_its_own(self):
        self.clean_starts_on()
        self.two_factor_session()
        transaction = self.transaction(200, "/api/v1/accounts/1/held-messages")
        self.hooks.queued_and_held_mail_of_its_own(transaction)
        account = max(self.server.accounts)
        self.assertEqual(transaction["fullPath"], f"/api/v1/accounts/{account:d}/held-messages")
        self.assertRegex(self.server.accounts[account][0], r"^contract-own-held-\d+@restmail\.test$")

    def test_queue_refusals_and_the_rest_reset_and_keep_their_paths(self):
        self.clean_starts_on()
        for path, status in (
            ("/api/v1/admin/queue/0", 422),
            ("/api/v1/admin/queue/2147483647", 404),
            ("/api/v1/admin/queue/2147483647/retry", 200),
            ("/api/v1/admin/queue/not-an-integer/bounce", 200),
            ("/api/v1/admin/queue/1", 401),
            ("/api/v1/admin/queue?status=pending&limit=x", 422),
            ("/api/v1/admin/queue/stats", 200),
            ("/api/v1/accounts/2147483647/held-messages", 404),
            ("/api/v1/accounts/0/held-messages", 422),
            ("/api/v1/messages/scheduled/1/release", 403),
            ("/api/v1/messages/scheduled", 200),
            ("/api/v1/messages/scheduled/52", 204),
        ):
            transaction = self.transaction(status, path)
            self.hooks.queued_and_held_mail_of_its_own(transaction)
            self.assertEqual(transaction["fullPath"], path)
        self.assertEqual(self.resets(), 12)
        self.assertEqual(self.server.queue, {})
        self.assertEqual(self.server.accounts, {})

    def test_without_the_switch_a_queue_exchange_is_unchanged(self):
        for path in ("/api/v1/admin/queue/1", "/api/v1/accounts/1/held-messages"):
            transaction = self.transaction(200, path)
            before = copy.deepcopy(transaction)
            self.hooks.queued_and_held_mail_of_its_own(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

    def test_after_a_reset_the_fixture_queue_hooks_stand_aside(self):
        # The fixture entries they look for are gone after a reset.
        self.clean_starts_on()
        for hook, path in (
            ("list_the_sent_messages_queue_entry", "/api/v1/admin/queue?status=pending&limit=20"),
            ("retry_a_held_entry_after_bulk_examples", "/api/v1/admin/queue/9/retry"),
            ("bounce_the_held_bounce_entry", "/api/v1/admin/queue/9/bounce"),
        ):
            transaction = self.transaction(200, path)
            getattr(self.hooks, hook)(transaction)
            self.assertEqual(transaction["fullPath"], path)
        self.assertEqual(self.server.calls, [])

    def test_the_credential_and_ban_operations_reset_in_their_first_hook(self):
        # It runs first, so the two-factor swap, the edit's own key and the
        # create's state see the clean start and the path it leaves.
        start = own_data.MOVED.index("listCredentials")
        for operation in own_data.MOVED[start : own_data.MOVED.index("unbanIP") + 1]:
            names = [h for o, h in self.registered if o == operation]
            self.assertIn(names[0], ("a_credential_to_manage", "a_ban_to_manage"), operation)

    def test_each_credential_exchange_gets_an_account_and_a_revoke_its_credential(self):
        self.clean_starts_on()
        self.two_factor_session()
        self.hooks.mailbox_address = lambda origin, account: f"account-{account}@restmail.test"
        for method, path, status in (
            ("GET", "/api/v1/accounts/1/credentials", 200),
            ("POST", "/api/v1/accounts/1/api-keys", 422),
            ("PATCH", "/api/v1/accounts/1/api-keys/2", 200),
            ("DELETE", "/api/v1/accounts/1/credentials/1", 204),
            ("DELETE", "/api/v1/accounts/9/credentials/7", 403),
            ("DELETE", "/api/v1/accounts/1/api-keys/1?x=1", 204),
            ("DELETE", "/api/v1/accounts/1/client-secrets/5", 204),
        ):
            transaction = self.transaction(status, path)
            transaction["request"]["method"] = method
            request = copy.deepcopy(transaction["request"])
            made = len(self.server.made)
            with patch.object(self.hooks.time, "sleep"):
                self.hooks.a_credential_to_manage(transaction)
            account = max(self.server.accounts)
            address, session = self.server.accounts[account]
            self.assertRegex(address, r"^contract-own-credential-\d+@restmail\.test$")
            self.assertEqual(session, "Bearer two-factor-session")
            want = re.sub(r"/accounts/\d+", f"/accounts/{account:d}", path)
            if method != "DELETE":
                # Nothing else is made: the edit makes its own key.
                self.assertEqual(len(self.server.made), made)
            else:
                collection, body, session = self.server.made[-1]
                self.assertEqual(collection, re.sub(r"/\d+(\?.*)?$", "", want))
                want = re.sub(r"/\d+(\?|$)", f"/{self.server.serial:d}\\1", want)
                # Made with the two-factor session and its password.
                self.assertEqual(session, "Bearer two-factor-session")
                self.assertEqual(body["password"], self.hooks.mfa_admin.PASSWORD)
                if "/credentials/" in path:
                    self.assertNotIn("permissions", body)
                else:
                    self.assertEqual(
                        body["permissions"],
                        [{"capability": "mail:read", "scope": f"mailbox:account-{account:d}@restmail.test"}],
                    )
            self.assertEqual(transaction["fullPath"], want)
            # The request's own credential and body stay as they are.
            self.assertEqual(transaction["request"], request)
            self.assertLessEqual(set(self.stub.stash), set(self.hooks.SURVIVES_A_RESET))
        self.assertEqual(self.resets(), 7)

    def test_credential_refusals_and_the_admin_list_reset_and_keep_their_paths(self):
        self.clean_starts_on()
        for method, path in (
            ("GET", "/api/v1/accounts/0/credentials"),
            ("POST", "/api/v1/accounts/2147483647/api-keys"),
            ("DELETE", "/api/v1/accounts/0/client-secrets/1"),
            ("GET", "/api/v1/accounts/not-an-integer/credentials"),
            ("GET", "/api/v1/admin/api-keys?limit=20"),
        ):
            transaction = self.transaction(204, path)
            transaction["request"]["method"] = method
            before = copy.deepcopy(transaction)
            self.hooks.a_credential_to_manage(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.server.made, [])
        self.assertEqual(self.server.accounts, {})
        self.assertEqual(self.resets(), 5)

    def test_the_documented_ban_delete_gets_a_ban_of_its_own(self):
        self.clean_starts_on()
        transaction = self.transaction(204, "/api/v1/admin/bans/1")
        self.hooks.a_ban_to_manage(transaction)
        made, body, _ = self.server.made[-1]
        self.assertEqual(made, "/api/v1/admin/bans")
        self.assertEqual(body["ip"], "192.0.2.77")
        self.assertEqual(transaction["fullPath"], f"/api/v1/admin/bans/{self.server.serial:d}")
        for status, path in (
            (404, "/api/v1/admin/bans/2147483647"),
            (204, "/api/v1/admin/bans/ip/192.0.2.10"),
            (201, "/api/v1/admin/bans"),
            (200, "/api/v1/admin/bans"),
        ):
            transaction = self.transaction(status, path)
            before = copy.deepcopy(transaction)
            self.hooks.a_ban_to_manage(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(len(self.server.made), 1)
        self.assertEqual(self.resets(), 5)

    def test_without_the_switch_credential_and_ban_exchanges_are_unchanged(self):
        for hook, path in (
            (self.hooks.a_credential_to_manage, "/api/v1/accounts/1/credentials/1"),
            (self.hooks.a_ban_to_manage, "/api/v1/admin/bans/1"),
        ):
            transaction = self.transaction(204, path)
            transaction["request"]["method"] = "DELETE"
            before = copy.deepcopy(transaction)
            hook(transaction)
            self.assertEqual(transaction, before)
        self.assertEqual(self.server.calls, [])

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


class ListingsHoldWhatWasMade(OwnDataHooks):
    """A listing's 200 after a reset must list what its before hook made (#1785)."""

    LISTED_TLS = {"id": 7, "reporting_org": "Contract Reporter", "policy_domain": "restmail.test"}

    def answered(self, transaction, rows, status=200):
        transaction["real"] = {
            "statusCode": str(status),
            "body": json.dumps({"data": rows, "pagination": {"total": len(rows)}}),
        }
        return transaction

    def noted(self, transaction, what, match):
        self.stub.stash["listing_made"] = {
            "name": transaction["name"], "what": what, "match": match,
        }

    def tls_listing(self, rows):
        transaction = self.transaction(200, "/api/v1/admin/tls-reports?limit=20")
        self.noted(transaction, "TLS report", {
            "reporting_org": "Contract Reporter", "policy_domain": "restmail.test",
        })
        return self.answered(transaction, rows)

    def test_the_listings_are_checked_after_they_answer(self):
        source = (ROOT / "tests/contract/hooks.py").read_text()
        self.assertIn(
            '@hooks.after("listTlsReports")\n@hooks.after("listPipelines")\n'
            '@hooks.after("listThreads")\n@hooks.after("listFolders")\n'
            '@hooks.after("listMessages")\n@hooks.after("searchMessages")\n'
            'def the_listing_holds_what_was_made(',
            source,
        )

    def test_a_tls_report_listing_notes_the_report_it_received(self):
        self.clean_starts_on()
        transaction = self.transaction(200, "/api/v1/admin/tls-reports?limit=20")
        self.hooks.report_and_audit_reads_after_a_reset(transaction)
        self.assertEqual(self.stub.stash["listing_made"], {
            "name": transaction["name"],
            "what": "TLS report",
            "match": {"reporting_org": "Contract Reporter", "policy_domain": "restmail.test"},
        })

    def test_a_pipeline_listing_notes_the_pipeline_it_made(self):
        self.clean_starts_on()
        self.hooks.a_domain_of_its_own = lambda transaction: 31
        self.hooks.a_pipeline_of_its_own = lambda transaction, domain_id: 32
        transaction = self.transaction(200, "/api/v1/admin/pipelines?limit=20")
        self.hooks.pipeline_reads_after_a_reset(transaction)
        self.assertEqual(self.stub.stash["listing_made"]["match"], {"id": 32})
        self.assertEqual(self.stub.stash["listing_made"]["what"], "pipeline")

    def test_the_awaiting_list_notes_the_subject_of_its_thread(self):
        self.clean_starts_on()
        self.hooks.mailbox_id = lambda origin, address: 4
        transaction = self.transaction(200, "/api/v1/accounts/1/threads?state=awaiting&limit=1")
        self.hooks.on_the_admins_mailbox_after_a_reset(transaction)
        [(_, body)] = self.server.imports
        raw = base64.b64decode(body["raw_message"]).decode()
        subject = self.stub.stash["listing_made"]["match"]["last_message.subject"]
        self.assertTrue(subject.startswith("Contract awaiting "))
        self.assertIn(f"\r\nSubject: {subject}\r\n", raw)

    def test_the_folder_list_notes_the_folder_it_made(self):
        self.clean_starts_on()
        self.hooks.mailbox_id = lambda origin, address: 4
        transaction = self.transaction(200, "/api/v1/accounts/1/folders", name="/f > F")
        self.hooks.a_mailbox_read_after_a_reset(transaction)
        [(path, body)] = self.server.folders
        self.assertEqual(path, "/api/v1/accounts/4/folders")
        self.assertEqual(self.stub.stash["listing_made"]["match"], {"name": body["name"]})
        self.assertEqual(self.stub.stash["listing_made"]["what"], "folder")
        self.hooks.the_listing_holds_what_was_made(
            self.answered(transaction, [{"name": "INBOX"}, {"name": body["name"]}])
        )
        self.assertNotIn("fail", transaction)

    def test_a_folder_page_notes_the_message_it_imported_there(self):
        self.clean_starts_on()
        self.hooks.mailbox_id = lambda origin, address: 4
        transaction = self.transaction(200, "/api/v1/accounts/1/folders/INBOX/messages", name="/m > M")
        self.hooks.a_mailbox_read_after_a_reset(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/accounts/4/folders/INBOX/messages")
        [(path, body)] = self.server.imports
        self.assertEqual(path, "/api/v1/accounts/4/messages/import")
        self.assertEqual(body["folder"], "INBOX")
        match = self.stub.stash["listing_made"]["match"]
        self.assertEqual(match["folder"], "INBOX")
        self.assertTrue(match["subject"].startswith("Contract listed "))
        self.assertIn(f"\r\nSubject: {match['subject']}\r\n", base64.b64decode(body["raw_message"]).decode())
        self.hooks.the_listing_holds_what_was_made(self.answered(transaction, []))
        self.assertIn("does not hold the message made for it", transaction["fail"])

    def test_a_search_notes_a_message_its_query_finds(self):
        self.clean_starts_on()
        self.hooks.mailbox_id = lambda origin, address: 4
        transaction = self.transaction(200, "/api/v1/accounts/1/search?q=Subject", name="/s > S")
        self.hooks.a_mailbox_read_after_a_reset(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/accounts/4/search?q=Subject")
        [(_, body)] = self.server.imports
        subject = self.stub.stash["listing_made"]["match"]["subject"]
        self.assertTrue(subject.startswith("Subject "))
        self.assertIn(f"\r\nSubject: {subject}\r\n", base64.b64decode(body["raw_message"]).decode())

    def test_mailbox_read_refusals_reset_and_keep_their_ids(self):
        self.clean_starts_on()
        self.hooks.mailbox_id = lambda origin, address: self.fail("looked up an account")
        for status, path in (
            (422, "/api/v1/accounts/0/folders"),
            (422, "/api/v1/accounts/0/folders/INBOX/messages"),
            (422, "/api/v1/accounts/0/search"),
            (404, "/api/v1/accounts/2147483647/search?q=Subject"),
        ):
            with self.subTest(path=path):
                transaction = self.transaction(status, path)
                before = self.resets()
                self.hooks.a_mailbox_read_after_a_reset(transaction)
                self.assertEqual(self.resets(), before + 1)
                self.assertEqual(transaction["fullPath"], path)
                self.assertNotIn("listing_made", self.stub.stash)
        self.assertEqual(self.server.imports, [])
        self.assertEqual(self.server.folders, [])

    def test_without_the_switch_a_mailbox_read_is_unchanged(self):
        self.hooks.mailbox_id = lambda origin, address: self.fail("looked up an account")
        transaction = self.transaction(200, "/api/v1/accounts/1/folders/INBOX/messages")
        self.hooks.a_mailbox_read_after_a_reset(transaction)
        self.assertEqual(transaction["fullPath"], "/api/v1/accounts/1/folders/INBOX/messages")
        self.assertEqual(self.server.calls, [])
        self.assertNotIn("listing_made", self.stub.stash)

    def test_the_page_after_a_cursor_is_noted_as_holding_none_of_it(self):
        self.clean_starts_on()
        self.hooks.mailbox_id = lambda origin, address: 4
        transaction = self.transaction(
            200, "/api/v1/accounts/1/threads?state=awaiting&limit=1&cursor=eyJpZCI6NzJ9"
        )
        self.hooks.on_the_admins_mailbox_after_a_reset(transaction)
        self.assertIsNone(self.stub.stash["listing_made"]["match"])
        self.hooks.the_listing_holds_what_was_made(self.answered(transaction, []))
        self.assertNotIn("fail", transaction)

    def test_a_listing_that_holds_what_was_made_passes(self):
        self.clean_starts_on()
        transaction = self.tls_listing([
            {"id": 3, "reporting_org": "Other", "policy_domain": "restmail.test"},
            self.LISTED_TLS,
        ])
        self.hooks.the_listing_holds_what_was_made(transaction)
        self.assertNotIn("fail", transaction)
        self.assertNotIn("listing_made", self.stub.stash)

    def test_a_nested_field_is_matched_in_its_object(self):
        self.clean_starts_on()
        transaction = self.transaction(200, "/api/v1/accounts/4/threads?state=awaiting")
        self.noted(transaction, "thread", {"last_message.subject": "Contract awaiting 1"})
        self.answered(transaction, [{"thread_id": "t", "last_message": {"subject": "Contract awaiting 1"}}])
        self.hooks.the_listing_holds_what_was_made(transaction)
        self.assertNotIn("fail", transaction)
        self.noted(transaction, "thread", {"last_message.subject": "Contract awaiting 1"})
        self.answered(transaction, [{"thread_id": "t", "last_message": {"subject": "Contract awaiting 2"}}])
        self.hooks.the_listing_holds_what_was_made(transaction)
        self.assertIn("does not hold the thread made for it", transaction["fail"])

    def test_an_empty_listing_fails(self):
        self.clean_starts_on()
        transaction = self.tls_listing([])
        self.hooks.the_listing_holds_what_was_made(transaction)
        self.assertEqual(
            transaction["fail"],
            "the listing does not hold the TLS report made for it (reporting_org "
            "'Contract Reporter', policy_domain 'restmail.test'): it answered 0 other rows",
        )

    def test_a_listing_of_something_else_fails(self):
        self.clean_starts_on()
        for row in (
            {"id": 3, "reporting_org": "Other", "policy_domain": "restmail.test"},
            {"id": 4, "reporting_org": "Contract Reporter", "policy_domain": "other.test"},
        ):
            with self.subTest(row=row):
                transaction = self.tls_listing([row])
                self.hooks.the_listing_holds_what_was_made(transaction)
                self.assertIn("it answered 1 other row", transaction["fail"])
        transaction = self.transaction(200, "/api/v1/admin/pipelines?limit=20")
        self.noted(transaction, "pipeline", {"id": 32})
        self.hooks.the_listing_holds_what_was_made(self.answered(transaction, [{"id": 31}, {"id": 33}]))
        self.assertIn("pipeline made for it (id 32)", transaction["fail"])

    def test_a_listing_with_nothing_made_for_it_fails(self):
        self.clean_starts_on()
        transaction = self.answered(
            self.transaction(200, "/api/v1/admin/tls-reports"), [self.LISTED_TLS]
        )
        self.hooks.the_listing_holds_what_was_made(transaction)
        self.assertIn("nothing made for it", transaction["fail"])

    def test_another_exchanges_note_is_dropped_and_not_used(self):
        self.clean_starts_on()
        self.tls_listing([])
        transaction = self.answered(
            self.transaction(200, "/api/v1/admin/tls-reports", name="/y > Y"), [self.LISTED_TLS]
        )
        self.hooks.the_listing_holds_what_was_made(transaction)
        self.assertIn("nothing made for it", transaction["fail"])
        self.assertNotIn("listing_made", self.stub.stash)

    def test_refusals_and_failed_statuses_are_left_to_their_own_checks(self):
        self.clean_starts_on()
        refused = self.answered(self.transaction(422, "/api/v1/admin/tls-reports?limit=0"), [], 422)
        self.hooks.the_listing_holds_what_was_made(refused)
        self.assertNotIn("fail", refused)
        # A 200 that answered something else fails on its status, not here.
        transaction = self.tls_listing([])
        transaction["real"]["statusCode"] = "500"
        self.hooks.the_listing_holds_what_was_made(transaction)
        self.assertNotIn("fail", transaction)

    def test_a_run_without_a_reset_is_left_alone(self):
        for transaction in (
            self.tls_listing([]),
            self.answered(self.transaction(200, "/api/v1/admin/pipelines"), []),
        ):
            self.hooks.the_listing_holds_what_was_made(transaction)
            self.assertNotIn("fail", transaction)
        self.assertEqual(self.server.calls, [])
        # Without the switch the before hooks note nothing either.
        transaction = self.transaction(200, "/api/v1/admin/tls-reports")
        self.hooks.report_and_audit_reads_after_a_reset(transaction)
        self.assertNotIn("listing_made", self.stub.stash)

if __name__ == "__main__":
    unittest.main()
