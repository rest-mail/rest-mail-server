"""Create a real budget hold and verify its release, only in the own-data phase."""

import json
import urllib.error
import urllib.request

import vertrag_hooks as hooks


def call(origin, token, method, path, body=None, status=200):
    request = urllib.request.Request(
        origin + path,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
        method=method,
    )
    try:
        response = urllib.request.urlopen(request, timeout=10)
    except urllib.error.HTTPError as error:
        if error.code != status:
            raise
        with error:
            return json.load(error)
    with response:
        if response.status != status:
            raise RuntimeError(f"held release setup: {method} {path}: HTTP {response.status}, expected {status}")
        return json.load(response)["data"]


def register_held_release_hooks(shared):
    @hooks.before("releaseHeldMessage")
    def a_real_held_send(transaction):
        if not shared["asked_for_a_clean_start"]():
            return
        # The first shared hook has already reset and seeded. Only the success
        # gets a hold: invalid, missing, anonymous and password-only controls
        # retain their original path and credential.
        if str(transaction.get("expected", {}).get("statusCode")) != "200":
            return
        address = shared["session_address"](transaction)
        if address not in (shared["ADMIN"], shared["mfa_admin"].ADDRESS):
            return
        origin = shared["setup_origin"](transaction)
        token = shared["mfa_admin_token"](origin)
        password = shared["CREDENTIALED_PASSWORD"]
        made = shared["a_made_account"](origin, "contract-held-release", password)
        sender_token = shared["mfa_admin"]._sign_in(origin, made["address"], password)["access_token"]
        call(origin, token, "PUT", f"/api/v1/accounts/{made['id']}/send-limits",
             {"messages_per_hour": 1, "messages_per_day": 100,
              "recipients_per_hour": 100, "recipients_per_day": 100})
        body = {"from": made["address"], "to": ["held-proof@nowhere.invalid"],
                "subject": "Quarterly report", "body_text": "Figures attached."}
        call(origin, sender_token, "POST", "/api/v1/messages/send", body, status=201)
        held = call(origin, sender_token, "POST", "/api/v1/messages/send", body, status=202)
        if held.get("status") != "held" or held.get("account_id") != made["id"]:
            raise RuntimeError("the disposable sender did not produce its own held record")
        if held.get("last_error") != f"account:{made['id']}: messages_per_hour":
            raise RuntimeError("the message was not held by its own account budget")
        hooks.stash["held_release_proof"] = {"held": held, "sender": made}
        transaction["fullPath"] = f"/api/v1/messages/scheduled/{held['id']}/release"
        if address == shared["ADMIN"]:
            transaction["request"]["headers"]["Authorization"] = "Bearer " + token

    @hooks.after("releaseHeldMessage")
    def the_held_send_really_persisted(transaction):
        if not shared["asked_for_a_clean_start"]() or str(transaction.get("expected", {}).get("statusCode")) != "200":
            return
        made = hooks.stash.pop("held_release_proof", None)
        try:
            if made is None:
                raise RuntimeError("no real held record was prepared")
            if str(transaction.get("real", {}).get("statusCode")) != "200":
                raise RuntimeError("the release did not answer 200")
            released = hooks.get_json(transaction, "real")["data"]
            held = made["held"]
            if released.get("id") != held["id"] or released.get("status") != "sent":
                raise RuntimeError("the same held record did not transition to sent")
            message_id = released.get("sent_message_id")
            if not isinstance(message_id, int) or message_id <= 0:
                raise RuntimeError("the release names no sent message")
            origin = shared["setup_origin"](transaction)
            token = shared["mfa_admin_token"](origin)
            # A second release reads the committed row under lock. A missing
            # or still-held row cannot answer this precise refusal.
            again = call(origin, token, "POST", f"/api/v1/messages/scheduled/{held['id']}/release", status=409)
            if again.get("error", {}).get("code") != "not_held":
                raise RuntimeError("the persisted record is not a completed release")
            sent = call(origin, token, "GET", f"/api/v1/messages/{message_id}")
            if (sent.get("id"), sent.get("folder"), sent.get("sender"), sent.get("subject")) != (
                message_id, "Sent", made["sender"]["address"], held["subject"]
            ):
                raise RuntimeError("the released message has no matching persisted Sent copy")
            remaining = call(origin, token, "GET", f"/api/v1/accounts/{made['sender']['id']}/held-messages")
            if any(row["id"] == held["id"] for row in remaining):
                raise RuntimeError("the released record remains held")
        except (RuntimeError, KeyError, TypeError) as error:
            transaction["fail"] = "held release proof: " + str(error)

    return a_real_held_send, the_held_send_really_persisted
