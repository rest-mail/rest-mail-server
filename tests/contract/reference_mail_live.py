"""Check IMAP/POP3 octet counts and decoded IMAP searches on a disposable topology.

Prepare the contract MFA administrator with `chore test:contract-mfa-admin`.
Pass the deployed commit and listener addresses as seen from this process.
`chore test:contract-live-proofs` runs this alongside the other live reports.
"""

import argparse
import base64
import imaplib
import json
import os
import poplib
import re
import secrets
import ssl
import sys
from contextlib import contextmanager
from dataclasses import dataclass
from email import policy
from email.header import decode_header
from email.parser import BytesHeaderParser
from pathlib import Path

import mfa_admin
from api_keys_live import Client, login


@dataclass(frozen=True)
class Fixture:
    name: str
    raw: bytes
    sections: tuple[str, ...] = ("1",)
    filenames: tuple[tuple[str, str], ...] = ()

    @property
    def message_id(self):
        return f"<{self.name}@reference-proof.test>"


def corpus(address):
    def message(name, sender, subject, content, eol=b"\r\n", **kwargs):
        headers = [
            f"Message-ID: <{name}@reference-proof.test>".encode(),
            b"From: " + sender + b" <sender@example.test>",
            f"To: {address}".encode(),
            b"Subject: " + subject,
            b"MIME-Version: 1.0",
        ]
        return Fixture(name, eol.join(headers) + eol + content, **kwargs)

    return (
        message(
            "eight-bit",
            b"=?ISO-8859-2?Q?=A3ukasz?=",
            "Café direct".encode(),
            b"Content-Type: text/plain; charset=utf-8\r\n"
            b"Content-Transfer-Encoding: 8bit\r\n\r\nna\xc3\xafve\r\n",
        ),
        message(
            "bare-lf",
            b"=?KOI8-R?B?" + base64.b64encode("Юлия".encode("koi8-r")) + b"?=",
            b"=?windows-1252?Q?J=F6rg?=",
            b"X-Tag: =?UTF-8?Q?M=C3=BCller?=\n\n.\n..leading\nlast line",
            eol=b"\n",
        ),
        message(
            "mixed-endings",
            b"Plain",
            b"control",
            b"X-Tag: =?UTF-8?Q?M=C3=BCller?=\n"
            b"X-Unknown: =?x-reference-unknown?Q?opaque?=\r\n\n"
            b"First\nSecond\r\nThird\n",
        ),
        message(
            "nested-filenames",
            b"=?UTF-8?Q?Ren=C3=A9e?=",
            b"=?x-reference-unknown?Q?opaque?= =?UTF-8?Q?M=C3=BCller?=",
            b"Content-Type: multipart/mixed; boundary=proof\r\n\r\n"
            b"--proof\r\nContent-Type: multipart/alternative; boundary=inner\r\n\r\n"
            b"--inner\r\nContent-Type: text/plain\r\n\r\nplain\r\n"
            b"--inner\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--inner--\r\n"
            b"--proof\r\nContent-Type: application/octet-stream\r\n"
            b"Content-Disposition: attachment; filename*0*=iso-8859-2'pl'%A3uk;\r\n"
            b" filename*1*=asz.txt\r\nContent-Transfer-Encoding: base64\r\n\r\n"
            b"AQIDBA==\r\n"
            b"--proof\r\nContent-Type: application/octet-stream\r\n"
            b"Content-Disposition: attachment; filename*=windows-1252''%80.txt\r\n"
            b"Content-Transfer-Encoding: base64\r\n\r\nBQY=\r\n--proof--\r\n",
            sections=("1.1", "1.2", "2", "3"),
            filenames=(("2", "Łukasz.txt"), ("3", "€.txt")),
        ),
    )


SEARCH_CASES = (
    ("TEXT", "Müller", ("bare-lf", "mixed-endings", "nested-filenames")),
    ("SUBJECT", "Müller", ("nested-filenames",)),
    ("SUBJECT", "Jörg", ("bare-lf",)),
    ("FROM", "Łukasz", ("eight-bit",)),
    ("FROM", "Юлия", ("bare-lf",)),
    ("FROM", "Müller", ()),
    ("TEXT", "unmatched-é", ()),
)
DOT_LINES = [".", "..leading"]


