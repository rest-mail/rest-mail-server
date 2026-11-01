"""Keep generated account probes off the account the run signs in as."""

import json
import urllib.request

import vertrag_hooks as hooks

# Each of these acts on the account its path names. Sent at the run's own
# account they test nothing the description promises: deleteMailbox refuses an
# account holding grants to a session without a second factor (403
# two_factor_required), endMailboxSessions would end the run's own sessions so
# everything after it answers 401, and inviteAccount refuses an account that
# already has credentials (409).
OWN_ACCOUNT_OPERATIONS = ("deleteMailbox", "endMailboxSessions", "inviteAccount")


def register_own_account_hooks(admin_token):
    def own_account_id(origin):
        ids = hooks.stash.setdefault("own_account_ids", {})
        if origin not in ids:
            # The session's own account is listed first (listAccounts).
            request = urllib.request.Request(
                origin + "/api/v1/accounts",
                headers={"Authorization": "Bearer " + admin_token(origin)},
            )
            with urllib.request.urlopen(request, timeout=10) as response:
                accounts = json.load(response).get("data") or []
            account_id = accounts[0].get("id") if accounts else None
            if type(account_id) is not int or account_id <= 0:
                raise RuntimeError("the fixture admin's account has no positive ID")
            ids[origin] = account_id
        return ids[origin]

    def skip_the_own_account(transaction):
        # A probe drawn from a success transaction may name any id the schema
        # permits, the run's own included (fuzz run 40856371771 sent
        # DELETE /api/v1/admin/mailboxes/4 as admin@restmail.test). That
        # request is taken out of the run rather than pointed at another
        # account: vertrag judges and reports the id it drew, not the path a
        # hook rewrites. Documented refusals, malformed ids and other
        # principals (the authorization phase) are sent unchanged.
        if " as " in transaction.get("name", ""):
            return
        if not str(transaction.get("expected", {}).get("statusCode", "")).startswith(
            "2"
        ):
            return
        path = transaction.get("fullPath", "").partition("?")[0]
        parts = path.rstrip("/").split("/")
        if parts[-1] in ("sessions", "invitation"):
            parts.pop()
        account_id = parts[-1]
        if not account_id.isdecimal() or int(account_id) < 1:
            return
        origin = "{}//{}:{}".format(
            transaction["protocol"],
            transaction["host"],
            transaction["port"],
        )
        if int(account_id) == own_account_id(origin):
            transaction["skip"] = True

    for operation in OWN_ACCOUNT_OPERATIONS:
        hooks.before(operation)(skip_the_own_account)
    return skip_the_own_account
