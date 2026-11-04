"""The moved operations' phase: what it selects, and that only it resets (#1385)."""

import contextlib
import io
import json
import re
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import own_data

ROOT = Path(__file__).resolve().parents[2]

# What reads or wrote the pipeline the run kept, and what follows its link.
PIPELINES = ("createPipeline", "testPipeline", "deletePipeline", "updatePipeline")

# What read or wrote the alias the run kept, which is also all createAlias's
# link reaches.
ALIASES = ("createAlias", "getAlias", "updateAlias", "deleteAlias")

# What read or wrote the custom filter the run kept, which is also all
# createCustomFilter's link reaches.
CUSTOM_FILTERS = (
    "createCustomFilter",
    "getCustomFilter",
    "updateCustomFilter",
    "testCustomFilter",
    "deleteCustomFilter",
)

# What read or wrote the folders the run kept, and what follows createFolder's
# link: the rename, and the delete after it.
FOLDERS = ("createFolder", "renameFolder", "deleteFolder")

# What read or wrote the invitable account count, of the invitation operations.
# No link starts from or reaches either.
INVITATIONS = ("inviteAccount", "acceptInvitation")

# Every grant operation of the area map. No link starts from or reaches any.
GRANTS = (
    "createGrant",
    "deleteGrant",
    "activateGrant",
    "deactivateGrant",
    "makeGrantEligible",
    "listGrants",
    "listMyGrants",
)

# Every domain sender rule operation of the area map. Each follows
# createDomain's link, and each add's link reaches only the remove of its list.
DKIM = ("setDKIMKey", "deleteDKIMKey", "getDKIMRotation", "setDKIMRotation")

# createDomain, the delete, the policy file, and what createDomain's link reaches
# that no group above holds.
DOMAINS = (
    "createDomain",
    "deleteDomain",
    "domainDNSCheck",
    "getDomainRelay",
    "setDomainRelay",
    "listSuppressions",
    "mtaStsPolicyFile",
)

SENDER_RULES = (
    "listAllowlist",
    "addToAllowlist",
    "removeFromAllowlist",
    "listBlocklist",
    "addToBlocklist",
    "removeFromBlocklist",
)

# Every contact operation of the area map. createContact's link reaches only the
# update and the delete. listAccounts' link reaches the others but the
# reply-time setting, and listAccounts is not selected: each of those exchanges
# carries its own account id, which its hook sends to the seeded account.
CONTACTS = (
    "createContact",
    "updateContact",
    "deleteContact",
    "listContacts",
    "blockSender",
    "importContacts",
    "suggestContacts",
    "getContactReplyTimeSetting",
    "setContactReplyTimeSetting",
)

