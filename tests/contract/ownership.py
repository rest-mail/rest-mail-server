#!/usr/bin/env python3
"""Writes the configuration of vertrag's ownership phase (#657).

Roles say which operations a caller may use, not on whose objects. The phase
groups the principals into tenants and sends each tenant's principals the other
tenant's ids, with operations they are entitled to; every answer must be a
refusal, 403 or 404 (#656). A success is an ownership bypass.

The tenants are the two password accounts tests/e2e/contract_principals_test.go
makes and writes to .cache/contract-principals.json, each with its app password:

    tenant   principals                    roles
    a        ordinary, app-password        mail:read, mail:write, mail:send on
    b        other, other-app-password     its own mailbox; the password
                                           session also mailbox:manage

An account id is not made by any operation, so each tenant declares its own
(vertrag's tenant `ids`). Only the account-scoped operations are selected, as
the declared id is the account's, with the account list beside them, which
must not show the other tenant's account. Keep this run report-only until it has
run clean.

With --domains it writes the second run instead: the administrators
`chore test:contract-domain-tenants` makes, one of restmail.test (tenant a) and
one of contract-other.test (tenant b), each holding the admin role on its own
domain, sent the other's domain id on the /api/v1/admin/domains/{id} operations.
Tenant ids are keyed by parameter name, and the account run already declares
path.id, so the domains need a run of their own.

With --mailboxes it writes the third: the same administrators, each declaring
its own mailbox, sent on the /api/v1/admin/mailboxes/{id} operations and the
mailbox list.

With --messages it writes the fourth: the two accounts again, sent the other's
messages, drafts, scheduled sends, invitations and attachments (#1163). Their
ids are a message's, not an account's: the victim tenant makes each cell's
resource itself, by the chain the description's links lead from (sendMessage,
saveDraft, updateDraft, getRawMessage). ownership_hooks.py sends that chain,
and the attacker's request, from the sender's own address, schedules the
victim's scheduled send for tomorrow and puts an invitation in its sent
message, so the run provides future_time and calendar_invitation. No link
leads to an attachment, so before the run each tenant imports a message with
one into its own account through the API and declares its id as path.id,
which only getAttachment, the one row without a chain, reads. Its tenants'
principals file is the account run's.

Usage: ownership.py [--domains|--mailboxes|--messages] <openapi.yaml> <vertrag.yml> <tenants.json> <out.yml> <report.xml>
"""

import base64
import json
import os
import sys
import time
import urllib.request

import yaml
from authorization import LOGIN, sign_in

# What an account holds on its own mailbox: an app password reads, writes and
# sends, and a signed-in session also manages the account.
MAIL_ROLES = ["mail:read", "mail:write", "mail:send"]
SESSION_ROLES = MAIL_ROLES + ["mailbox:manage"]
# The operations each run sends: those that take the declared id, and the list
# beside them, which must not show the other tenant's rows. A transaction is
# named "<path> > <summary> > <status>", so the list is its path and " > ".
ACCOUNT_OPERATIONS = r"^/api/v1/accounts(/\{id\}| > )"
# The domain run also sends the DKIM routes, whose id is the domain's, and the
# admin resources a create links to (aliases, filters, pipelines,
# certificates), made again for each cell by the victim tenant's administrator.
# The queue, grants and bans take ids no link makes and that are not a
# domain's, so the declared id would name nothing there; the mailboxes have a
# run of their own (MAILBOX_OPERATIONS).
DOMAIN_OPERATIONS = (
    r"^/api/v1/admin/(domains(/\{id\}| > )|dkim/\{id\}"
    r"|(aliases|custom-filters|pipelines|certificates)(/\{id\}| > ))"
)
# The mailbox run: the admin mailbox routes and their list, with each tenant's
# administrator's own mailbox as the declared id.
MAILBOX_OPERATIONS = r"^/api/v1/admin/mailboxes(/\{id\}| > )"
# The message run: the operations on one message, draft, scheduled send or
# attachment, and the creates their links start from (sendMessage, saveDraft),
# which are not rows themselves (no path id) but must be sent for a chain to
# be. Not the held-message release, the seal or the scheduled list: #1163 names
# the twelve rows here.
MESSAGE_OPERATIONS = (
    r"^/api/v1/(messages/(send|draft) > "
    r"|messages/\{id\}(/(raw|forward|attachments|calendar-reply))? > "
    r"|messages/draft/\{id\}(/send)? > "
    r"|messages/scheduled/\{id\} > "
    r"|attachments/\{id\} > )"
)
# Someone else's account, or an object in it, is answered as if it did not
# exist: 404, never 403 (docs/api-conventions.md, rows 11 and 12, decided on
# #672 on 2026-11-01). The message run holds its cells to that.
NOT_YOURS = [404]
# What the admin role holds on its own domain (internal/bootstrap).
DOMAIN_ROLES = [
    "domains:read",
    "domains:write",
    "domains:delete",
    "accounts:read",
    "accounts:write",
    "accounts:delete",
    "pipelines:read",
    "pipelines:write",
    "pipelines:delete",
]
# The fixture's disposable password (tests/contract/fixtures, domainTenantPassword).
DOMAIN_TENANT_PASSWORD = "contract-domain-admin123!@"


