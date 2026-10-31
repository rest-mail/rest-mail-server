#!/usr/bin/env python3
"""Look for any secret the contract run sent, in everything the server answered.

The examples phase records its traffic as a HAR file (vertrag's har reporter).
Secrets in JSON, Authorization headers and session cookies are collected:
passwords, private keys and recovery codes the run sent, plus credentials the
server issued once (app passwords, API keys, enrollment secrets and recovery
codes, and session tokens). Every response body, header and cookie is searched
for each of them, as is the audit log read through the API after the run (#673).

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
signed in as the run's two-factor administrator (audit_reader_body).
Exit 1 with one line per finding; 0 when the run is clean. Exit 2 if input or
audit retrieval cannot be verified; an incomplete audit never reports clean.
"""

import argparse
import base64
import json
import sys
import urllib.request
from http.cookies import CookieError, SimpleCookie
from pathlib import Path
from urllib.parse import unquote, urlencode, urlsplit, urlunsplit

# The fields that carry a secret the client sends: passwords (an account's, a
# relay's), DKIM and certificate private keys, a recovery code.
SENT_FIELDS = {
    "password",
    "new_password",
    "current_password",
    "private_key",
    "key_pem",
    "recovery_code",
    "refresh_token",
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


# These are the server's credential cookies (internal/auth/auth.go). CSRF and
# preferences are intentionally not credentials. HAR may store both a Cookie
# header and a decoded cookies array; the scan deduplicates collected values.
SESSION_COOKIES = {
    "restmail_access": "access_token",
    "restmail_refresh": "refresh_token",
}


def usable_secret(value):
    return isinstance(value, str) and len(value) >= MIN_LENGTH and value != REDACTED


def cookie_values(message, header_name):
    for cookie in message.get("cookies", []):
        yield cookie.get("name", ""), cookie.get("value", "")
    for header in message.get("headers", []):
        if header.get("name", "").lower() != header_name:
            continue
        try:
            cookies = SimpleCookie()
            cookies.load(header.get("value", ""))
        except CookieError:
            continue
        for name, cookie in cookies.items():
            yield name, cookie.value


def authorization_values(message):
    for header in message.get("headers", []):
        if header.get("name", "").lower() != "authorization":
            continue
        parts = header.get("value", "").split(None, 1)
        if len(parts) != 2:
            continue
        scheme, value = parts
        if scheme.lower() == "bearer":
            yield "access_token", value
        elif scheme.lower() == "basic":
            try:
                decoded = base64.b64decode(value, validate=True).decode("utf-8")
            except (ValueError, UnicodeError):
                continue
            _, separator, password = decoded.partition(":")
            if separator:
                yield "password", password


def session_values(message, header_name):
    for name, value in cookie_values(message, header_name):
        if name in SESSION_COOKIES:
            yield SESSION_COOKIES[name], value


def response_parts(entry):
    response = entry.get("response", {})
    yield "", body_text(response.get("content"))
    for header in response.get("headers", []):
        name, value = header.get("name", ""), header.get("value", "")
        if name.lower() in {"location", "content-location"}:
            value = unquote(value)
        yield f"in response header {name} ", json.dumps(value)
    for name, value in cookie_values(response, "set-cookie"):
        yield f"in response cookie {name} ", json.dumps(value)
    for field, value in authorization_values(response):
        yield f"in response Authorization {field} ", json.dumps(value)


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
    """Report a secret's length without disclosing even a prefix."""
    return f"<secret> ({len(value)} chars)"


def status_of(entry):
    return entry.get("response", {}).get("status", 0)


# Answers that document the API carry the spec's example values on purpose.
DOC_PATHS = ("/api/docs/",)


def label(entry):
    request = entry.get("request", {})
    name = entry.get("comment") or ""
    # Query/fragment values and URL userinfo can themselves be credentials.
    # The decoded route still identifies the operation and permits the final
    # known-secret redaction to cover a secret embedded in a path segment.
    url = urlsplit(request.get("url", "?"))
    public_url = unquote(
        urlunsplit((url.scheme, url.netloc.rsplit("@", 1)[-1], url.path, "", ""))
    )
    return f"{request.get('method', '?')} {public_url} [{name}]"


def path_of(entry):
    url = entry.get("request", {}).get("url", "")
    rest = url.split("://", 1)[-1]
    return "/" + rest.split("/", 1)[1].split("?", 1)[0] if "/" in rest else "/"


def scan(har, audit=(), known=()):
    """Findings for a HAR document and the audit entries read after the run.
    known are (field, secret, where it was sent) the recording redacts: the
    fixture admin's password."""
    entries = har.get("log", {}).get("entries", [])
    responses = [tuple(response_parts(entry)) for entry in entries]
    answers = [parts[0][1] for parts in responses]
    # secret -> (field, index of the answer that issued it, or None if sent).
    secrets = {value: (field, None, origin) for field, value, origin in known}
    for i, entry in enumerate(entries):
        post = entry.get("request", {}).get("postData") or {}
        for field, value in json_secrets(body_text(post), SENT_FIELDS):
            secrets.setdefault(value, (field, None, label(entry)))
        request = entry.get("request", {})
        sent = list(authorization_values(request)) + list(
            session_values(request, "cookie")
        )
        for field, value in sent:
            if usable_secret(value):
                secrets.setdefault(value, (field, None, label(entry)))
        if 200 <= status_of(entry) < 300 and not path_of(entry).startswith(DOC_PATHS):
            fields = ISSUED_FIELDS
            path = path_of(entry)
            if path == "/api/v1/push/devices" or path.startswith(
                "/api/v1/push/devices/"
            ):
                fields = fields - {"token"}
            issued = json_secrets(answers[i], fields)
            issued += list(session_values(entry.get("response", {}), "set-cookie"))
            for field, value in issued:
                if usable_secret(value):
                    secrets.setdefault(value, (field, i, label(entry)))

    findings = []
    for value, (field, issued, origin) in secrets.items():
        how = "issued by" if issued is not None else "sent in"
        for j in range(len(entries)):
            if j == issued:
                continue
            for transport, text in responses[j]:
                if not transport and path_of(entries[j]).startswith(DOC_PATHS):
                    continue
                location = where(text, value)
                if location:
                    findings.append(
                        f"{label(entries[j])} answers the {field} {how} {origin}, {hint(value)} {transport}{location}"
                    )
        for record in audit:
            location = where(record, value)
            if location:
                action = json.loads(record).get("action", "?")
                findings.append(
                    f"the audit log's {action} entry holds the {field} {how} {origin}, {hint(value)} {location}"
                )
                break
    # Metadata is untrusted too: URLs, HAR comments, object keys, header names
    # and audit actions can contain any collected secret. Redact every known
    # value from every finding, not just the value that triggered that finding.
    for value in sorted(secrets, key=len, reverse=True):
        findings = [finding.replace(value, "<secret>") for finding in findings]
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


# A contract run must fail rather than declare a truncated audit log clean.
MAX_AUDIT_PAGES = 1000
MAX_AUDIT_RESPONSE_BYTES = 16 * 1024 * 1024
MAX_AUDIT_TOTAL_BYTES = 64 * 1024 * 1024


class AuditReadError(Exception):
    """The audit could not be read completely and safely."""


class _NoAuditRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # urllib otherwise forwards the original Authorization on redirects.
        # Refuse login redirects too, before trusting a different endpoint.
        raise AuditReadError("audit redirect refused")


def _audit_json(response):
    raw = response.read(MAX_AUDIT_RESPONSE_BYTES + 1)
    if len(raw) > MAX_AUDIT_RESPONSE_BYTES:
        raise AuditReadError("audit response exceeds the safety limit")
    return json.loads(raw)


def audit_reader_body():
    """Reading the audit log takes recent proof of a second factor: the reader
    signs in as the run's two-factor administrator (mfa_admin.py), with a real
    code, not as auth.login's fixture admin."""
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    import mfa_admin

    return mfa_admin.sign_in_body()


def read_audit(config_path, known=None):
    """Read every audit page without redirects. Add this reader's freshly
    issued credentials to known too: its login is outside the recorded HAR.
    Fail on incomplete, malformed, cyclic or excessive pagination.
    """
    import yaml

    with open(config_path) as stream:
        config = yaml.safe_load(stream)
    endpoint = config["endpoint"].rstrip("/")
    signin = config["auth"]["login"]
    opener = urllib.request.build_opener(_NoAuditRedirect())
    login = urllib.request.Request(
        endpoint + signin["path"],
        data=json.dumps(audit_reader_body()).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with opener.open(login, timeout=30) as response:
        issued = _audit_json(response)
        token = issued["data"]["access_token"]
        if not usable_secret(token) or "\r" in token or "\n" in token:
            raise AuditReadError("audit login did not issue a usable token")
        if known is not None:
            credentials = json_secrets(json.dumps(issued), ISSUED_FIELDS)
            credentials += list(
                session_values(
                    {
                        "headers": [
                            {"name": "set-cookie", "value": v}
                            for v in response.headers.get_all("Set-Cookie", [])
                        ]
                    },
                    "set-cookie",
                )
            )
            known.extend(
                (field, value, "the audit reader's login")
                for field, value in credentials
                if usable_secret(value)
            )
    records, cursor, seen = [], None, set()
    total_bytes = 0
    for _ in range(MAX_AUDIT_PAGES):
        query = {"limit": "200"}
        if cursor is not None:
            query["before_seq"] = cursor
        request = urllib.request.Request(
            endpoint + "/api/v1/admin/audit?" + urlencode(query),
            headers={"Authorization": "Bearer " + token},
        )
        with opener.open(request, timeout=30) as response:
            page = _audit_json(response)
        if not isinstance(page, dict) or not isinstance(page.get("data"), list):
            raise AuditReadError("invalid audit page")
        entries = page["data"]
        pagination = page.get("pagination")
        if (
            not all(isinstance(entry, dict) for entry in entries)
            or not isinstance(pagination, dict)
            or type(pagination.get("has_more")) is not bool
        ):
            raise AuditReadError("invalid audit pagination")
        batch = [json.dumps(entry) for entry in entries]
        total_bytes += sum(len(record.encode("utf-8")) for record in batch)
        if total_bytes > MAX_AUDIT_TOTAL_BYTES:
            raise AuditReadError("audit total exceeds the safety limit")
        records.extend(batch)
        if not pagination["has_more"]:
            return records
        cursor = pagination.get("cursor")
        if (
            not entries
            or not isinstance(cursor, str)
            or not cursor.strip()
            or cursor in seen
        ):
            raise AuditReadError("incomplete or cyclic audit pagination")
        seen.add(cursor)
    raise AuditReadError("audit page count exceeds the safety limit")


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("har")
    parser.add_argument("--audit-from", metavar="VERTRAG_YML")
    args = parser.parse_args(argv)
    try:
        with open(args.har) as stream:
            har = json.load(stream)
        known = login_secrets(args.audit_from) if args.audit_from else []
        audit = read_audit(args.audit_from, known) if args.audit_from else []
        findings = scan(har, audit, known)
        entries = len(har.get("log", {}).get("entries", []))
    except Exception:  # noqa: BLE001 -- CLI boundary must hide untrusted credentials.
        # HTTP/JSON/YAML errors may contain credentials, URLs or response text.
        # Never print them or emit a clean verdict for an incomplete scan.
        print(
            "secret scan failed: input or audit log could not be verified",
            file=sys.stderr,
        )
        return 2
    for finding in findings:
        print("secret echoed:", finding)
    print(
        f"secret scan: {entries} recorded exchanges, {len(audit)} audit entries, {len(findings)} findings"
    )
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
