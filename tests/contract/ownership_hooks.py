"""Hooks the message ownership run loads after hooks.py (ownership.py --messages).

The documented examples send from the fixture administrator, and so would the
run: the victim tenant's chain (sendMessage, saveDraft, updateDraft) and the
attacker's own request (updateDraft, forwardMessage). An account may not send
as the administrator, so the chain would be refused before it made anything,
and the attacker would be refused for its sender (403) instead of for the
message (#1163). In an ownership run (run.sh sets CONTRACT_OWNERSHIP) a request
from any other account is sent from, and only to, that account's own address,
so what the victim makes stays its own and the attacker's request is valid but
for whose message it names.
"""

import base64
import json
import os
import time
from datetime import UTC, datetime, timedelta

import vertrag_hooks as hooks

# The fixture administrator the examples send from (tests/contract/hooks.py).
ADMIN = "admin@restmail.test"


def in_an_ownership_run():
    return os.environ.get("CONTRACT_OWNERSHIP") == "1"


def credential_address(transaction):
    """The address the request's credential is for: a session's, from its
    token's claims, or an app password's, from its Basic user. None for any
    other credential, or none."""
    header = transaction.get("request", {}).get("headers", {}).get("Authorization", "")
    scheme, _, value = header.partition(" ")
    try:
        if scheme == "Bearer":
            payload = value.split(".")[1]
            claims = json.loads(
                base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4))
            )
            address = claims.get("address") if isinstance(claims, dict) else None
        elif scheme == "Basic":
            address = base64.b64decode(value, validate=True).decode().partition(":")[0]
        else:
            return None
    except (IndexError, ValueError, UnicodeDecodeError):
        return None
    return address if isinstance(address, str) and "@" in address else None


@hooks.before("sendMessage")
@hooks.before("saveDraft")
@hooks.before("updateDraft")
@hooks.before("forwardMessage")
def send_as_the_sender_itself(transaction):
    address = own_sender(transaction)
    if address is None:
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or str(body.get("from", "")).lower() != ADMIN:
        return
    body["from"] = address
    body["to"] = [address]
    body.pop("cc", None)
    body.pop("bcc", None)
    hooks.set_json(transaction, body)


def own_sender(transaction):
    """The sender's own address in an ownership run, or None where the hooks
    leave the request as it is: outside one, or for the administrator."""
    if not in_an_ownership_run():
        return None
    address = credential_address(transaction)
    if address is None or address.lower() == ADMIN:
        return None
    return address


def run_sender(transaction):
    """The address the request's credential is for in an ownership run, the
    administrator's included, or None outside one."""
    if not in_an_ownership_run():
        return None
    return credential_address(transaction)


def a_refusal(transaction):
    """Whether the transaction is named for a response other than a success.
    An ownership cell is built from the success, so only examples are."""
    status = transaction.get("name", "").split(" > ")[2:3]
    return bool(status) and not status[0].startswith("2")


def an_hour_tomorrow(now):
    start = (now + timedelta(days=1)).replace(minute=0, second=0, microsecond=0)
    return start, start + timedelta(hours=1)


def rfc3339(moment):
    return moment.astimezone(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


@hooks.before("sendMessage")
def a_send_each_link_can_follow(transaction):
    # The victim's chain starts at a send. Its scheduled example (202) names a
    # time already past, and its 201 carries no invitation, so neither would
    # make what cancelScheduledMessage and respondToCalendar are sent for
    # (the run provides future_time and calendar_invitation). A scheduled send
    # is moved to tomorrow; any other send invites the sender to an hour
    # tomorrow, which its Sent copy keeps (#1163). The run provides both for
    # its examples too, which it sends as the administrator, so the
    # administrator's sends are made the same way: left as written, the
    # scheduled send was refused 422 and the reply found no invitation, 400
    # (#1942). Only a send documented to succeed is changed: the refusals
    # (scheduled_at_past, scheduled_at_too_far and the rest) keep what they
    # are refused for.
    address = run_sender(transaction)
    body = hooks.get_json(transaction)
    if address is None or not isinstance(body, dict) or a_refusal(transaction):
        return
    start, end = an_hour_tomorrow(datetime.now(UTC))
    if body.get("scheduled_at") is not None:
        body["scheduled_at"] = rfc3339(start)
    elif body.get("calendar_event") is None:
        domain = address.rpartition("@")[2]
        body["calendar_event"] = {
            "method": "REQUEST",
            "uid": f"contract-ownership-{time.time_ns()}@{domain}",
            "summary": "Ownership run",
            "dtstart": rfc3339(start),
            "dtend": rfc3339(end),
            "all_day": False,
            "organizer": {"address": address},
            "attendees": [
                {"address": address, "partstat": "NEEDS-ACTION", "rsvp": True}
            ],
            "sequence": 0,
        }
    else:
        return
    hooks.set_json(transaction, body)


@hooks.before("respondToCalendar")
def reply_as_the_sender_itself(transaction):
    # The reply is sent from the administrator in the example; an account
    # replies from its own address, so a refusal is for whose invitation it
    # names, not for its sender.
    address = own_sender(transaction)
    body = hooks.get_json(transaction)
    if address is None or not isinstance(body, dict):
        return
    if str(body.get("from", "")).lower() != ADMIN:
        return
    body["from"] = address
    hooks.set_json(transaction, body)


@hooks.after("sendMessage")
def keep_the_invitation(transaction):
    # What respondToCalendar's refusal below is sent on: the message the
    # sender's own invitation made, as only the success is filled by a link.
    address = run_sender(transaction)
    sent = hooks.get_json(transaction)
    if address is None or not isinstance(sent, dict) or sent.get("calendar_event") is None:
        return
    try:
        data = (hooks.get_json(transaction, "real") or {}).get("data") or {}
    except (ValueError, TypeError, AttributeError):
        return
    if isinstance(data.get("id"), int):
        invitations = hooks.stash.get("ownership_invitations", {})
        invitations[address.lower()] = data["id"]
        hooks.stash["ownership_invitations"] = invitations


@hooks.before("respondToCalendar")
def a_foreign_sender_on_an_invitation(transaction):
    # foreign_sender (403) requires calendar_invitation too, but names message
    # 1, which no link fills and which holds no invitation: the handler
    # answers 400 for the message before it reads the sender (#1942). It is
    # sent on the invitation the same credential's send made.
    address = run_sender(transaction)
    if address is None or " > 403 > " not in transaction.get("name", ""):
        return
    message = hooks.stash.get("ownership_invitations", {}).get(address.lower())
    if isinstance(message, int):
        transaction["fullPath"] = f"/api/v1/messages/{message:d}/calendar-reply"
