#!/usr/bin/env python3
"""Writes the configuration of vertrag's authorization phase (#376).

The phase sends every operation's documented request once as every principal
and checks the answer against the operation's `security`: a principal holding
one requirement's roles gets the documented success, one with no credential
401, one without the roles 403.

The principals are the ones tests/e2e/contract_principals_test.go makes and
writes to .cache/contract-principals.json. The app password is minted fresh by
each E2E run and vertrag reads no environment in its configuration, so the
configuration is written here, after that suite, rather than kept in
vertrag.yml: vertrag.yml with `principals` added and the surface gate left out
(the required run already judged examples; this diagnostic run repeats them).

    principal          credential                           roles
    none               nothing                              -
    admin              signs in (bearer)                    every role the spec names: superadmin at *
    ordinary           signs in (bearer)                    none: no grants
    app-password       Basic, the ordinary account's        none: read, write and send on its own mailbox

These are provisional role expectations, not a complete model of resource
scope: ordinary and app-password mail capabilities cover their own mailbox,
and the admin cannot necessarily reach another mailbox without a grant. The
phase uses literal example IDs rather than lifecycle links (vertrag#81).
Keep this run report-only until these fixture and scope mismatches are resolved.
The account with no password cannot sign in, so it has no separate credential
from `none`; there is no agent principal until the model has agents (#341).

Usage: authorization.py <openapi.yaml> <vertrag.yml> <principals.json> <out.yml> <report.xml>
"""
import base64
import json
import os
import sys

import yaml

METHODS = ("get", "put", "post", "delete", "patch", "head", "options", "trace")
LOGIN = "/api/v1/auth/login"
BEARER = "Authorization: Bearer {$response.body#/data/access_token}"


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


def sign_in(principal):
    return {
        "login": {"path": LOGIN, "body": {
            "email": principal["address"], "password": principal["password"], "client": "native"}},
        "header": BEARER,
    }


def main(spec_path, config_path, principals_path, out_path, report_path):
    with open(config_path) as source:
        config = yaml.safe_load(source)
    with open(principals_path) as source:
        fixture = json.load(source)
    app = fixture["app_password"]
    basic = base64.b64encode(("%s:%s" % (app["address"], app["secret"])).encode()).decode()

    here = os.path.dirname(os.path.abspath(config_path))
    config["spec"] = os.path.abspath(spec_path)
    config["hookfiles"] = os.path.join(here, config["hookfiles"])
    config.pop("surface", None)
    config["reporter"] = ["cli", "junit"]
    config["output"] = ["", os.path.abspath(report_path)]
    config["principals"] = [
        {"name": "none"},
        {"name": "admin", "auth": sign_in(fixture["admin"]), "roles": roles(spec_path)},
        {"name": "ordinary", "auth": sign_in(fixture["ordinary"]), "roles": []},
        {"name": "app-password", "auth": {"header": "Authorization: Basic " + basic}, "roles": []},
    ]
    # This generated configuration holds the freshly minted app password.
    # Restrict existing files too, and never copy it into the report artifact.
    descriptor = os.open(out_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as out:
        os.fchmod(out.fileno(), 0o600)
        out.write("# Written by tests/contract/authorization.py; it holds a credential.\n")
        yaml.safe_dump(config, out, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 6:
        sys.exit("usage: authorization.py <spec> <config> <principals> <out.yml> <report.xml>")
    sys.exit(main(*sys.argv[1:]))
