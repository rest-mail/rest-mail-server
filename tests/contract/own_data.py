"""The operations that start from a clean database, in a run of their own (#1385).

MOVED resets and seeds before each exchange and makes the data that exchange
needs (hooks.py, start_clean). A reset removes what createDomain,
createPipeline, createAlias, createCustomFilter, createFolder, createContact
and createAccount made, so the run also sends everything that follows their
links, which must still pass with that domain, pipeline, alias, custom filter,
folder, contact or account gone: the same operations without a reset are the comparison.

A reset also removes every principal and fixture the other phases use, and the
primary run still has operations that depend on them, so this is sent after
those phases, just before the teardown. The primary run keeps sending every one
of these operations, without a reset, until the last of them has moved.

The configuration is the run's own, narrowed to these operations and without the
surface gate, which is about the whole API. That gate is also what fails a skipped
exchange, so --check does that here: a link that resolved to nothing is not a pass.

Usage: own_data.py <vertrag-run.yml> <out.yml>
       own_data.py --check <own-data.xml>
       own_data.py --endpoint <vertrag-own-data.yml>
"""

import sys
import xml.etree.ElementTree as ET

import yaml

MOVED = (
    "getDomain",
    "setMtaStsPolicy",
    "getMtaStsPolicy",
    "deleteMtaStsPolicy",
    "createPipeline",
    "testPipeline",
    "deletePipeline",
    # createAlias's link reaches only these three, all moved.
    "createAlias",
    "getAlias",
    "updateAlias",
    "deleteAlias",
    # createCustomFilter's link reaches get, update and test, and test's
    # reaches delete: all moved.
    "createCustomFilter",
    "getCustomFilter",
    "updateCustomFilter",
    "testCustomFilter",
    "deleteCustomFilter",
    # createFolder's link reaches rename, and rename's reaches delete: all
    # moved.
    "createFolder",
    "renameFolder",
    "deleteFolder",
    # updateDomain certifies a domain before it goes live. It follows
    # createDomain's link and no link starts from it.
    "updateDomain",
    # inviteAccount and acceptInvitation each make the account they invite. No
    # link starts from or reaches either.
    "inviteAccount",
    "acceptInvitation",
    # The grant operations. deleteGrant makes the grant it deletes; the others'
    # examples need nothing made. No link starts from or reaches any of them.
    "createGrant",
    "deleteGrant",
    "activateGrant",
    "deactivateGrant",
    "makeGrantEligible",
    "listGrants",
    "listMyGrants",
    # The sender rule operations. Each follows createDomain's link, and the
    # add's link reaches only the remove of its list: all moved.
    "listAllowlist",
    "addToAllowlist",
    "removeFromAllowlist",
    "listBlocklist",
    "addToBlocklist",
    "removeFromBlocklist",
    # The contact operations. createContact's link reaches only the update and
    # the delete, and no other link starts from or reaches any of them: all
    # moved.
    "createContact",
    "updateContact",
    "deleteContact",
    "listContacts",
    "blockSender",
    "importContacts",
    "suggestContacts",
    "getContactReplyTimeSetting",
    "setContactReplyTimeSetting",
    # The send operations. No link reaches either. sendMessage's 201 link
    # reaches the message operations below, and getMessage's and
    # getRawMessage's links three more: all moved. Each follower gets a message
    # of its own after its reset, since the sent one went with it.
    "sendMessage",
    "sendTestEmail",
    "getMessage",
    "updateMessage",
    "getRawMessage",
    "forwardMessage",
    "listAttachments",
    "getMessageTrace",
    "deleteMessage",
    "verifyMessageSeal",
    "getThread",
    # The draft operations. saveDraft's link reaches the update, and the
    # update's the send: all moved. The update gets a draft of its own after
    # its reset, and the send makes its own already.
    "saveDraft",
    "updateDraft",
    "sendDraft",
    # The DKIM operations. Each follows createDomain's link, and setDKIMKey's
    # reaches only deleteDKIMKey: all moved. Each gets a domain of its own after
    # its reset, and the delete the key it deletes.
    "setDKIMKey",
    "deleteDKIMKey",
    "getDKIMRotation",
    "setDKIMRotation",
    # createDomain, and the rest of what its link reaches: the DNS check, the
    # relay and the suppressions, each on a domain of its own after its reset.
    # The policy file asks for a domain of its own, and the delete deletes one.
    # Nothing that follows createDomain is left unmoved.
    "createDomain",
    "deleteDomain",
    "domainDNSCheck",
    "getDomainRelay",
    "setDomainRelay",
    "listSuppressions",
    "mtaStsPolicyFile",
    # The mailbox administration operations. createAccount's link reaches only
    # the read, update and delete of a mailbox: all moved. Each of those, and
    # endMailboxSessions, gets an account of its own after its reset, since the
    # created one went with it. No link reaches the list.
    "createAccount",
    "getMailbox",
    "updateMailbox",
    "deleteMailbox",
    "listMailboxes",
    "endMailboxSessions",
    # The thread and import operations. No link starts from or reaches any of
    # them. Each goes to the fixture admin's mailbox after its reset; the list
    # of threads awaiting a reply gets one, and the due date and the snooze the
    # thread they change.
    "listThreads",
    "setThreadDue",
    "snoozeThread",
    "importMessage",
    # The account settings operations. getSieveScript's link reaches only the
    # delete, and putSieveScript's only the read: all moved. Each gets an
    # account of its own after its reset, since the examples' 1 is then an
    # account the fixture admin does not reach, and what a read or remove
    # needs is made on it.
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
    # The delivery queue, held and scheduled mail operations. listQueue's link
    # reaches the read, retry and bounce of an entry, and the bounce's the
    # delete: all moved. Each success gets an entry, an account or a
    # scheduled message of its own after its reset, made through the API.
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
    # The credential operations: app passwords, API keys and client secrets.
    # The app password's and the API key's create each link only to their
    # revoke, and no link starts from the rest: all moved. Each revoke gets a
    # credential of its own after its reset, since the created one went with it.
    "listCredentials",
    "createAppPassword",
    "revokeCredential",
    "listAPIKeys",
    "createAPIKey",
    "updateAPIKey",
    "revokeAPIKey",
    "listAdminAPIKeys",
    "listClientSecrets",
    "createClientSecret",
    "revokeClientSecret",
    # The ban operations. createBan's link reaches only deleteBan, which gets a
    # ban of its own after its reset: all moved.
    "listBans",
    "createBan",
    "deleteBan",
    "unbanIP",
    # The rest of the pipeline operations. updatePipeline follows
    # createPipeline's link, the last that followed one without a reset; it
    # updates a pipeline of its own after its reset. No link starts from or
    # reaches the three reads, and the list gets a pipeline of its own.
    "updatePipeline",
    "listPipelines",
    "listPipelineLogs",
    "getPipelineAnalytics",
    # The server settings. No link starts from or reaches any of them, and none
    # needs anything made: the reset puts the limits and the capability ceiling
    # back as a new installation has them.
    "listRoles",
    "listCapabilities",
    "getServerLimits",
    "updateServerLimits",
    "getServerCapabilities",
    "updateServerCapabilities",
    # The DMARC and TLS report, audit and delivery reads, and the TLS report
    # intake. No link starts from or reaches any of them. A list of TLS reports
    # gets one of its own after its reset; the others need nothing made.
    # getDmarcReport stays: no API call makes a received DMARC report.
    "listDmarcReports",
    "listTlsReports",
    "receiveTlsReport",
    "listAuditEntries",
    "verifyAuditLog",
    "getDeliveryLogs",
    "getDashboardStats",
    # The mailbox reads listAccounts' link reaches. listAccounts is not
    # selected, and no link starts from any of them. Each goes to the fixture
    # admin's mailbox after its reset, and a listing that succeeds gets a
    # folder or a message of its own to list.
    "listFolders",
    "listMessages",
    "searchMessages",
    # The attachment download. No link reaches it. A read of an attachment
    # that is there gets one of its own, with known bytes, after its reset.
    "getAttachment",
    # The delivery and federation operations: the gateway's mailbox check and
    # delivery, and RESTMAIL discovery, capabilities, mailbox check and
    # delivery. No link starts from or reaches any of them. A documented
    # delivery or mailbox check success goes to a mailbox of its own after its
    # reset; the others need nothing made. acmeChallenge stays: the server
    # serves it, and describes it, only with ACME on, which the topology is not.
    "checkMailboxAddress",
    "deliverMessage",
    "restmailDiscover",
    "restmailCapabilities",
    "restmailCheckMailbox",
    "restmailDeliver",
    # The rest of the filter operations. No link starts from or reaches any of
    # them. The list gets a custom filter of its own after its reset; the
    # preview and the validation need nothing made.
    "testFilter",
    "listCustomFilters",
    "validateCustomFilter",
    # The public reads and the mail client discovery reads. No link starts from
    # or reaches any of them. The public reads need nothing made; a discovery
    # read gets its address's domain activated after the reset.
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
    # The domain administration lists and the certificate operations.
    # createCertificate's link reaches the read, and the read's the delete:
    # all moved. No other link starts from or reaches any of them. Each list
    # gets a row of its own after its reset, and the read and the delete a
    # certificate of their own.
    "listDomains",
    "listAliases",
    "listDKIMKeys",
    "listCertificates",
    "createCertificate",
    "getCertificate",
    "deleteCertificate",
    # The account link operations and the token exchange. linkAccount's link
    # reaches only unlinkAccount, and no other link starts from or reaches any
    # of them. None needs anything made: the link's 201 links the seeded
    # eve@restmail.test, which the reset puts back unlinked, and the rest are
    # refusals. The client secret operations moved with the credentials.
    "linkAccount",
    "unlinkAccount",
    "issueClientCredentialsToken",
    # The sign-in and recovery operations. No link starts from or reaches any
    # of them. A success that ends a session, rotates one or starts a second
    # factor acts on an account made for it after its reset (logout, refresh
    # and enroll), and the mail client sign-in uses an app password of an
    # account made for it. The rest read, sign the fixture admin in, or are
    # refusals, and need nothing made.
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
    # The last operations step 2 found free. No link starts from any of them,
    # and only listAccounts' link reaches getAccount and listCalendarEvents;
    # listAccounts is not selected, so each is sent with its own id. The
    # account read and the calendar list go to the seeded account after their
    # reset, and the list gets an invitation of its own. The delivery check's
    # success asks about an address no mailbox has, and the probe's probes the
    # database: neither needs anything made. The rest are refusals.
    "getAccount",
    "listCalendarEvents",
    "respondToCalendar",
    "accountEvents",
    "probeServices",
    "verifyDelivery",
)


def configure(config):
    config["operation-id"] = list(MOVED)
    config.pop("surface", None)
    # The gate's list of what it could not send is its own.
    config["unproven"] = ".cache/contract/unproven-own-data.json"
    return config


def unsent(report):
    """The names of the exchanges a JUnit report skipped."""
    return [
        case.get("name")
        for case in ET.parse(report).getroot().iter("testcase")
        if case.find("skipped") is not None
    ]


def main(argv):
    if len(argv) == 2 and argv[0] == "--check":
        skipped = unsent(argv[1])
        for name in skipped:
            print("own data: not sent:", name)
        return 1 if skipped else 0
    if len(argv) == 2 and argv[0] == "--endpoint":
        # The run's endpoint, where the hooks make their setup calls.
        with open(argv[1]) as stream:
            print(yaml.safe_load(stream)["endpoint"])
        return 0
    if len(argv) != 2:
        sys.exit(
            "usage: own_data.py <vertrag-run.yml> <out.yml> | --check <own-data.xml>"
            " | --endpoint <vertrag-own-data.yml>"
        )
    source, out = argv
    with open(source) as stream:
        config = yaml.safe_load(stream)
    with open(out, "w") as stream:
        yaml.safe_dump(configure(config), stream, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
