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

With --domains it writes the second run instead: the administrators
`chore test:contract-domain-tenants` makes, one of restmail.test (tenant a) and
one of contract-other.test (tenant b), each holding the admin role on its own
domain, sent the other's domain id on the /api/v1/admin/domains/{id} operations.
Tenant ids are keyed by parameter name, and the account run already declares
path.id, so the domains need a run of their own.

Usage: ownership.py [--domains] <openapi.yaml> <vertrag.yml> <tenants.json> <out.yml> <report.xml>
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
DOMAIN_OPERATIONS = r"^/api/v1/admin/domains/\{id\}"
DOMAIN_ROLES = ["domains:read", "domains:write", "domains:delete"]
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
    with open(tenants_path) as source:
        fixture = json.load(source)
    config = run_config(spec_path, config_path, report_path)
    config["only-matching"] = [DOMAIN_OPERATIONS]
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
            "ids": {"path.id": str(fixture[tenant]["domain_id"])},
        }
        for tenant in ("a", "b")
    }
    config["ownership"] = {"refuse": [403, 404]}
    write_private(config, out_path)
    return 0


def main(spec_path, config_path, principals_path, out_path, report_path):
    with open(principals_path) as source:
        fixture = json.load(source)
    config = run_config(spec_path, config_path, report_path)
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
    write_private(config, out_path)
    return 0


if __name__ == "__main__":
    arguments = sys.argv[1:]
    run = main
    if arguments[:1] == ["--domains"]:
        run, arguments = domains, arguments[1:]
    if len(arguments) != 5:
        sys.exit(
            "usage: ownership.py [--domains] <spec> <config> <tenants> <out.yml> <report.xml>"
        )
    sys.exit(run(*arguments))
