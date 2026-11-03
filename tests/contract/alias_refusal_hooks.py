"""Give the routing refusal a temporary inactive alias on the hosted domain."""

import json
import os
import urllib.parse
import urllib.request
import uuid

import vertrag_hooks as hooks


def register_alias_refusal_hooks(admin_token):
    pending = {}

    def call(origin, method, path, body=None):
        request = urllib.request.Request(
            origin + path,
            data=None if body is None else json.dumps(body).encode(),
            headers={
                "Authorization": "Bearer " + admin_token(origin),
                "Content-Type": "application/json",
            },
            method=method,
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.load(response) if response.status != 204 else None

    @hooks.before("checkMailboxAddress")
    def prepare_alias_refusal(transaction):
        path, _, query = transaction.get("fullPath", "").partition("?")
        if (
            path != "/api/mailboxes"
            or urllib.parse.parse_qsl(query, keep_blank_values=True)
            != [("address", "reserved@example.com")]
            or transaction.get("request", {}).get("method") != "GET"
            or transaction.get("expected", {}).get("statusCode") != "550"
        ):
            return
        origin = os.environ.get("CONTRACT_ENDPOINT", "http://10.99.0.20:8080").rstrip("/")
        address = f"contract-alias-refused-{uuid.uuid4().hex}@restmail.test"
        alias = call(
            origin,
            "POST",
            "/api/v1/admin/aliases",
            {
                "source_address": address,
                "destination_address": "admin@restmail.test",
            },
        )["data"]
        target = f"/api/v1/admin/aliases/{alias['id']}"
        try:
            updated = call(origin, "PATCH", target, {"active": False})["data"]
            if updated.get("active") is not False:
                raise RuntimeError("the refusal fixture alias is not inactive")
        except Exception:
            call(origin, "DELETE", target)
            raise
        transaction["fullPath"] = path + "?" + urllib.parse.urlencode({"address": address})
        pending[transaction["fullPath"]] = (origin, target)

    @hooks.after("checkMailboxAddress")
    def remove_alias_refusal(transaction):
        fixture = pending.get(transaction.get("fullPath"))
        if fixture is not None:
            call(fixture[0], "DELETE", fixture[1])
            del pending[transaction["fullPath"]]

    return prepare_alias_refusal, remove_alias_refusal
