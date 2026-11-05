"""Create ownership resources on the victim administrator's actual domain.

Shared example hooks use restmail.test or the last-created domain in their
stash. Those domains do not belong to both scoped administrators. Keep each
setup credential and the attacker's request unchanged; links still carry the
real created resource's ID. Global-only setup remains visibly unsent.
"""

import json
import os
import time
from pathlib import Path

import vertrag_hooks as hooks
from ownership_hooks import credential_address, in_an_ownership_run


def domain_tenant_for_setup(transaction):
    if not in_an_ownership_run() or " > 201 > " not in transaction.get("name", ""):
        return None
    header = transaction.get("request", {}).get("headers", {}).get("Authorization", "")
    if not header.startswith("Bearer "):
        return None
    address = credential_address(transaction)
    if not address or address.partition("@")[0] != "contract-domain-admin":
        return None
    path = os.environ.get(
        "CONTRACT_DOMAIN_TENANTS", ".cache/contract/domain-tenants.json"
    )
    with Path(path).open() as source:
        tenants = json.load(source)
    return next(
        (tenant for tenant in tenants.values() if tenant["address"] == address), None
    )


@hooks.before("createPipeline")
@hooks.before("createCustomFilter")
def create_on_the_tenants_domain(transaction):
    tenant = domain_tenant_for_setup(transaction)
    if tenant is None:
        return
    body = hooks.get_json(transaction)
    if (
        not isinstance(body, dict)
        or type(body.get("domain_id")) is not int
        or body["domain_id"] < 1
    ):
        return
    body["domain_id"] = tenant["domain_id"]
    if isinstance(body.get("name"), str) and body["name"]:
        body["name"] = f"contract-ownership-{time.time_ns()}"
    hooks.set_json(transaction, body)


@hooks.before("createAlias")
def create_alias_on_the_tenants_domain(transaction):
    tenant = domain_tenant_for_setup(transaction)
    if tenant is None:
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or not isinstance(body.get("source_address"), str):
        return
    local, separator, domain = body["source_address"].rpartition("@")
    if not local or not separator or "." not in domain:
        return
    domain = tenant["address"].partition("@")[2]
    body["source_address"] = f"contract-ownership-{time.time_ns()}@{domain}"
    hooks.set_json(transaction, body)
