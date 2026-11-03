"""The operations that start from a clean database, in a run of their own (#1385).

MOVED resets and seeds before each exchange and makes the data that exchange
needs (hooks.py, start_clean). A reset removes what createDomain,
createPipeline, createAlias, createCustomFilter, createFolder and createContact
made, so the run also sends everything that follows their links, which must
still pass with that domain, pipeline, alias, custom filter, folder or contact
gone: the same operations without a reset are the comparison.

A reset also removes every principal and fixture the other phases use, and the
primary run still has operations that depend on them, so this is sent after
those phases, just before the teardown. The primary run keeps sending every one
of these operations, without a reset, until the last of them has moved.

The configuration is the run's own, narrowed to these operations and without the
surface gate, which is about the whole API. That gate is also what fails a skipped
exchange, so --check does that here: a link that resolved to nothing is not a pass.

Usage: own_data.py <vertrag-run.yml> <out.yml>
       own_data.py --check <own-data.xml>
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
    # reaches the message operations, which have not moved and are not
    # selected here: the reset comes before the send, never between it and
    # what follows its link, so the primary run still follows that link.
    "sendMessage",
    "sendTestEmail",
)

FOLLOWS_THE_CREATED_DOMAIN = (
    "createDomain",
    "deleteDKIMKey",
    "deleteDomain",
    "domainDNSCheck",
    "getDKIMRotation",
    "getDomainRelay",
    "listSuppressions",
    "mtaStsPolicyFile",
    "setDKIMKey",
    "setDKIMRotation",
    "setDomainRelay",
)

# deletePipeline, the other, has moved.
FOLLOWS_THE_CREATED_PIPELINE = ("updatePipeline",)


def configure(config):
    config["operation-id"] = list(
        MOVED + FOLLOWS_THE_CREATED_DOMAIN + FOLLOWS_THE_CREATED_PIPELINE
    )
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
    if len(argv) != 2:
        sys.exit(
            "usage: own_data.py <vertrag-run.yml> <out.yml> | --check <own-data.xml>"
        )
    source, out = argv
    with open(source) as stream:
        config = yaml.safe_load(stream)
    with open(out, "w") as stream:
        yaml.safe_dump(configure(config), stream, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