class Report:
    def __init__(self, commit, topology):
        self.data = {
            "commit": commit,
            "topology": topology,
            "passed": False,
            "cells": [],
        }

    def check(self, name, expected, actual, **details):
        passed = expected == actual
        self.data["cells"].append(
            {
                "name": name,
                "expected": expected,
                "actual": actual,
                "status": "pass" if passed else "fail",
                **details,
            }
        )
        return passed

    @contextmanager
    def probe(self, name):
        try:
            yield
        except (
            AssertionError,
            OSError,
            ValueError,
            TypeError,
            KeyError,
            RuntimeError,
            imaplib.IMAP4.error,
            poplib.error_proto,
        ) as error:
            self.check(name, "successful exchange", f"{type(error).__name__}: {error}")

    def write(self, output):
        cells = self.data["cells"]
        passed = sum(cell["status"] == "pass" for cell in cells)
        self.data["passed"] = bool(cells) and passed == len(cells)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(
            json.dumps(self.data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
        )
        for cell in cells:
            if cell["status"] == "fail":
                print(
                    f"{cell['name']} expected {cell['expected']!r}, observed {cell['actual']!r}"
                )
        print(
            f"Reference mail clients passed {passed}/{len(cells)} checks. Report at {output}."
        )
        return 0 if self.data["passed"] else 1


def octets(value):
    if not isinstance(value, bytes) or not value.isdigit():
        raise ValueError(f"expected an unsigned decimal, got {value!r}")
    return int(value)


# imaplib returns literal fragments as (prefix, octets) followed by the rest of
# the response. Keep those octets intact, including any CR, LF and parentheses.
TOKEN = re.compile(rb'\s*(?:([()])|"((?:[^"\\]|\\.)*)"|\{([0-9]+)\}\r\n|([^\s()"{]+))')


def fetch_attributes(response, uid):
    data = b"".join(
        fragment[0] + b"\r\n" + fragment[1] if isinstance(fragment, tuple) else fragment
        for fragment in response
        if fragment is not None
    )
    prefix = re.match(rb"[0-9]+ \(", data)
    if not prefix:
        raise ValueError("missing FETCH attributes")
    position = prefix.end() - 1

    def value():
        nonlocal position
        token = TOKEN.match(data, position)
        if token is None:
            raise ValueError("invalid IMAP FETCH token")
        position = token.end()
        paren, quoted, length, atom = token.groups()
        if paren == b"(":
            items = []
            while data[position : position + 1] != b")":
                items.append(value())
            position += 1
            return items
        if paren:
            raise ValueError("unexpected closing parenthesis in IMAP FETCH")
        if quoted is not None:
            return re.sub(rb"\\(.)", rb"\1", quoted)
        if length is not None:
            end = position + octets(length)
            if end > len(data):
                raise ValueError("truncated IMAP FETCH literal")
            literal = data[position:end]
            position = end
            return literal
        return None if atom.upper() == b"NIL" else atom

    fields = value()
    if data[position:].strip() or len(fields) % 2:
        raise ValueError("unexpected data after or within FETCH attributes")
    attributes = {
        key.upper(): item
        for key, item in zip(fields[::2], fields[1::2])
        if isinstance(key, bytes)
    }
    if len(attributes) != len(fields) // 2 or octets(attributes[b"UID"]) != uid:
        raise ValueError(f"FETCH did not uniquely identify requested UID {uid}")
    return attributes


def body_parts(structure, section=""):
    if not isinstance(structure, list) or not structure:
        raise ValueError("missing BODYSTRUCTURE part")
    if isinstance(structure[0], list):
        for i, child in enumerate(structure, 1):
            if not isinstance(child, list):
                break  # Multipart subtype and extensions follow the children.
            child_section = f"{section}.{i}" if section else str(i)
            yield from body_parts(child, child_section)
    else:
        if len(structure) < 7:
            raise ValueError("incomplete single-part BODYSTRUCTURE")
        yield section or "1", structure


def attachment_filename(part):
    if len(part) < 9:
        raise ValueError("missing attachment extensions")
    disposition = part[8]  # Basic (non-text, non-message) part: after the MD5 field.
    if (
        not isinstance(disposition, list)
        or len(disposition) != 2
        or not isinstance(disposition[0], bytes)
        or disposition[0].upper() != b"ATTACHMENT"
    ):
        raise ValueError("missing attachment disposition")
    params = disposition[1]
    if (
        not isinstance(params, list)
        or len(params) % 2
        or not all(isinstance(p, bytes) for p in params)
    ):
        raise ValueError("invalid attachment disposition parameters")
    filename = {key.upper(): value for key, value in zip(params[::2], params[1::2])}[
        b"FILENAME"
    ]
    return "".join(
        chunk.decode(charset or "utf-8") if isinstance(chunk, bytes) else chunk
        for chunk, charset in decode_header(filename.decode("utf-8"))
    )


def require_ok(response):
    status, data = response
    if status != "OK":
        raise ValueError(f"IMAP answered {status}: {data!r}")
    return data


def fetch(client, uid, items):
    data = require_ok(client.uid("FETCH", str(uid), f"({items})"))
    return fetch_attributes(data, uid)


def fetch_body(client, uid, section):
    body = fetch(client, uid, f"BODY.PEEK[{section}]")[f"BODY[{section}]".encode()]
    if not isinstance(body, bytes):
        raise TypeError(f"missing or invalid BODY[{section}]")
    return body


def uid_search(client, *criteria):
    data = require_ok(client.uid("SEARCH", *criteria))
    if len(data) != 1 or data[0] is None:
        raise ValueError("missing IMAP SEARCH result")
    return sorted(octets(uid) for uid in data[0].split())


def search(client, utf8, field, term):
    if utf8:
        # RFC 6855 forbids CHARSET after ENABLE UTF8=ACCEPT. These fixed terms
        # contain no IMAP quoted-string metacharacters.
        return uid_search(client, field, f'"{term}"')
    # imaplib sends the next command argument as a synchronizing literal. This
    # lets an IMAP4rev1 session carry non-ASCII without an invalid quoted string.
    client.literal = term.encode("utf-8")
    return uid_search(client, "CHARSET", "UTF-8", field)


def message_id(raw):
    return BytesHeaderParser(policy=policy.default).parsebytes(raw).get("Message-ID")


def check_dot_lines(report, name, raw):
    report.check(
        name,
        DOT_LINES,
        [line.decode("ascii") for line in raw.splitlines() if line.startswith(b".")],
    )


def check_imap(report, args, tls, address, password, fixtures, uids, utf8):
    mode = "imap/utf8" if utf8 else "imap/legacy"
    with (
        report.probe(mode),
        imaplib.IMAP4_SSL(
            args.imap_host, args.imap_port, ssl_context=tls, timeout=20
        ) as client,
    ):
        require_ok(client.login(address, password))
        if utf8:
            require_ok(client.enable("UTF8=ACCEPT"))
            enabled = b" ".join(item for item in client.response("ENABLED")[1] if item)
            if not report.check(
                f"{mode}/enable", True, b"UTF8=ACCEPT" in enabled.upper().split()
            ):
                return
        require_ok(client.select("INBOX", readonly=True))
        report.check(
            f"{mode}/mailbox-uids", sorted(uids.values()), uid_search(client, "ALL")
        )
        for fixture in fixtures:
            name, uid = f"{mode}/{fixture.name}", uids[fixture.name]
            with report.probe(name):
                metadata = fetch(client, uid, "RFC822.SIZE BODYSTRUCTURE")
                body = fetch_body(client, uid, "")
                report.check(
                    f"{name}/message-id", fixture.message_id, message_id(body), uid=uid
                )
                report.check(
                    f"{name}/RFC822.SIZE",
                    len(body),
                    octets(metadata[b"RFC822.SIZE"]),
                    uid=uid,
                )
                if fixture.name == "bare-lf":
                    check_dot_lines(report, f"{name}/dot-lines", body)
                parts = dict(body_parts(metadata[b"BODYSTRUCTURE"]))
                report.check(
                    f"{name}/sections", list(fixture.sections), list(parts), uid=uid
                )
                for section, part in parts.items():
                    with report.probe(f"{name}/BODY[{section}]"):
                        octet_count = len(fetch_body(client, uid, section))
                        report.check(
                            f"{name}/BODY[{section}]",
                            octet_count,
                            octets(part[6]),
                            uid=uid,
                        )
                for section, filename in fixture.filenames:
                    with report.probe(f"{name}/filename/{section}"):
                        report.check(
                            f"{name}/filename/{section}",
                            filename,
                            attachment_filename(parts[section]),
                            uid=uid,
                        )
        for field, term, names in SEARCH_CASES:
            name = f"{mode}/SEARCH {field} {term}"
            with report.probe(name):
                report.check(
                    name,
                    sorted(uids[fixture] for fixture in names),
                    search(client, utf8, field, term),
                )


def pop_listing(lines):
    sizes = {}
    for line in lines:
        number, size = map(octets, line.split())
        if number in sizes or not number:
            raise ValueError(f"invalid POP3 LIST message number {number}")
        sizes[number] = size
    return sizes


def check_pop(report, args, tls, address, password, fixtures):
    by_id = {fixture.message_id: fixture.name for fixture in fixtures}
    with report.probe("pop3"):
        client = poplib.POP3_SSL(
            args.pop3_host, args.pop3_port, context=tls, timeout=20
        )
        try:
            client.user(address)
            client.pass_(password)
            count, total = client.stat()
            report.check("pop3/STAT messages", len(fixtures), count)
            listed = pop_listing(client.list()[1])
            numbers = list(range(1, len(fixtures) + 1))
            report.check("pop3/LIST messages", numbers, sorted(listed))
            received, ids = 0, []
            for number in numbers:
                name = f"pop3/message-{number}"
                with report.probe(name):
                    # poplib counts the actual received line terminators and removes
                    # one stuffing dot. Its lines omit the terminators and terminator dot.
                    response, lines, size = client.retr(number)
                    received += size
                    raw = b"".join(line + b"\r\n" for line in lines)
                    identity = message_id(raw)
                    ids.append(identity or "")
                    fixture = by_id.get(identity)
                    report.check(f"{name}/CRLF octets", len(raw), size, fixture=fixture)
                    report.check(
                        f"{name}/LIST", size, listed.get(number), fixture=fixture
                    )
                    single = pop_listing([client.list(number)[3:].strip()])
                    report.check(
                        f"{name}/LIST {number}",
                        {str(number): size},
                        {str(key): value for key, value in single.items()},
                        fixture=fixture,
                    )
                    hint = re.fullmatch(
                        rb"\+OK ([0-9]+) octets", response, re.IGNORECASE
                    )
                    report.check(
                        f"{name}/RETR octets",
                        size,
                        octets(hint[1]) if hint else None,
                        fixture=fixture,
                    )
                    if fixture == "bare-lf":
                        check_dot_lines(report, f"{name}/dot-lines", raw)
            report.check("pop3/STAT octets", received, total)
            report.check("pop3/message-ids", sorted(by_id), sorted(ids))
        finally:
            client.quit()


def api_expect(report, client, name, expected, method, path, body=None, token=None):
    status, payload = client.call(method, path, body, token)
    if not report.check(name, expected, status, method=method, path=path):
        return None
    if status != 204:
        data = (payload or {}).get("data")
        if not isinstance(data, dict):
            raise ValueError(f"{name}: missing response data")
        return data


def run(args):
    topology = {
        "name": args.topology,
        "api": args.endpoint,
        "imap": [args.imap_host, args.imap_port],
        "pop3": [args.pop3_host, args.pop3_port],
        "image_tag": os.environ.get("RESTMAIL_TOPOLOGY_IMAGE_TAG"),
    }
    report = Report(args.commit, topology)
    client = Client(args.endpoint)
    account_id = None
    with report.probe("setup"):
        try:
            tls = ssl.create_default_context(cafile=args.ca_file)
            if args.insecure:
                tls.check_hostname = False
                tls.verify_mode = ssl.CERT_NONE
            admin = mfa_admin.session(args.endpoint)
            address = f"reference-proof-{secrets.token_hex(8)}@restmail.test"
            password = "Fixture!" + secrets.token_urlsafe(24)
            account = api_expect(
                report,
                client,
                "create disposable mailbox",
                201,
                "POST",
                "/api/v1/accounts",
                {"address": address, "password": password},
                admin,
            )
            if account is not None:
                account_id = account["id"]
                token = login(client, address, password)["access_token"]
                fixtures = corpus(address)
                uids = {}
                for fixture in fixtures:
                    data = api_expect(
                        report,
                        client,
                        f"import/{fixture.name}",
                        200,
                        "POST",
                        f"/api/v1/accounts/{account_id}/messages/import",
                        {
                            "raw_message": base64.b64encode(fixture.raw).decode(
                                "ascii"
                            ),
                            "internal_date": "2026-01-01T00:00:00Z",
                        },
                        token,
                    )
                    if data is not None and report.check(
                        f"import/{fixture.name}/stored", True, data["imported"]
                    ):
                        uids[fixture.name] = data["id"]
                if len(uids) == len(fixtures):
                    for utf8 in (False, True):
                        check_imap(
                            report, args, tls, address, password, fixtures, uids, utf8
                        )
                    check_pop(report, args, tls, address, password, fixtures)
        finally:
            if account_id is not None:
                with report.probe("remove disposable mailbox"):
                    api_expect(
                        report,
                        client,
                        "remove disposable mailbox",
                        204,
                        "DELETE",
                        f"/api/v1/admin/mailboxes/{account_id}",
                        token=admin,
                    )
    return report.write(args.output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("endpoint")
    parser.add_argument("output", type=Path)
    parser.add_argument(
        "--commit", required=True, help="full SHA deployed on the topology"
    )
    parser.add_argument("--topology", required=True, help="disposable topology name")
    parser.add_argument("--imap-host", default="imap.restmail.test")
    parser.add_argument("--imap-port", type=int, default=993)
    parser.add_argument("--pop3-host", default="pop3.restmail.test")
    parser.add_argument("--pop3-port", type=int, default=995)
    tls = parser.add_mutually_exclusive_group()
    tls.add_argument("--ca-file", help="CA for the IMAP and POP3 listeners")
    tls.add_argument(
        "--insecure",
        action="store_true",
        help="allow the disposable testbed's TLS certificates",
    )
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{40}", args.commit):
        parser.error("--commit must be a full Git SHA")
    return run(args)


if __name__ == "__main__":
    sys.exit(main())
