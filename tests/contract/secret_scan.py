#!/usr/bin/env python3
"""Look for any secret the contract run sent, in everything the server answered.

The examples phase records its traffic as a HAR file (vertrag's har reporter).
Every secret in it is collected: the passwords, private keys, TOTP secrets and
recovery codes the run sent in a request, and the ones the server issued once
in a response (an app password, an API key, an enrollment's secret and codes,
a session's tokens). Then every response body is searched for each of them, and
so is the audit log, read through the API at the end of the run (#673).

A secret may appear in exactly one answer: the one that issued it. Anywhere
else, in any other answer or in the audit log, it is a finding.

vertrag replaces every value of its own login body (the fixture admin's
address as well as its password) and the session token with "<redacted>"
wherever they appear in a recorded body, so a "<redacted>" cannot be judged:
it is the admin's address in every message it sent. The fixture admin's
password is searched for in the audit log instead, which is read unredacted;
that it is never answered back is the one thing the recording cannot show.

Usage: secret_scan.py <run.har> [--audit-from vertrag.yml]

With --audit-from, the audit log is read from the configuration's endpoint,
signed in as its auth.login does (the fixture admin).
Exit 1 with one line per finding; 0 when the run is clean.
"""

import argparse
import base64
import json
import sys
import urllib.request

# The fields that carry a secret the client sends: passwords (an account's, a
# relay's), DKIM and certificate private keys, a recovery code.
SENT_FIELDS = {
    "password",
    "new_password",
    "current_password",
    "private_key",
    "key_pem",
    "recovery_code",
}
# The fields that carry a secret the server issues once: an app password, an
# enrollment's secret and recovery codes, a session's tokens, an API key's
# token. A push device's token is the client's, not a credential, and is not
# collected. Short values (a six-digit TOTP code) are left out: they match by
# chance.
ISSUED_FIELDS = {
    "password",
    "secret",
    "recovery_codes",
    "access_token",
    "refresh_token",
    "token",
}
MIN_LENGTH = 8
REDACTED = "<redacted>"


def body_text(content):
    """A HAR body's text, decoded when the archive stored it as base64."""
    if not content:
        return ""
    text = content.get("text") or ""
    if content.get("encoding") == "base64" or content.get("_encoding") == "base64":
        try:
            return base64.b64decode(text).decode("utf-8", "replace")
        except ValueError:
            return text
    return text


def secrets_in(value, fields, found):
    """Collect (field, secret) pairs for fields from a decoded JSON value. An
    error object is left out: its details name the fields a refusal is about,
    and say what is wrong with them, in words that are no secret."""
    if isinstance(value, dict):
        for key, item in value.items():
            if key == "error":
                continue
            if key in fields:
                for s in item if isinstance(item, list) else [item]:
                    if isinstance(s, str) and len(s) >= MIN_LENGTH and s != REDACTED:
                        found.append((key, s))
            secrets_in(item, fields, found)
    elif isinstance(value, list):
        for item in value:
            secrets_in(item, fields, found)
    return found


def json_secrets(text, fields):
    try:
        return secrets_in(json.loads(text), fields, [])
    except ValueError:
        return []


def where(text, value):
    """Locate a secret in decoded JSON strings/keys or plain text; None if absent.

    Match logical strings, not their JSON encoding: private keys contain
    newlines, and passwords may contain quotes, backslashes or Unicode.
    """
    try:
        doc = json.loads(text)
    except ValueError:
        return "in the text" if value in text else None
    paths = []

    def walk(node, path):
        if isinstance(node, dict):
            for key, item in node.items():
                if value in key:
                    paths.append(f"{path} (object key)")
                walk(item, f"{path}.{key.replace(value, '<secret>')}")
        elif isinstance(node, list):
            for i, item in enumerate(node):
                walk(item, f"{path}[{i}]")
        elif isinstance(node, (str, int, float)) and value in str(node):
            paths.append(path)

    walk(doc, "$")
    return "at " + ", ".join(paths[:3]) if paths else None


def hint(value):
    """A secret as a finding shows it: its first characters and its length."""
    return f"{value[:4]}…({len(value)})"


