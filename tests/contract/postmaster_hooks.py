"""Give postmaster refusals isolated domains, discarded by the topology reset."""

import json
import urllib.request
import uuid

import vertrag_hooks as hooks


def register_postmaster_hooks(mfa_admin_token):
    def prepare_postmaster_refusal(transaction):
        expected = transaction.get("expected", {})
        body = expected.get("body", {})
        if isinstance(body, str):
            try:
                body = json.loads(body)
            except ValueError:
                return
        if (
            not isinstance(body, dict)
            or body.get("error", {}).get("code") != "postmaster_required"
        ):
            return
        if str(expected.get("statusCode")) != "409":
            return
        path = transaction.get("fullPath", "").partition("?")[0]
        parts = path.split("/")
        if len(parts) != 6 or parts[:4] != ["", "api", "v1", "admin"]:
            return
        resource = parts[4]
        if resource not in ("mailboxes", "aliases", "grants"):
            return
        origin = "{}//{}:{}".format(
            transaction["protocol"], transaction["host"], transaction["port"]
        )

        def call(method, target, payload=None):
            request = urllib.request.Request(
                origin + target,
                data=None if payload is None else json.dumps(payload).encode(),
                headers={
                    "Authorization": "Bearer " + mfa_admin_token(origin),
                    "Content-Type": "application/json",
                },
                method=method,
            )
            with urllib.request.urlopen(request, timeout=10) as response:
                return (
                    json.load(response).get("data") if response.status != 204 else None
                )

        domain = call(
            "POST",
            "/api/v1/admin/domains",
            {"name": f"contract-postmaster-{uuid.uuid4().hex}.test"},
        )
        mailboxes = call("GET", f"/api/v1/admin/mailboxes?domain_id={domain['id']}")
        account = next(
            mailbox
            for mailbox in mailboxes
            if mailbox["address"] == "postmaster@" + domain["name"]
        )
        identifier = account["id"]
        if resource == "aliases":
            alias = call(
                "POST",
                "/api/v1/admin/aliases",
                {
                    "source_address": account["address"],
                    "destination_address": "reports@external-postmaster.example",
                },
            )
            call("DELETE", f"/api/v1/admin/mailboxes/{account['id']}")
            identifier = alias["id"]
        elif resource == "grants":
            grants = call(
                "GET",
                f"/api/v1/admin/grants?holder_kind=account&holder_id={account['id']}",
            )
            identifier = next(
                grant["id"] for grant in grants if grant["role"] == "receiver"
            )
        transaction["fullPath"] = f"/api/v1/admin/{resource}/{identifier}"

    for operation in (
        "updateMailbox",
        "deleteMailbox",
        "updateAlias",
        "deleteAlias",
        "deleteGrant",
    ):
        hooks.before(operation)(prepare_postmaster_refusal)
    return prepare_postmaster_refusal
