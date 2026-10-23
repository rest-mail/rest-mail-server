#!/usr/bin/env python3
"""Reset the two named examples left in the disposable fixture admin mailbox.

The full examples gate runs before coverage in a separate vertrag process.
Only its Receipts folder and spam@example.org contact need removing before
the second process repeats their create examples. The queue fixture is
replenished separately by chore test:contract-fixtures.
"""
import json
from pathlib import Path
import sys
import urllib.error
import urllib.parse
import urllib.request

import yaml

ADDRESS = "admin@restmail.test"
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


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: repeat_fixtures.py <vertrag.yml>")
    with Path(sys.argv[1]).open() as source:
        prepare(yaml.safe_load(source))
    print("repeated contract example fixtures prepared")