def basic(principal):
    token = base64.b64encode(
        f"{principal['address']}:{principal['secret']}".encode()
    ).decode()
    return {"header": "Authorization: Basic " + token}


def run_config(spec_path, config_path, report_path):
    """vertrag.yml as an ownership run uses it: without the surface gate, with
    its own JUnit report."""
    with open(config_path) as source:
        config = yaml.safe_load(source)
    here = os.path.dirname(os.path.abspath(config_path))
    config["spec"] = os.path.abspath(spec_path)
    config["hookfiles"] = os.path.join(here, config["hookfiles"])
    config.pop("surface", None)
    config["reporter"] = ["cli", "junit"]
    config["output"] = ["", os.path.abspath(report_path)]
    return config


def write_private(config, out_path):
    # The generated configuration holds credentials. Restrict existing files
    # too, and never copy it into the report artifact.
    descriptor = os.open(out_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as out:
        os.fchmod(out.fileno(), 0o600)
        out.write("# Written by tests/contract/ownership.py; it holds credentials.\n")
        yaml.safe_dump(config, out, sort_keys=False, allow_unicode=True)


def domains(spec_path, config_path, tenants_path, out_path, report_path):
    return administrators(
        spec_path,
        config_path,
        tenants_path,
        out_path,
        report_path,
        DOMAIN_OPERATIONS,
        "domain_id",
    )


def mailboxes(spec_path, config_path, tenants_path, out_path, report_path):
    return administrators(
        spec_path,
        config_path,
        tenants_path,
        out_path,
        report_path,
        MAILBOX_OPERATIONS,
        "mailbox_id",
    )


def administrators(
    spec_path, config_path, tenants_path, out_path, report_path, operations, id_key
):
    """A run of the domain administrators: operations selected, each tenant
    declaring the fixture's id_key as path.id."""
    with open(tenants_path) as source:
        fixture = json.load(source)
    config = run_config(spec_path, config_path, report_path)
    if id_key == "domain_id":
        config["hookfiles"] = [
            config["hookfiles"],
            os.path.join(
                os.path.dirname(os.path.abspath(__file__)), "domain_ownership_hooks.py"
            ),
        ]
    config["only-matching"] = [operations]
    config["principals"] = [
        {
            "name": "domain-admin-" + tenant,
            "auth": sign_in(
                {
                    "address": fixture[tenant]["address"],
                    "password": DOMAIN_TENANT_PASSWORD,
                }
            ),
            "roles": DOMAIN_ROLES,
        }
        for tenant in ("a", "b")
    ]
    config["tenants"] = {
        tenant: {
            "principals": ["domain-admin-" + tenant],
            "ids": {"path.id": str(fixture[tenant][id_key])},
        }
        for tenant in ("a", "b")
    }
    config["ownership"] = {"refuse": [403, 404]}
    write_private(config, out_path)
    return 0


def account_principals(fixture):
    return [
        {
            "name": "ordinary",
            "auth": sign_in(fixture["ordinary"]),
            "roles": SESSION_ROLES,
        },
        {
            "name": "app-password",
            "auth": basic(fixture["app_password"]),
            "roles": MAIL_ROLES,
        },
        {"name": "other", "auth": sign_in(fixture["other"]), "roles": SESSION_ROLES},
        {
            "name": "other-app-password",
            "auth": basic(fixture["other_app_password"]),
            "roles": MAIL_ROLES,
        },
    ]


# Each account tenant's principals: the signed-in owner first, so the owner is
# who a chain is sent as when it may call every operation of it.
ACCOUNT_TENANTS = {
    "a": ["ordinary", "app-password"],
    "b": ["other", "other-app-password"],
}


def main(spec_path, config_path, principals_path, out_path, report_path):
    with open(principals_path) as source:
        fixture = json.load(source)
    config = run_config(spec_path, config_path, report_path)
    config["only-matching"] = [ACCOUNT_OPERATIONS]
    config["principals"] = account_principals(fixture)
    config["tenants"] = {
        tenant: {
            "principals": principals,
            "ids": {"path.id": str(fixture[principals[0]]["id"])},
        }
        for tenant, principals in ACCOUNT_TENANTS.items()
    }
    config["ownership"] = {"refuse": [403, 404]}
    write_private(config, out_path)
    return 0


# What the message run's hooks make true (ownership_hooks.py): the victim's
# scheduled send is for tomorrow, and its sent message carries an invitation,
# so the links to cancelScheduledMessage and respondToCalendar have a resource.
MESSAGE_STATES = ["future_time", "calendar_invitation"]


def attachment_message(stamp):
    """A message with one attachment, as an account already holding it imports it."""
    return (
        f"Message-ID: <contract-ownership-{stamp}@example.test>\r\n"
        "From: Original <old@example.test>\r\n"
        "Subject: ownership attachment\r\n"
        "MIME-Version: 1.0\r\n"
        'Content-Type: multipart/mixed; boundary="part"\r\n\r\n'
        "--part\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"
        "See the attachment.\r\n"
        "--part\r\nContent-Type: text/plain; name=notes.txt\r\n"
        "Content-Disposition: attachment; filename=notes.txt\r\n"
        "Content-Transfer-Encoding: base64\r\n\r\n"
        + base64.b64encode(b"ownership notes\n").decode()
        + "\r\n--part--\r\n"
    ).encode()


def call(endpoint, method, path, body=None, token=None):
    request = urllib.request.Request(
        endpoint.rstrip("/") + path,
        data=None if body is None else json.dumps(body).encode(),
        method=method,
        headers={"Content-Type": "application/json"},
    )
    if token:
        request.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(request, timeout=20) as response:
        return json.load(response)


def held_attachment(endpoint, principal):
    """The id of an attachment the account holds: signed in as it, a message
    with one is imported into its own account (importMessage, which every
    account may do on its own mailbox) and its attachment read back. No
    operation links to getAttachment, so its id is declared."""
    session = call(
        endpoint,
        "POST",
        LOGIN,
        {
            "email": principal["address"],
            "password": principal["password"],
            "client": "native",
        },
    )
    token = session["data"]["access_token"]
    imported = call(
        endpoint,
        "POST",
        "/api/v1/accounts/{}/messages/import".format(principal["id"]),
        {
            "raw_message": base64.b64encode(
                attachment_message(time.time_ns())
            ).decode(),
            "folder": "INBOX",
            "internal_date": "2026-01-02T03:04:05Z",
        },
        token,
    )
    listed = call(
        endpoint,
        "GET",
        "/api/v1/messages/{}/attachments".format(imported["data"]["id"]),
        token=token,
    )
    return str(listed["data"][0]["id"])


def held_attachments(endpoint, fixture):
    """Each account tenant's attachment as its declared path.id, or none, with
    why, when one cannot be made: its getAttachment cells are then unsent."""
    try:
        return {
            tenant: {"path.id": held_attachment(endpoint, fixture[principals[0]])}
            for tenant, principals in ACCOUNT_TENANTS.items()
        }
    except (OSError, ValueError, KeyError, IndexError, TypeError) as error:
        print(
            f"ownership.py: no attachment declared for the message run: {error}",
            file=sys.stderr,
        )
        return {}


def messages(spec_path, config_path, principals_path, out_path, report_path):
    """The message run: the account tenants, sent the message operations, with
    ownership_hooks.py loaded after the run's hooks. Every row but
    getAttachment is linked from what the victim makes, so a tenant's only
    declared id is its own attachment, which no row with a chain reads."""
    with open(principals_path) as source:
        fixture = json.load(source)
    config = run_config(spec_path, config_path, report_path)
    here = os.path.dirname(os.path.abspath(__file__))
    config["hookfiles"] = [
        config["hookfiles"],
        os.path.join(here, "ownership_hooks.py"),
    ]
    config["only-matching"] = [MESSAGE_OPERATIONS]
    config["provides"] = {
        **(config.get("provides") or {}),
        "state": list(MESSAGE_STATES),
    }
    config["principals"] = account_principals(fixture)
    ids = held_attachments(config["endpoint"], fixture)
    config["tenants"] = {
        tenant: {
            "principals": principals,
            **({"ids": ids[tenant]} if tenant in ids else {}),
        }
        for tenant, principals in ACCOUNT_TENANTS.items()
    }
    config["ownership"] = {"refuse": NOT_YOURS}
    write_private(config, out_path)
    return 0


if __name__ == "__main__":
    arguments = sys.argv[1:]
    run = main
    if arguments[:1] == ["--domains"]:
        run, arguments = domains, arguments[1:]
    elif arguments[:1] == ["--mailboxes"]:
        run, arguments = mailboxes, arguments[1:]
    elif arguments[:1] == ["--messages"]:
        run, arguments = messages, arguments[1:]
    if len(arguments) != 5:
        sys.exit(
            "usage: ownership.py [--domains|--mailboxes|--messages] <spec> <config> <tenants> <out.yml> <report.xml>"
        )
    sys.exit(run(*arguments))
