#!/usr/bin/env python3
"""Reset what the examples gate leaves behind before coverage repeats it.

The full examples gate runs before coverage in a separate vertrag process.
Its Receipts folder and spam@example.org contact, and the account the
createAccount example made, need removing before the second process repeats
their create examples. Deleting an account takes a session signed in with
2FA, which the primary fixture admin has not, so mfa_admin.py signs that one
in. The queue fixture is replenished separately by chore
test:contract-fixtures.
"""
import json
from pathlib import Path
import sys
import urllib.error
import urllib.parse
import urllib.request

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import mfa_admin  # noqa: E402

ADDRESS = "admin@restmail.test"
EXAMPLES = Path(__file__).resolve().parents[2] / "openapi-example.json"
BLOCKED_SENDER = "spam@example.org"


def prepare(config):
    login = config["auth"]["login"]
    if login["path"] != "/api/v1/auth/login" or login["body"].get("email") != ADDRESS:
        raise ValueError("repeat fixtures require the disposable admin@restmail.test account")
    endpoint = config["endpoint"].rstrip("/")
    headers = {"Content-Type": "application/json"}

    def send(method, path, status, body=None, missing=False):
        request = urllib.request.Request(endpoint + path, method=method, headers=headers,
                                         data=None if body is None else json.dumps(body).encode())
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                if response.status != status:
                    raise RuntimeError("unexpected response preparing repeated contract examples")
                return json.load(response) if status != 204 else None
        except urllib.error.HTTPError as error:
            if not missing or error.code != 404:
                raise
            return None

    token = send("POST", login["path"], 200, login["body"])["data"]["access_token"]
    headers["Authorization"] = "Bearer " + token
    accounts = [account for account in send("GET", "/api/v1/admin/mailboxes", 200)["data"]
                if account.get("address") == ADDRESS]
    if len(accounts) != 1 or type(accounts[0].get("id")) is not int or accounts[0]["id"] < 1:
        raise RuntimeError("fixture admin mailbox is missing or ambiguous")
    base = "/api/v1/accounts/" + str(accounts[0]["id"])
    send("DELETE", base + "/folders/Receipts", 204, missing=True)
    contacts = send("GET", base + "/contacts?" + urllib.parse.urlencode({"search": BLOCKED_SENDER}), 200)["data"]
    for contact in contacts:
        if contact.get("email") == BLOCKED_SENDER:
            contact_id = contact.get("id")
            if type(contact_id) is not int or contact_id < 1:
                raise RuntimeError("invalid repeated contact fixture ID")
            send("DELETE", base + "/contacts/" + str(contact_id), 204, missing=True)

    # The account the createAccount example made, which the in-process
    # deleteMailbox proof (it needs 2FA) no longer removes on the live run.
    created = json.loads(EXAMPLES.read_text())["createAccount"]["created"]["request"]["body"]["address"]
    for account in send("GET", "/api/v1/admin/mailboxes?limit=200", 200)["data"]:
        if account.get("address") == created:
            if type(account.get("id")) is not int or account["id"] < 1:
                raise RuntimeError("invalid created account fixture ID")
            headers["Authorization"] = "Bearer " + mfa_admin.session(endpoint)
            send("DELETE", "/api/v1/admin/mailboxes/" + str(account["id"]), 204, missing=True)
            headers["Authorization"] = "Bearer " + token


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: repeat_fixtures.py <vertrag.yml>")
    with Path(sys.argv[1]).open() as source:
        prepare(yaml.safe_load(source))
    print("repeated contract example fixtures prepared")
