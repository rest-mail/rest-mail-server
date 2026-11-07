"""Give the absent-setting examples an accessible account with no stored settings."""

import json
import re

import vertrag_hooks as hooks


def register_absent_settings_hooks(shared):
    @hooks.before("getSieveScript")
    @hooks.before("deleteSieveScript")
    @hooks.before("disableVacation")
    def prepare_absent_account_setting(transaction):
        path, separator, query = transaction.get("fullPath", "").partition("?")
        match = re.fullmatch(r"/api/v1/accounts/[1-9][0-9]*/(sieve|vacation)", path)
        expected = transaction.get("expected", {})
        method = transaction.get("request", {}).get("method")
        if not match or str(expected.get("statusCode")) != "404":
            return
        setting = match.group(1)
        if method not in (("GET", "DELETE") if setting == "sieve" else ("DELETE",)):
            return
        try:
            body = json.loads(expected.get("body", ""))
        except (TypeError, ValueError):
            return
        message = "Sieve script not found" if setting == "sieve" else "Vacation reply not found"
        if body != {"error": {"code": "not_found", "message": message}}:
            return
        if not shared["with_the_fixture_admins_session"](transaction):
            return
        origin = shared["origin_of"](transaction)
        fixtures = hooks.stash.setdefault("absent_setting_accounts", {})
        if origin not in fixtures:
            account_id = shared["an_account_without_credentials"](origin)
            if type(account_id) is not int or account_id <= 0:
                raise RuntimeError("absent-setting setup returned no positive account ID")
            fixtures[origin] = account_id
        transaction["fullPath"] = f"/api/v1/accounts/{fixtures[origin]}/{setting}" + (separator + query if separator else "")

    stored_first = {
        "sieve": {"script": "keep;", "active": False},
        "vacation": {"enabled": True, "subject": "Away", "body": "Back soon."},
    }

    @hooks.before("getSieveScript")
    @hooks.before("deleteSieveScript")
    @hooks.before("disableVacation")
    def a_stored_setting_to_act_on(transaction):
        path = transaction.get("fullPath", "").partition("?")[0]
        match = re.fullmatch(r"/api/v1/accounts/[1-9][0-9]*/(sieve|vacation)", path)
        if not match or str(transaction.get("expected", {}).get("statusCode")) not in ("200", "204"):
            return
        if not shared["with_the_fixture_admins_session"](transaction):
            return
        shared["fixture_call"](transaction, "PUT", path, stored_first[match.group(1)], expect=(200, 201, 404))

    return prepare_absent_account_setting