# The send operations: the ones whose hooks read or write sender_mailboxes or
# test_send_mailboxes. No link reaches either. sendMessage's 201 link reaches the
# message operations, and their links three more, none moved or selected: the
# reset is before the send, never between it and a follower.
SENDS = ("sendMessage", "sendTestEmail")
# What sendMessage's link reaches, and the draft operations, whose links reach
# one another: all moved together, each follower on a message of its own.
DRAFTS = ("saveDraft", "updateDraft", "sendDraft")
# The mailbox administration operations: createAccount, what its link reaches,
# and the list and the sessions' end, which no link reaches.
MAILBOXES = (
    "createAccount",
    "getMailbox",
    "updateMailbox",
    "deleteMailbox",
    "listMailboxes",
    "endMailboxSessions",
)
# The thread and import operations: no link starts from or reaches any of them.
THREADS_AND_IMPORTS = ("listThreads", "setThreadDue", "snoozeThread", "importMessage")
# The account settings operations: putSieveScript's link reaches the read, and
# the read's the delete. No link reaches or starts from the others.
ACCOUNT_SETTINGS = (
    "getVacation",
    "setVacation",
    "disableVacation",
    "getSieveScript",
    "putSieveScript",
    "deleteSieveScript",
    "validateSieveScript",
    "getQuietHours",
    "setQuietHours",
    "getAccountSendLimits",
    "setAccountSendLimits",
    "getQuota",
)
# The delivery queue, held and scheduled mail operations: listQueue, what its
# link reaches, and the rest, which no link reaches.
QUEUE = (
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
# The credential and ban operations, with what each one's links reach: each
# create's revoke, all moved together.
CREDENTIALS_AND_BANS = {
    "listCredentials": set(),
    "createAppPassword": {"revokeCredential"},
    "revokeCredential": set(),
    "listAPIKeys": set(),
    "createAPIKey": {"revokeAPIKey"},
    "updateAPIKey": set(),
    "revokeAPIKey": set(),
    "listAdminAPIKeys": set(),
    "listClientSecrets": set(),
    "createClientSecret": set(),
    "revokeClientSecret": set(),
    "listBans": set(),
    "createBan": {"deleteBan"},
    "deleteBan": set(),
    "unbanIP": set(),
}
# The pipeline reads, which no link starts from or reaches.
PIPELINE_READS = ("listPipelines", "listPipelineLogs", "getPipelineAnalytics")
# The server settings, which no link starts from or reaches.
SERVER_SETTINGS = (
    "listRoles",
    "listCapabilities",
    "getServerLimits",
    "updateServerLimits",
    "getServerCapabilities",
    "updateServerCapabilities",
)
# The DMARC and TLS report, audit and delivery reads, and the TLS report
# intake, which no link starts from or reaches.
REPORTS_AND_AUDIT = (
    "listDmarcReports",
    "listTlsReports",
    "receiveTlsReport",
    "listAuditEntries",
    "verifyAuditLog",
    "getDeliveryLogs",
    "getDashboardStats",
)
# The mailbox reads: listAccounts' link reaches them, and no link starts from
# any of them.
MAILBOX_READS = ("listFolders", "listMessages", "searchMessages")
# The attachment download: no link starts from or reaches it.
ATTACHMENTS = ("getAttachment",)
# The delivery and federation operations: no link starts from or reaches any.
DELIVERY = (
    "checkMailboxAddress",
    "deliverMessage",
    "restmailDiscover",
    "restmailCapabilities",
    "restmailCheckMailbox",
    "restmailDeliver",
)
# The rest of the filter operations, which no link starts from or reaches.
FILTERS = ("testFilter", "listCustomFilters", "validateCustomFilter")
# The public reads and the mail client discovery reads, which no link starts
# from or reaches.
PUBLIC_READS = (
    "getHealth",
    "describeServer",
    "apiRoot",
    "swaggerUI",
    "openapiSpec",
    "openapiDocument",
    "prometheusMetrics",
    "mozillaAutoconfig",
    "mozillaAutoconfigWellKnown",
    "microsoftAutodiscover",
)
# The domain administration lists, which no link starts from or reaches.
DOMAIN_LISTS = ("listDomains", "listAliases", "listDKIMKeys", "listCertificates")
# The certificate operations: createCertificate's link reaches the read, and
# the read's the delete.
CERTIFICATES = ("createCertificate", "getCertificate", "deleteCertificate")
# The account link operations: linkAccount's link reaches the unlink.
ACCOUNT_LINKS = ("linkAccount", "unlinkAccount")
# The token exchange, which no link starts from or reaches. The client secret
# operations moved with the credentials (CREDENTIALS_AND_BANS).
TOKEN_EXCHANGE = ("issueClientCredentialsToken",)
# The sign-in and recovery operations, which no link starts from or reaches.
SIGN_IN_AND_RECOVERY = (
    "login",
    "logout",
    "refreshToken",
    "protocolSignIn",
    "resetAdmin",
    "recoverAccount",
    "ownerChallenge",
    "ownerToken",
    "ownerStatus",
    "getTwoFactorStatus",
    "enrollTwoFactor",
    "confirmTwoFactor",
    "disableTwoFactor",
    "changeOwnPassword",
    "requestRecoveryEmail",
    "redeemRecoveryEmail",
    "getRecoveryEmail",
    "registerRecoveryEmail",
    "recoveryEmailLink",
    "verifyRecoveryEmail",
)
# The last operations step 2 found free. No link starts from any of them; only
# listAccounts' reaches the account read and the calendar list.
LAST_FREE = (
    "getAccount",
    "listCalendarEvents",
    "respondToCalendar",
    "accountEvents",
    "probeServices",
    "verifyDelivery",
)
# Clearing a suppression and reading a DMARC report: no link starts from or
# reaches either.
MADE_THROUGH_MAIL = ("clearSuppression", "getDmarcReport")
TEST_DATA = (
    "resetTestData",
    "seedTestData",
    "snapshotTestData",
    "restoreTestSnapshot",
)
FOLLOWS_THE_SENT_MESSAGE = {
    "getMessage",
    "updateMessage",
    "getRawMessage",
    "forwardMessage",
    "listAttachments",
    "getMessageTrace",
    "deleteMessage",
    "verifyMessageSeal",
    "getThread",
}


def link_followers(description, start):
    """The operations reachable from start through the description's links."""
    links = {}
    for item in description["paths"].values():
        for operation in item.values():
            if not isinstance(operation, dict) or "operationId" not in operation:
                continue
            for response in operation.get("responses", {}).values():
                for link in (response.get("links") or {}).values():
                    links.setdefault(operation["operationId"], set()).add(
                        link["operationId"]
                    )
    seen, waiting = set(), [start]
    while waiting:
        for follower in links.get(waiting.pop(), ()):
            if follower not in seen:
                seen.add(follower)
                waiting.append(follower)
    return seen


def description_operations(description):
    """Every operation id the description declares."""
    return [
        operation["operationId"]
        for item in description["paths"].values()
        for operation in item.values()
        if isinstance(operation, dict) and "operationId" in operation
    ]


class Selection(unittest.TestCase):
    def test_the_run_is_narrowed_to_these_operations(self):
        config = {
            "exclude-operation-id": [],
            "surface": {"exclude": []},
            "unproven": ".cache/contract/unproven.json",
        }
        out = own_data.configure(config)
        self.assertEqual(
            out["operation-id"],
            list(own_data.MOVED),
        )
        for operation in ("resetTestData", "seedTestData", "snapshotTestData", "restoreTestSnapshot"):
            self.assertIn(operation, out["operation-id"])
        self.assertEqual(out["exclude-operation-id"], [])
        self.assertNotIn("surface", out)
        self.assertEqual(out["unproven"], ".cache/contract/unproven-own-data.json")

    def test_it_provides_the_states_its_hooks_make(self):
        out = own_data.configure({"provides": {"principal": ["anonymous"], "state": ["x"]}})
        self.assertEqual(
            out["provides"],
            {"principal": ["anonymous"], "state": ["calendar_invitation", "disposable_data", "x"]},
        )
        self.assertEqual(
            own_data.configure({"provides": {}})["provides"],
            {"state": ["calendar_invitation", "disposable_data"]},
        )

    def test_each_provided_state_is_only_the_moved_examples_its_hooks_make(self):
        # A provided state sends every selected example that requires it, so
        # each must be exactly the examples the hooks set up after the reset.
        pairs = json.loads((ROOT / "openapi-example.json").read_text())
        requiring = {
            (operation, name)
            for operation, examples in pairs.items()
            for name, pair in examples.items()
            if pair.get("requires", {}).get("state") in own_data.PROVIDES["state"]
            and operation in own_data.MOVED
        }
        self.assertEqual(
            requiring,
            {
                ("respondToCalendar", "accepted"),
                ("respondToCalendar", "foreign_sender"),
                ("resetTestData", "admin"),
                ("restoreTestSnapshot", "admin"),
                ("restoreTestSnapshot", "unknown_field"),
            },
        )

    def test_only_this_run_sends_what_wipes_the_data(self):
        # The primary run provides no disposable_data, so it sends none of the
        # wiping exchanges; every example of the two operations it could send
        # requires something, so none of their transactions is sent there,
        # nor are their coverage probes or fuzz requests (#1334).
        pairs = json.loads((ROOT / "openapi-example.json").read_text())
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        provided = config.get("provides") or {}
        for operation in ("resetTestData", "restoreTestSnapshot"):
            for name, pair in pairs[operation].items():
                with self.subTest(operation=operation, name=name):
                    requires = pair.get("requires") or {}
                    self.assertTrue(requires, "sent by the primary run")
                    for kind, value in requires.items():
                        self.assertNotIn(value, provided.get(kind) or [])
        wiping = {
            (operation, name)
            for operation in ("resetTestData", "restoreTestSnapshot")
            for name, pair in pairs[operation].items()
            if (pair.get("requires") or {}).get("state") == "disposable_data"
        }
        self.assertEqual(
            wiping,
            {
                ("resetTestData", "admin"),
                ("restoreTestSnapshot", "admin"),
                ("restoreTestSnapshot", "unknown_field"),
            },
        )

    def test_it_selects_everything_a_reset_takes_the_created_domain_from(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        selected = set(own_data.MOVED)
        self.assertEqual(
            selected
            - set(PIPELINES)
            - set(ALIASES)
            - set(CUSTOM_FILTERS)
            - set(FOLDERS)
            - set(INVITATIONS)
            - set(GRANTS)
            - set(CONTACTS)
            - set(SENDS)
            - FOLLOWS_THE_SENT_MESSAGE
            - set(DRAFTS)
            - set(MAILBOXES)
            - set(THREADS_AND_IMPORTS)
            - set(ACCOUNT_SETTINGS)
            - set(QUEUE)
            - set(CREDENTIALS_AND_BANS)
            - set(PIPELINE_READS)
            - set(SERVER_SETTINGS)
            - set(REPORTS_AND_AUDIT)
            - set(MAILBOX_READS)
            - set(ATTACHMENTS)
            - set(DELIVERY)
            - set(FILTERS)
            - set(PUBLIC_READS)
            - set(DOMAIN_LISTS)
            - set(CERTIFICATES)
            - set(ACCOUNT_LINKS)
            - set(TOKEN_EXCHANGE)
            - set(SIGN_IN_AND_RECOVERY)
            - set(LAST_FREE)
            - set(TEST_DATA)
            - set(MADE_THROUGH_MAIL),
            link_followers(description, "createDomain") | {"createDomain"},
        )

    def test_it_selects_every_pipeline_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "createPipeline"),
            {"updatePipeline", "deletePipeline"},
        )
        self.assertLessEqual(set(PIPELINES), set(own_data.MOVED))
        self.assertFalse(hasattr(own_data, "FOLLOWS_THE_CREATED_PIPELINE"))

    def test_the_pipeline_reads_have_moved_and_no_link_touches_them(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in (
            PIPELINE_READS + FILTERS + PUBLIC_READS + DOMAIN_LISTS + TOKEN_EXCHANGE + SIGN_IN_AND_RECOVERY
        ):
            self.assertEqual(link_followers(description, operation), set())
            for start in description_operations(description):
                self.assertNotIn(operation, link_followers(description, start), start)
        self.assertLessEqual(
            set(PIPELINE_READS + FILTERS + PUBLIC_READS + DOMAIN_LISTS + TOKEN_EXCHANGE + SIGN_IN_AND_RECOVERY),
            set(own_data.MOVED),
        )

    def test_the_last_free_operations_have_moved_and_only_list_accounts_links_to_them(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in LAST_FREE:
            self.assertEqual(link_followers(description, operation), set())
        for start in description_operations(description):
            reached = set(LAST_FREE) & link_followers(description, start)
            if start == "listAccounts":
                self.assertEqual(reached, {"getAccount", "listCalendarEvents"})
            else:
                self.assertFalse(reached, start)
        self.assertNotIn("listAccounts", own_data.MOVED)
        self.assertLessEqual(set(LAST_FREE), set(own_data.MOVED))

    def test_the_test_data_operations_have_moved_and_no_link_touches_them(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in TEST_DATA:
            self.assertEqual(link_followers(description, operation), set())
        for start in description_operations(description):
            self.assertFalse(set(TEST_DATA) & link_followers(description, start), start)
        self.assertLessEqual(set(TEST_DATA), set(own_data.MOVED))
        self.assertEqual(set(TEST_DATA) & set(own_data.MOVED[:-len(TEST_DATA)]), set())

    def test_the_suppression_clear_and_dmarc_read_have_moved_and_no_link_reaches_them(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in MADE_THROUGH_MAIL:
            self.assertEqual(link_followers(description, operation), set())
        for start in description_operations(description):
            self.assertFalse(set(MADE_THROUGH_MAIL) & link_followers(description, start), start)
        self.assertLessEqual(set(MADE_THROUGH_MAIL), set(own_data.MOVED))

    def test_it_selects_every_account_link_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "linkAccount") | {"linkAccount"},
            set(ACCOUNT_LINKS),
        )
        for start in description_operations(description):
            if start not in ACCOUNT_LINKS:
                self.assertFalse(set(ACCOUNT_LINKS) & link_followers(description, start), start)
        self.assertLessEqual(set(ACCOUNT_LINKS), set(own_data.MOVED))

    def test_it_selects_every_certificate_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "createCertificate") | {"createCertificate"},
            set(CERTIFICATES),
        )
        for start in description_operations(description):
            if start not in CERTIFICATES:
                self.assertFalse(set(CERTIFICATES) & link_followers(description, start), start)
        self.assertLessEqual(set(CERTIFICATES), set(own_data.MOVED))

    def test_the_server_settings_have_moved_and_no_link_touches_them(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in SERVER_SETTINGS:
            self.assertEqual(link_followers(description, operation), set())
            for start in description_operations(description):
                self.assertNotIn(operation, link_followers(description, start), start)
        self.assertLessEqual(set(SERVER_SETTINGS), set(own_data.MOVED))

    def test_the_report_and_audit_reads_have_moved_and_no_link_touches_them(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in REPORTS_AND_AUDIT:
            self.assertEqual(link_followers(description, operation), set())
            for start in description_operations(description):
                self.assertNotIn(operation, link_followers(description, start), start)
        self.assertLessEqual(set(REPORTS_AND_AUDIT), set(own_data.MOVED))

    def test_the_mailbox_reads_have_moved_and_only_listaccounts_reaches_them(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in MAILBOX_READS:
            self.assertEqual(link_followers(description, operation), set())
            linking = {
                start
                for start in description_operations(description)
                if operation in link_followers(description, start)
            }
            self.assertEqual(linking, {"listAccounts"}, operation)
        self.assertNotIn("listAccounts", own_data.configure({})["operation-id"])
        self.assertLessEqual(set(MAILBOX_READS), set(own_data.MOVED))

    def test_the_attachment_download_has_moved_and_no_link_touches_it(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in ATTACHMENTS:
            self.assertEqual(link_followers(description, operation), set())
            for start in description_operations(description):
                self.assertNotIn(operation, link_followers(description, start), start)
        self.assertLessEqual(set(ATTACHMENTS), set(own_data.MOVED))

    def test_the_delivery_operations_have_moved_and_no_link_touches_them(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in DELIVERY:
            self.assertEqual(link_followers(description, operation), set())
            for start in description_operations(description):
                self.assertNotIn(operation, link_followers(description, start), start)
        self.assertLessEqual(set(DELIVERY), set(own_data.MOVED))

    def test_it_selects_every_alias_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "createAlias") | {"createAlias"}, set(ALIASES)
        )
        self.assertLessEqual(set(ALIASES), set(own_data.MOVED))

    def test_it_selects_every_custom_filter_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "createCustomFilter") | {"createCustomFilter"},
            set(CUSTOM_FILTERS),
        )
        self.assertLessEqual(set(CUSTOM_FILTERS), set(own_data.MOVED))

    def test_it_selects_every_folder_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "createFolder") | {"createFolder"},
            set(FOLDERS),
        )
        self.assertLessEqual(set(FOLDERS), set(own_data.MOVED))

    def test_it_selects_the_invitation_operations_which_no_link_reaches(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in INVITATIONS:
            self.assertEqual(link_followers(description, operation), set())
        reached = set()
        for operation in description_operations(description):
            reached |= link_followers(description, operation)
        self.assertFalse(reached & set(INVITATIONS))
        self.assertLessEqual(set(INVITATIONS), set(own_data.MOVED))

    def test_it_selects_every_grant_operation_which_no_link_reaches(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        areas = json.loads(
            (ROOT / "tests/contract/api-areas.json").read_text()
        )
        in_the_map = {
            operation
            for operations in areas.values()
            if isinstance(operations, list)
            for operation in operations
            if "Grant" in operation
        }
        self.assertEqual(in_the_map, set(GRANTS))
        for operation in GRANTS:
            self.assertEqual(link_followers(description, operation), set())
        reached = set()
        for operation in description_operations(description):
            reached |= link_followers(description, operation)
        self.assertFalse(reached & set(GRANTS))
        self.assertLessEqual(set(GRANTS), set(own_data.MOVED))

    def test_it_selects_every_sender_rule_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        areas = json.loads(
            (ROOT / "tests/contract/api-areas.json").read_text()
        )
        self.assertEqual(
            {
                operation
                for operation in areas["646"]
                if operation.endswith(("Allowlist", "Blocklist"))
            },
            set(SENDER_RULES),
        )
        self.assertEqual(
            link_followers(description, "addToAllowlist"), {"removeFromAllowlist"}
        )
        self.assertEqual(
            link_followers(description, "addToBlocklist"), {"removeFromBlocklist"}
        )
        for operation in SENDER_RULES:
            if not operation.startswith("addTo"):
                self.assertEqual(link_followers(description, operation), set())
        self.assertLessEqual(
            set(SENDER_RULES), link_followers(description, "createDomain")
        )
        self.assertLessEqual(set(SENDER_RULES), set(own_data.MOVED))

    def test_it_selects_every_contact_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        areas = json.loads(
            (ROOT / "tests/contract/api-areas.json").read_text()
        )
        self.assertEqual(set(areas["639"]), set(CONTACTS))
        self.assertEqual(
            link_followers(description, "createContact"),
            {"updateContact", "deleteContact"},
        )
        linking = {
            operation["operationId"]
            for item in description["paths"].values()
            for operation in item.values()
            if isinstance(operation, dict) and "operationId" in operation
            and link_followers(description, operation["operationId"]) & set(CONTACTS)
        }
        self.assertEqual(linking, {"createContact", "listAccounts"})
        for operation in set(CONTACTS) - {"createContact"}:
            self.assertEqual(link_followers(description, operation), set())
        selected = own_data.configure({})["operation-id"]
        self.assertNotIn("listAccounts", selected)
        self.assertLessEqual(set(CONTACTS), set(own_data.MOVED))

    def test_it_selects_the_send_operations_and_what_follows_the_send(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        hooks = (ROOT / "tests/contract/hooks.py").read_text()
        readers = set()
        for chunk in hooks.split("\n\n\n"):
            if "sender_mailboxes" in chunk or "test_send_mailboxes" in chunk:
                readers |= set(re.findall(r'@hooks\.before\("(\w+)"\)', chunk))
        # a_signing_mailbox_at is called from sendMessage's hook alone.
        self.assertEqual(readers, set(SENDS))
        self.assertEqual(hooks.count("a_signing_mailbox_at("), 3)
        self.assertEqual(
            link_followers(description, "sendMessage"), FOLLOWS_THE_SENT_MESSAGE
        )
        self.assertEqual(link_followers(description, "sendTestEmail"), set())
        reached = set()
        for operation in description_operations(description):
            reached |= link_followers(description, operation)
        self.assertFalse(reached & set(SENDS))
        self.assertLessEqual(set(SENDS), set(own_data.MOVED))
        self.assertLessEqual(FOLLOWS_THE_SENT_MESSAGE, set(own_data.MOVED))
        # No other link reaches what follows the send.
        for operation in description_operations(description):
            if operation not in set(SENDS) | FOLLOWS_THE_SENT_MESSAGE:
                self.assertFalse(
                    link_followers(description, operation) & FOLLOWS_THE_SENT_MESSAGE,
                    operation,
                )

    def test_it_selects_every_draft_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "saveDraft"), {"updateDraft", "sendDraft"}
        )
        for operation in description_operations(description):
            if operation not in DRAFTS:
                self.assertFalse(
                    link_followers(description, operation) & set(DRAFTS), operation
                )
        self.assertLessEqual(set(DRAFTS), set(own_data.MOVED))

    def test_it_selects_every_dkim_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(link_followers(description, "setDKIMKey"), {"deleteDKIMKey"})
        for operation in DKIM:
            if operation != "setDKIMKey":
                self.assertEqual(link_followers(description, operation), set())
        self.assertLessEqual(set(DKIM), link_followers(description, "createDomain"))
        self.assertLessEqual(set(DKIM), set(own_data.MOVED))

    def test_it_selects_every_queue_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "listQueue"),
            {"getQueueEntry", "retryQueueEntry", "bounceQueueEntry", "deleteQueueEntry"},
        )
        for operation in set(QUEUE) - link_followers(description, "listQueue") - {"listQueue"}:
            self.assertEqual(link_followers(description, operation), set(), operation)
        self.assertLessEqual(set(QUEUE), set(own_data.MOVED))

    def test_it_selects_every_mailbox_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "createAccount"),
            {"getMailbox", "updateMailbox", "deleteMailbox"},
        )
        for operation in ("listMailboxes", "endMailboxSessions"):
            self.assertEqual(link_followers(description, operation), set())
        self.assertLessEqual(set(MAILBOXES), set(own_data.MOVED))

    def test_it_selects_every_thread_and_import_operation(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in THREADS_AND_IMPORTS:
            self.assertEqual(link_followers(description, operation), set())
            for start in description_operations(description):
                self.assertNotIn(operation, link_followers(description, start), start)
        self.assertLessEqual(set(THREADS_AND_IMPORTS), set(own_data.MOVED))

    def test_it_selects_every_account_setting_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertEqual(
            link_followers(description, "putSieveScript"),
            {"getSieveScript", "deleteSieveScript"},
        )
        for operation in set(ACCOUNT_SETTINGS) - {"putSieveScript", "getSieveScript"}:
            self.assertEqual(link_followers(description, operation), set(), operation)
        self.assertLessEqual(set(ACCOUNT_SETTINGS), set(own_data.MOVED))

    def test_it_selects_every_credential_and_ban_operation_and_its_links(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation, followers in CREDENTIALS_AND_BANS.items():
            self.assertEqual(link_followers(description, operation), followers, operation)
        self.assertLessEqual(set(CREDENTIALS_AND_BANS), set(own_data.MOVED))

    def test_everything_that_follows_the_created_domain_has_moved(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        self.assertLessEqual(
            {"domainDNSCheck", "getDomainRelay", "setDomainRelay", "listSuppressions"},
            link_followers(description, "createDomain"),
        )
        self.assertLessEqual(set(DOMAINS), set(own_data.MOVED))
        self.assertLessEqual(
            link_followers(description, "createDomain") | {"createDomain"},
            set(own_data.MOVED),
        )
        self.assertFalse(hasattr(own_data, "FOLLOWS_THE_CREATED_DOMAIN"))

    def test_a_skipped_exchange_is_not_a_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory, "own-data.xml")
            report.write_text('<testsuite><testcase name="sent"/></testsuite>')
            self.assertEqual(own_data.main(["--check", str(report)]), 0)
            report.write_text(
                '<testsuite><testcase name="sent"/><testcase name="not"><skipped/></testcase></testsuite>'
            )
            self.assertEqual(own_data.main(["--check", str(report)]), 1)


class Wiring(unittest.TestCase):
    def test_only_the_own_data_phase_resets(self):
        script = (ROOT / "tests/contract/run.sh").read_text()
        self.assertEqual(script.count("CONTRACT_RESET_SEED=1"), 1)
        phase = script[script.index("\nown-data)\n") : script.index("\nfuzz)\n")]
        self.assertIn("CONTRACT_RESET_SEED=1", phase)
        self.assertIn("own_data.py --check", phase)
        self.assertNotIn("CONTRACT_RESET_SEED", (ROOT / "chores.yml").read_text())
        self.assertNotIn("CONTRACT_RESET_SEED", (ROOT / "vertrag.yml").read_text())
        self.assertNotIn(
            "CONTRACT_RESET_SEED",
            (ROOT / ".github/workflows/e2e-suite.yml").read_text(),
        )

    def test_the_hooks_make_their_data_on_the_runs_endpoint(self):
        # The gateway routes are sent to the internal listener, which serves
        # nothing else: run.sh names the run's endpoint for the setup calls.
        script = (ROOT / "tests/contract/run.sh").read_text()
        phase = script[script.index("\nown-data)\n") : script.index("\nfuzz)\n")]
        export = phase.index("export CONTRACT_SETUP_ORIGIN")
        self.assertIn('own_data.py --endpoint "$out/vertrag-own-data.yml"', phase[:export])
        self.assertLess(export, phase.index("CONTRACT_RESET_SEED=1"))
        with tempfile.TemporaryDirectory() as scratch:
            config = Path(scratch) / "own-data.yml"
            config.write_text(yaml.safe_dump({"endpoint": "http://10.99.0.20:8080", "endpoints": [
                {"endpoint": "https://10.99.0.20:8443", "operation-id": ["deliverMessage"]}]}))
            printed = io.StringIO()
            with contextlib.redirect_stdout(printed):
                self.assertEqual(own_data.main(["--endpoint", str(config)]), 0)
        self.assertEqual(printed.getvalue(), "http://10.99.0.20:8080\n")

    def test_it_is_the_last_gate_and_there_is_no_teardown(self):
        chores = (ROOT / "chores.yml").read_text()
        own = chores.index("tests/contract/run.sh own-data {{.CLI_ARGS}} || code=$?")
        self.assertLess(chores.index("  test:contract-own-data:"), own)
        self.assertNotIn("run.sh teardown", chores)
        self.assertNotIn("teardown)", (ROOT / "tests/contract/run.sh").read_text())
        self.assertFalse((ROOT / "tests/contract/teardown.py").exists())
        workflow = yaml.safe_load((ROOT / ".github/workflows/e2e-suite.yml").read_text())
        steps = workflow["jobs"]["suite"]["steps"]
        names = [step.get("name", "") for step in steps]
        step = next(s for s in steps if s.get("run") == "chore test:contract-own-data")
        self.assertNotIn("continue-on-error", step)
        self.assertEqual(step["if"], "inputs.contract-phases == 'gate'")
        self.assertLess(names.index("Limits suite"), names.index(step["name"]))
        self.assertLess(
            names.index("Contract tests (example pairs, examples, coverage, secret scan and authorization report)"),
            names.index(step["name"]),
        )

    def test_nothing_is_taken_out_of_the_primary_run(self):
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        self.assertEqual(config["exclude-operation-id"], [])
        self.assertEqual(config["exclude-matching"], [])
        self.assertEqual(config["surface"]["exclude"], [])


if __name__ == "__main__":
    unittest.main()
