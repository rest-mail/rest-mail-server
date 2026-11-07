"""The moved operations' phase: what it selects, and that only it resets (#1385)."""

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
            "exclude-operation-id": ["resetTestData", "restoreTestSnapshot"],
            "surface": {"exclude": []},
            "unproven": ".cache/contract/unproven.json",
        }
        out = own_data.configure(config)
        self.assertEqual(
            out["operation-id"],
            list(own_data.MOVED),
        )
        self.assertEqual(
            out["exclude-operation-id"], ["resetTestData", "restoreTestSnapshot"]
        )
        self.assertNotIn("surface", out)
        self.assertEqual(out["unproven"], ".cache/contract/unproven-own-data.json")

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
            - set(SERVER_SETTINGS),
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
        for operation in PIPELINE_READS:
            self.assertEqual(link_followers(description, operation), set())
            for start in description_operations(description):
                self.assertNotIn(operation, link_followers(description, start), start)
        self.assertLessEqual(set(PIPELINE_READS), set(own_data.MOVED))

    def test_the_server_settings_have_moved_and_no_link_touches_them(self):
        description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
        for operation in SERVER_SETTINGS:
            self.assertEqual(link_followers(description, operation), set())
            for start in description_operations(description):
                self.assertNotIn(operation, link_followers(description, start), start)
        self.assertLessEqual(set(SERVER_SETTINGS), set(own_data.MOVED))

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
        # createGrant links deleteGrant, the delete of what it made (#1780): a
        # link inside the moved grant operations, which all start clean. No
        # other link reaches a grant operation, and none leaves one.
        for operation in GRANTS:
            self.assertLessEqual(link_followers(description, operation), set(GRANTS))
        reached = set()
        for operation in description_operations(description):
            if operation not in GRANTS:
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
        phase = script[script.index("\nown-data)\n") : script.index("\nteardown)\n")]
        self.assertIn("CONTRACT_RESET_SEED=1", phase)
        self.assertIn("own_data.py --check", phase)
        self.assertNotIn("CONTRACT_RESET_SEED", (ROOT / "chores.yml").read_text())
        self.assertNotIn("CONTRACT_RESET_SEED", (ROOT / "vertrag.yml").read_text())
        self.assertNotIn(
            "CONTRACT_RESET_SEED",
            (ROOT / ".github/workflows/e2e-suite.yml").read_text(),
        )

    def test_it_is_a_gate_sent_just_before_the_teardown(self):
        chores = (ROOT / "chores.yml").read_text()
        own = chores.index("tests/contract/run.sh own-data {{.CLI_ARGS}} || code=$?")
        teardown = chores.index(
            "tests/contract/run.sh teardown {{.CLI_ARGS}} || code=$?"
        )
        self.assertLess(own, teardown)
        self.assertLess(chores.index("  test:contract-teardown:"), own)

    def test_nothing_is_taken_out_of_the_primary_run(self):
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        self.assertEqual(
            config["exclude-operation-id"], ["resetTestData", "restoreTestSnapshot"]
        )
        self.assertEqual(config["exclude-matching"], [])
        self.assertEqual(
            [item["operation"] for item in config["surface"]["exclude"]],
            ["resetTestData", "restoreTestSnapshot"],
        )


if __name__ == "__main__":
    unittest.main()
