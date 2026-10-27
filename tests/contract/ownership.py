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
the declared id is the account's: the domain and administration tenants come
with the domain-scoped admin fixture. Keep this run report-only until it has
run clean.

Usage: ownership.py <openapi.yaml> <vertrag.yml> <principals.json> <out.yml> <report.xml>
"""

import base64
import json
import os
import sys

import yaml
from authorization import sign_in

# What an account holds on its own mailbox: an app password reads, writes and
# sends, and a signed-in session also manages the account.
MAIL_ROLES = ["mail:read", "mail:write", "mail:send"]
SESSION_ROLES = MAIL_ROLES + ["mailbox:manage"]
ACCOUNT_OPERATIONS = r"^/api/v1/accounts/\{id\}"


def basic(principal):
    token = base64.b64encode(
        f"{principal['address']}:{principal['secret']}".encode()
    ).decode()
    return {"header": "Authorization: Basic " + token}


def main(spec_path, config_path, principals_path, out_path, report_path):
    with open(config_path) as source:
        config = yaml.safe_load(source)
    with open(principals_path) as source:
        fixture = json.load(source)

    here = os.path.dirname(os.path.abspath(config_path))
    config["spec"] = os.path.abspath(spec_path)
    config["hookfiles"] = os.path.join(here, config["hookfiles"])
    config.pop("surface", None)
    config["reporter"] = ["cli", "junit"]
    config["output"] = ["", os.path.abspath(report_path)]
    config["only-matching"] = [ACCOUNT_OPERATIONS]
    config["principals"] = [
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
    config["tenants"] = {
        "a": {
            "principals": ["ordinary", "app-password"],
            "ids": {"path.id": str(fixture["ordinary"]["id"])},
        },
        "b": {
            "principals": ["other", "other-app-password"],
            "ids": {"path.id": str(fixture["other"]["id"])},
        },
    }
    config["ownership"] = {"refuse": [403, 404]}
    # This generated configuration holds the freshly minted app passwords.
    # Restrict existing files too, and never copy it into the report artifact.
    descriptor = os.open(out_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as out:
        os.fchmod(out.fileno(), 0o600)
        out.write("# Written by tests/contract/ownership.py; it holds credentials.\n")
        yaml.safe_dump(config, out, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 6:
        sys.exit(
            "usage: ownership.py <spec> <config> <principals> <out.yml> <report.xml>"
        )
    sys.exit(main(*sys.argv[1:]))
