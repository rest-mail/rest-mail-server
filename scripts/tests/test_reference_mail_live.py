"""Exercise the independent reference-client report parsers and comparisons."""

import json
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock, Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "tests" / "contract"))
import reference_mail_live as proof


class FetchTest(unittest.TestCase):
    def test_literal_octets_and_attributes_after_the_literal(self):
        literal = b"Subject: caf\xc3\xa9\r\n\r\nHi )\n."
        fields = proof.fetch_attributes(
            [(b"1 (BODY[] {%d}" % len(literal), literal), b" UID 75 RFC822.SIZE 23)"],
            75,
        )
        self.assertEqual(
            fields, {b"BODY[]": literal, b"UID": b"75", b"RFC822.SIZE": b"23"}
        )

    def test_empty_literal_is_distinct_from_nil(self):
        literal = proof.fetch_attributes([(b"1 (UID 75 BODY[1] {0}", b""), b")"], 75)
        nil = proof.fetch_attributes([b"1 (UID 75 BODY[1] NIL)"], 75)
        self.assertEqual(literal[b"BODY[1]"], b"")
        self.assertIsNone(nil[b"BODY[1]"])

    def test_rejects_missing_truncated_duplicate_or_wrong_uid(self):
        for data in (
            [None],
            [(b"1 (UID 75 BODY[] {12}", b"hi"), b")"],
            [b"1 (UID 75 UID 75)"],
            [b"1 (UID 76)"],
            [b"1 (UID 75) extra"],
        ):
            with self.subTest(data=data), self.assertRaises(ValueError):
                proof.fetch_attributes(data, 75)

    def test_bodystructure_sizes_sections_and_decoded_filenames(self):
        plain = b'("TEXT" "PLAIN" NIL NIL NIL "7BIT" 31 1)'
        html = b'("TEXT" "HTML" NIL NIL NIL "7BIT" 15 1)'
        attachment = (
            b'("APPLICATION" "OCTET-STREAM" NIL NIL NIL "BASE64" 8 NIL '
            b'("attachment" ("FileName" "=?UTF-8?Q?=C5=81ukasz.txt?=")))'
        )
        utf8 = "€.txt".encode()
        data = [
            (
                b"1 (UID 75 BODYSTRUCTURE (("
                + plain
                + html
                + b' "ALTERNATIVE")'
                + attachment
                + b'("APPLICATION" "OCTET-STREAM" NIL NIL NIL "BASE64" 4 NIL '
                + b'("ATTACHMENT" ("FILENAME" {%d}' % len(utf8),
                utf8,
            ),
            b'))) "MIXED"))',
        ]
        parts = dict(
            proof.body_parts(proof.fetch_attributes(data, 75)[b"BODYSTRUCTURE"])
        )
        self.assertEqual(
            {section: proof.octets(part[6]) for section, part in parts.items()},
            {"1.1": 31, "1.2": 15, "2": 8, "3": 4},
        )
        self.assertEqual(proof.attachment_filename(parts["2"]), "Łukasz.txt")
        self.assertEqual(proof.attachment_filename(parts["3"]), "€.txt")

    def test_search_uses_charset_only_before_enable_and_preserves_duplicate_uids(self):
        client = Mock()
        client.uid.return_value = "OK", [b"9 4 9"]
        self.assertEqual(proof.search(client, False, "TEXT", "Müller"), [4, 9, 9])
        self.assertEqual(client.literal, "Müller".encode())
        client.uid.assert_called_once_with("SEARCH", "CHARSET", "UTF-8", "TEXT")
        client.reset_mock()
        client.uid.return_value = "OK", [b""]
        self.assertEqual(proof.search(client, True, "FROM", "Łukasz"), [])
        client.uid.assert_called_once_with("SEARCH", "FROM", '"Łukasz"')

    def test_imap_flags_size_mismatches_in_both_sessions(self):
        fixture = proof.corpus("test@restmail.test")[0]
        tls_client = MagicMock()
        client = tls_client.__enter__.return_value
        client.login.return_value = client.enable.return_value = (
            client.select.return_value
        ) = ("OK", [])
        client.response.return_value = "ENABLED", [b"UTF8=ACCEPT"]

        def uid(command, *args):
            if command == "SEARCH":
                return "OK", [b"75"] if args[-1] == "ALL" else [b""]
            if args[-1] == "(RFC822.SIZE BODYSTRUCTURE)":
                return "OK", [
                    b'1 (UID 75 RFC822.SIZE 1 BODYSTRUCTURE ("TEXT" "PLAIN" NIL NIL NIL "7BIT" 2 1))'
                ]
            section, raw = (
                (b"1", b"hi!") if args[-1] == "(BODY.PEEK[1])" else (b"", fixture.raw)
            )
            return "OK", [(b"1 (UID 75 BODY[%s] {%d}" % (section, len(raw)), raw), b")"]

        client.uid.side_effect = uid
        for utf8 in (False, True):
            with (
                self.subTest(utf8=utf8),
                patch.object(proof.imaplib, "IMAP4_SSL", return_value=tls_client),
                patch.object(proof, "SEARCH_CASES", ()),
            ):
                report = proof.Report("test", {})
                args = SimpleNamespace(imap_host="test", imap_port=993)
                proof.check_imap(
                    report,
                    args,
                    None,
                    "test",
                    "secret",
                    [fixture],
                    {fixture.name: 75},
                    utf8,
                )
                by_check = {
                    cell["name"].rsplit("/", 1)[-1]: cell
                    for cell in report.data["cells"]
                }
                for check, expected, advertised in (
                    ("RFC822.SIZE", len(fixture.raw), 1),
                    ("BODY[1]", 3, 2),
                ):
                    cell = by_check[check]
                    self.assertEqual(
                        (cell["expected"], cell["actual"], cell["status"]),
                        (expected, advertised, "fail"),
                    )


