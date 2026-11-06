#!/usr/bin/env python3
"""Writes the configuration of vertrag's authorization phase (#376).

The phase sends every operation's documented request once as every principal
and checks the answer against the operation's `security`: a principal holding
one requirement's roles gets the documented success, one with no credential
401, one without the roles 403, or the 404 that hides someone else's object.

The principals are the ones tests/e2e/contract_principals_test.go makes and
writes to .cache/contract-principals.json. The app password is minted fresh by
each E2E run and vertrag reads no environment in its configuration, so the
configuration is written here, after that suite, rather than kept in
vertrag.yml: vertrag.yml with `principals` added and the surface gate left out
(the required run already judged examples; this diagnostic run repeats them).

    principal          credential                           roles
    none               nothing                              -
    admin              signs in with a second factor        every role the spec names: superadmin at *
                       (bearer)
    ordinary           signs in (bearer)                    none: no grants
    app-password       Basic, the ordinary account's        none: read, write and send on its own mailbox

These are provisional role expectations, not a complete model of resource
scope: ordinary and app-password mail capabilities cover their own mailbox,
and the admin cannot necessarily reach another mailbox without a grant. A
cell whose ids the description's links supply is sent on a resource made for
it as its owner (vertrag#81); an operation no link leads to still sends its
example ids.
Keep this run report-only until these fixture and scope mismatches are resolved.
The admin principal is the contract's two-factor administrator (mfa_admin.py),
a superadmin as the fixture admin is, signed in with an unused second factor.
Creating credentials, linking accounts and changing grants take a session that
proved one recently, which the fixture admin's never has. The examples phase
swaps the fixture admin's session for this administrator's in a hook, but this
phase ends a request whose credential a hook changes unsent ("a hook changed
the credential"), so its admin is that administrator from the start (#1162).

The account with no password cannot sign in, so it has no separate credential
from `none`; there is no agent principal until the model has agents (#341).

The same flat roles make two operations report a bypass that is not one, so
the matrix does not send them (OWN_ACCOUNT). Saving a draft without a from
and listing scheduled sends name no mailbox: both act on the caller's own
account, where ordinary and app-password hold mail:write and mail:send, so
their success is the right answer, and roles listed once for every operation
cannot say so.


Usage: authorization.py <openapi.yaml> <vertrag.yml> <principals.json> <out.yml> <report.xml>
"""

import base64
import json
import os
import sys

import mfa_admin
import yaml
from reports import spec_location

METHODS = ("get", "put", "post", "delete", "patch", "head", "options", "trace")
LOGIN = "/api/v1/auth/login"
BEARER = "Authorization: Bearer {$response.body#/data/access_token}"
# A refusal says only what the caller may already know (#672): someone else's
# account or message answers the same 404 as one that does not exist. Vertrag
# accepts this beside the status a cell requires, on every cell, so
# authorization_refusals.py holds it to the routes that decision covers.
HIDING_REFUSAL = [404]
# What this phase can set up that the examples phase does not (#1162). A
# recovery names its account in the body, so hooks.py makes a fresh account,
# enrolled in TOTP, for each principal's request (recover_an_account_of_its_own):
# the success is sent as every principal without touching the run's own
# accounts. The states whose target is the caller's own account
# (known_password, enrolled_authenticator, pending_enrollment) are not provided:
# their success ends or changes the principal's own sessions, and a hook may not
# point the request at another account's credential.
# The owner endpoints need a key in the owner registry: chore contract registers
# a throwaway one (owner_key.py, chore test:contract-owner-key) for this phase
# only, and hooks.py signs each success with it. A token is minted and redeemed
# only for an account made for that request, never one of the run's own. The
# refusals that require the key (not_status_key, not_owner, no_account) get a
# real fresh challenge too, signed by a key listed nowhere or, for an address no
# account has, by the run's key, so each reaches the check it documents.
PROVIDES = {"state": ["owner_key", "recoverable_account"]}
# Operations whose request names no mailbox: they act on the caller's own
# account, where every signed-in principal holds the capability they declare.
OWN_ACCOUNT = [
    {
        "name": "/api/v1/messages/draft > Save a new draft > 201 > application/json",
        "reason": "Without a from, the draft goes to the caller's own mailbox, where "
        "ordinary and app-password hold mail:write; a credential without it is "
        "refused (#1278), which the flat roles here cannot express.",
    },
    {
        "name": "/api/v1/messages/scheduled > List the messages waiting to be sent "
        "> 200 > application/json",
        "reason": "Lists the caller's own account's waiting sends, leaving out those "
        "it may not send as (#743); it never answers 403, and ordinary and "
        "app-password hold mail:send on their own mailbox.",
    },
]