def status_of(entry):
    return entry.get("response", {}).get("status", 0)


# Answers that document the API carry the spec's example values on purpose.
DOC_PATHS = ("/api/docs/",)


def label(entry):
    request = entry.get("request", {})
    name = entry.get("comment") or ""
    return f"{request.get('method', '?')} {request.get('url', '?')} [{name}]"


def path_of(entry):
    url = entry.get("request", {}).get("url", "")
    rest = url.split("://", 1)[-1]
    return "/" + rest.split("/", 1)[1].split("?", 1)[0] if "/" in rest else "/"


def scan(har, audit=(), known=()):
    """Findings for a HAR document and the audit entries read after the run.
    known are (field, secret, where it was sent) the recording redacts: the
    fixture admin's password."""
    entries = har.get("log", {}).get("entries", [])
    answers = [body_text(e.get("response", {}).get("content")) for e in entries]
    # secret -> (field, index of the answer that issued it, or None if sent).
    secrets = {value: (field, None, origin) for field, value, origin in known}
    for i, entry in enumerate(entries):
        post = entry.get("request", {}).get("postData") or {}
        for field, value in json_secrets(body_text(post), SENT_FIELDS):
            secrets.setdefault(value, (field, None, label(entry)))
        if 200 <= status_of(entry) < 300:
            for field, value in json_secrets(answers[i], ISSUED_FIELDS):
                secrets.setdefault(value, (field, i, label(entry)))

    findings = []
    for value, (field, issued, origin) in secrets.items():
        how = "issued by" if issued is not None else "sent in"
        for j, answer in enumerate(answers):
            if j == issued or path_of(entries[j]).startswith(DOC_PATHS):
                continue
            location = where(answer, value)
            if location:
                findings.append(
                    f"{label(entries[j])} answers the {field} {how} {origin}, {hint(value)} {location}"
                )
        for record in audit:
            location = where(record, value)
            if location:
                action = json.loads(record).get("action", "?")
                findings.append(
                    f"the audit log's {action} entry holds the {field} {how} {origin}, {hint(value)} {location}"
                )
                break
    return findings


def login_secrets(config_path):
    """The secrets of the configuration's auth.login body: its password."""
    import yaml

    with open(config_path) as stream:
        body = yaml.safe_load(stream)["auth"]["login"]["body"]
    return [
        (field, body[field], "vertrag.yml's auth.login")
        for field in SENT_FIELDS
        if isinstance(body.get(field), str) and len(body[field]) >= MIN_LENGTH
    ]


def read_audit(config_path):
    """Every audit entry, newest first, each as its JSON text, read from the
    vertrag configuration's endpoint as its auth.login signs in."""
    import yaml

    with open(config_path) as stream:
        config = yaml.safe_load(stream)
    endpoint = config["endpoint"].rstrip("/")
    signin = config["auth"]["login"]
    login = urllib.request.Request(
        endpoint + signin["path"],
        data=json.dumps(signin["body"]).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(login, timeout=30) as response:
        token = json.load(response)["data"]["access_token"]
    records, cursor = [], None
    while True:
        url = (
            endpoint
            + "/api/v1/admin/audit?limit=200"
            + (f"&before_seq={cursor}" if cursor else "")
        )
        request = urllib.request.Request(
            url, headers={"Authorization": "Bearer " + token}
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            page = json.load(response)
        records += [json.dumps(entry) for entry in page["data"]]
        pagination = page.get("pagination") or {}
        if not pagination.get("has_more") or not pagination.get("cursor"):
            return records
        cursor = pagination["cursor"]


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("har")
    parser.add_argument("--audit-from", metavar="VERTRAG_YML")
    args = parser.parse_args(argv)
    with open(args.har) as stream:
        har = json.load(stream)
    audit = read_audit(args.audit_from) if args.audit_from else []
    known = login_secrets(args.audit_from) if args.audit_from else []
    findings = scan(har, audit, known)
    entries = len(har.get("log", {}).get("entries", []))
    for finding in findings:
        print("secret echoed:", finding)
    print(
        f"secret scan: {entries} recorded exchanges, {len(audit)} audit entries, {len(findings)} findings"
    )
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