class PopTest(unittest.TestCase):
    def test_listing_requires_unique_positive_numbers_and_decimal_sizes(self):
        self.assertEqual(proof.pop_listing([b"1 37", b"2 16"]), {1: 37, 2: 16})
        for lines in ([b"1 7", b"1 8"], [b"1 -7"], [b"0 7"], [b"1 7 8"]):
            with self.subTest(lines=lines), self.assertRaises(ValueError):
                proof.pop_listing(lines)

    def test_pop_uses_destuffed_bytes_and_detects_all_wrong_size_advertisements(self):
        fixture = proof.corpus("test@restmail.test")[1]
        lines = fixture.raw.splitlines()
        size = sum(len(line) + 2 for line in lines)
        client = Mock()
        client.retr.return_value = b"+OK %d octets" % (size - 1), lines, size
        client.stat.return_value = 1, size + 1
        client.list.side_effect = [
            (b"+OK", [b"1 %d" % (size + 2)], 0),
            b"+OK 1 %d" % (size + 3),
        ]
        report = proof.Report("test", {})
        args = SimpleNamespace(pop3_host="test", pop3_port=995)
        with patch.object(proof.poplib, "POP3_SSL", return_value=client):
            proof.check_pop(report, args, None, "test", "secret", [fixture])
        cells = {cell["name"]: cell for cell in report.data["cells"]}
        for name in (
            "pop3/message-1/LIST",
            "pop3/message-1/LIST 1",
            "pop3/message-1/RETR octets",
            "pop3/STAT octets",
        ):
            self.assertEqual(cells[name]["status"], "fail")
        self.assertEqual(cells["pop3/message-1/dot-lines"]["status"], "pass")
        self.assertEqual(cells["pop3/message-1/CRLF octets"]["status"], "pass")
        client.quit.assert_called_once()

    def test_receipt_writes_values_and_fails_after_an_exchange_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "reference-mail" / "report.json"
            report = proof.Report("sha", {"name": "topology"})
            report.check("good", 3, 3)
            with report.probe("failed exchange"):
                raise OSError("connection refused")
            with patch("builtins.print"):
                self.assertEqual(report.write(output), 1)
            receipt = json.loads(output.read_text())
            self.assertEqual(receipt["commit"], "sha")
            self.assertEqual(receipt["topology"], {"name": "topology"})
            self.assertFalse(receipt["passed"])
            self.assertEqual(
                receipt["cells"][1]["actual"], "OSError: connection refused"
            )
