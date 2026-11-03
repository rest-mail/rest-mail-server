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
            claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
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
    if not in_an_ownership_run():
        return
    address = credential_address(transaction)
    if address is None or address.lower() == ADMIN:
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or str(body.get("from", "")).lower() != ADMIN:
        return
    body["from"] = address
    body["to"] = [address]
    body.pop("cc", None)
    body.pop("bcc", None)
    hooks.set_json(transaction, body)