def roles(spec_path):
    """Every role any operation's security requirement names."""
    with open(spec_path) as source:
        spec = yaml.safe_load(source)
    top = spec.get("security") or []
    found = set()
    for item in spec["paths"].values():
        for method in METHODS:
            if method in item:
                for requirement in item[method].get("security", top) or []:
                    for names in requirement.values():
                        found.update(names)
    return sorted(found)


def sign_in(principal, factor=None):
    """Vertrag signs each principal in once, before any cell is sent; a second
    factor, a one-time code, is spent by that one sign-in."""
    factor = factor or {}
    return {
        # Vertrag 0.31 refuses a login body field that is in neither list: the
        # password and any second factor are redacted, the address and client
        # are shown (vertrag#87).
        "login": {
            "path": LOGIN,
            "body": {
                "email": principal["address"],
                "password": principal["password"],
                "client": "native",
                **factor,
            },
            "secrets": ["password", *factor],
            "public": ["email", "client"],
        },
        "header": BEARER,
    }


def main(spec_path, config_path, principals_path, out_path, report_path, run_spec=""):
    with open(config_path) as source:
        config = yaml.safe_load(source)
    with open(principals_path) as source:
        fixture = json.load(source)
    app = fixture["app_password"]
    basic = base64.b64encode(f"{app['address']}:{app['secret']}".encode()).decode()

    here = os.path.dirname(os.path.abspath(config_path))
    config["spec"] = spec_location(run_spec or spec_path, here)
    config["hookfiles"] = os.path.join(here, config["hookfiles"])
    config.pop("surface", None)
    config["skip"] = list(config.get("skip") or []) + OWN_ACCOUNT
    config["authorization"] = {"refuse": HIDING_REFUSAL}
    provides = config.get("provides") or {}
    config["provides"] = {
        kind: sorted(set(provides.get(kind) or []) | set(values))
        for kind, values in {**provides, **PROVIDES}.items()
    }
    # The examples phase's unproven list is not this run's: it provides more.
    config["unproven"] = ".cache/contract/unproven-authorization.json"
    config["reporter"] = ["cli", "junit"]
    config["output"] = ["", os.path.abspath(report_path)]
    config["principals"] = [
        {"name": "none"},
        {
            "name": "admin",
            "auth": sign_in(
                {"address": mfa_admin.ADDRESS, "password": mfa_admin.PASSWORD},
                mfa_admin.factor(),
            ),
            "roles": roles(spec_path),
        },
        {"name": "ordinary", "auth": sign_in(fixture["ordinary"]), "roles": []},
        {
            "name": "app-password",
            "auth": {"header": "Authorization: Basic " + basic},
            "roles": [],
        },
    ]
    # This generated configuration holds the freshly minted app password.
    # Restrict existing files too, and never copy it into the report artifact.
    descriptor = os.open(out_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as out:
        os.fchmod(out.fileno(), 0o600)
        out.write(
            "# Written by tests/contract/authorization.py; it holds a credential.\n"
        )
        yaml.safe_dump(config, out, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 6:
        sys.exit(
            "usage: authorization.py <spec> <config> <principals> <out.yml> <report.xml>"
        )
    sys.exit(main(*sys.argv[1:], run_spec=os.environ.get("CONTRACT_RUN_SPEC", "")))
