"""Provide a live account and an IANA zone for the compiler's format placeholder.

The account's own time zone route needs the same two things, so it registers here
under its own path and operations.
"""

import json
import urllib.request

import vertrag_hooks as hooks


def register_quiet_hours_hooks(
    admin_token, route="quiet-hours", operations=("getQuietHours", "setQuietHours")
):
    def prepare_quiet_hours(transaction):
        if " as " in transaction.get("name", "") or not str(
            transaction.get("expected", {}).get("statusCode", "")
        ).startswith("2"):
            return
        path = transaction.get("fullPath", "")
        if path != f"/api/v1/accounts/1/{route}":
            return
        headers = transaction.get("request", {}).get("headers") or {}
        if not any(
            key.lower() in ("authorization", "cookie", "x-api-key") and value
            for key, value in headers.items()
        ):
            return
        origin = (
            f"{transaction['protocol']}//{transaction['host']}:{transaction['port']}"
        )
        request = urllib.request.Request(
            origin + "/api/v1/accounts",
            headers={"Authorization": "Bearer " + admin_token(origin)},
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            accounts = json.load(response).get("data") or []
        account_id = accounts[0].get("id") if accounts else None
        if type(account_id) is not int or account_id < 1:
            raise RuntimeError(f"{route} fixture has no live account")
        transaction["fullPath"] = f"/api/v1/accounts/{account_id}/{route}"
        body = transaction.get("request", {}).get("body")
        if transaction.get("request", {}).get("method") != "PUT" or not body:
            return
        try:
            setting = json.loads(body)
        except (TypeError, ValueError):
            return
        if isinstance(setting, dict) and setting.get("time_zone") == "x":
            setting["time_zone"] = "UTC"
            transaction["request"]["body"] = json.dumps(setting)

    for operation in operations:
        hooks.before(operation)(prepare_quiet_hours)
    return prepare_quiet_hours
