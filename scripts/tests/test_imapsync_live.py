"""Copy a mailbox into REST Mail with imapsync, the tool operators reach for (#734).

Opt-in: it needs the E2E topology running, so it runs only when
RESTMAIL_LIVE_IMAPSYNC is set. It fills a new folder on the reference Dovecot,
copies that account with imapsync over IMAP into REST Mail's gateway, compares
folders, counts, subjects and flags over IMAP, then copies again and expects
nothing added. The account also holds an empty folder: imapsync once reported
success while the destination silently lacked it (#1834), so the proof LISTs the
destination itself rather than trusting imapsync's exit status or report.
"""

import imaplib
import os
import re
import ssl
import subprocess
import sys
import time
import unittest

IMAGE = "gilleslamiral/imapsync:2.326@sha256:ece980cb0fd2806369975d2989a3e8d9fbf28d632cdd45a05961a1e511612ed5"
NETWORK = "testbed_mailnet"
GATEWAY = "rest-mail-imap-gateway"
SOURCE_HOST = os.environ.get("MAIL1_IMAP_IP", "10.99.0.111")
SOURCE_USER = os.environ.get("IMAPSYNC_SOURCE_USER", "alice@mail1.test")
TARGET_USER = os.environ.get("IMAPSYNC_TARGET_USER", "eve@restmail.test")
PASSWORD = os.environ.get("ADMIN_PASSWORD", "password123")
TARGET_PASSWORD = os.environ.get("IMAPSYNC_TARGET_PASSWORD", PASSWORD)


def gateway_address():
    return subprocess.run(
        ["docker", "inspect", "-f",
         '{{(index .NetworkSettings.Networks "%s").IPAddress}}' % NETWORK, GATEWAY],
        check=True, capture_output=True, text=True,
    ).stdout.strip()


def source():
    client = imaplib.IMAP4(SOURCE_HOST, 143, timeout=30)
    client.login(SOURCE_USER, PASSWORD)
    return client


def target(host):
    context = ssl.create_default_context()
    # The topology's certificates come from its own test CA.
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    client = imaplib.IMAP4_SSL(host, 993, ssl_context=context, timeout=30)
    client.login(TARGET_USER, TARGET_PASSWORD)
    return client


LIST_LINE = re.compile(rb'^\((?P<flags>[^)]*)\) (?:NIL|"(?:[^"\\]|\\.)*") (?P<name>.+)$')


def listed(data):
    """The LIST response imaplib returned, as {folder name: set of lowercased attributes}."""
    folders = {}
    for item in data:
        if item is None or item == b"":
            # None: no folders. b"": the rest of a line whose name came as a literal.
            continue
        if isinstance(item, tuple):
            # A name sent as a literal: the line ends {n} and the name follows.
            line, name = item[0], item[1]
            match = LIST_LINE.match(line)
            if match is None:
                raise AssertionError("unparsable LIST line %r" % (line,))
        else:
            match = LIST_LINE.match(item)
            if match is None:
                raise AssertionError("unparsable LIST line %r" % (item,))
            name = match.group("name")
            if name.startswith(b'"') and name.endswith(b'"'):
                name = re.sub(rb'\\(.)', rb'\1', name[1:-1])
        attributes = {flag.lower() for flag in match.group("flags").decode().split()}
        folders[name.decode()] = attributes
    return folders


def folders(client):
    """Every folder the server lists, read with our own LIST."""
    status, data = client.list('""', "*")
    if status != "OK":
        raise AssertionError("LIST failed: %r" % (data,))
    return listed(data)


def message_count(client, folder):
    """How many messages SELECT reports in folder (its EXISTS count)."""
    status, data = client.select('"%s"' % folder, readonly=True)
    if status != "OK":
        raise AssertionError("cannot select %s: %r" % (folder, data))
    count = int(data[0])
    client.close()
    return count


def contents(client, folder):
    """Each message in folder, as {subject: sorted flags that were copied}."""
    status, _ = client.select('"%s"' % folder, readonly=True)
    if status != "OK":
        raise AssertionError("cannot select %s" % folder)
    status, data = client.fetch("1:*", "(FLAGS BODY.PEEK[HEADER.FIELDS (SUBJECT)])")
    if status != "OK":
        raise AssertionError("cannot fetch %s" % folder)
    found = {}
    for item in data:
        if not isinstance(item, tuple):
            continue
        flags = re.search(rb"FLAGS \(([^)]*)\)", item[0])
        subject = re.search(rb"Subject: (.+?)\r?\n", item[1], re.IGNORECASE)
        kept = sorted(flag for flag in (flags.group(1).decode().split() if flags else [])
                      if flag != "\\Recent")
        found[subject.group(1).decode()] = kept
    return found


def imapsync(host, *names):
    chosen = []
    for name in names:
        chosen += ["--folder", name]
    result = subprocess.run(
        ["docker", "run", "--rm", "--network", NETWORK, IMAGE, "imapsync",
         "--host1", SOURCE_HOST, "--port1", "143", "--user1", SOURCE_USER, "--password1", PASSWORD,
         "--host2", host, "--port2", "993", "--ssl2", "--user2", TARGET_USER, "--password2", TARGET_PASSWORD,
         *chosen, "--nolog", "--noreleasecheck"],
        capture_output=True, text=True, timeout=600, check=False,
    )
    if result.returncode != 0:
        raise AssertionError("imapsync exited %d:\n%s" % (result.returncode, result.stdout[-4000:]))
    return result.stdout


@unittest.skipUnless(os.environ.get("RESTMAIL_LIVE_IMAPSYNC"), "set RESTMAIL_LIVE_IMAPSYNC with the topology up")
class ImapsyncCutover(unittest.TestCase):
    def test_a_copy_keeps_what_the_mailbox_held_and_a_second_adds_nothing(self):
        folder = "Cutover%d" % time.time()
        want = {
            "first read": ["\\Seen"],
            "second flagged": ["\\Flagged", "\\Seen"],
            "third unread": [],
            "fourth keywords": ["$Forwarded", "$Junk"],
            "fifth deleted": ["$Forwarded", "\\Deleted", "\\Seen"],
        }
        sender = source()
        sender.create(folder)
        for subject, flags in want.items():
            # imapsync identifies a message by its Message-Id and Received headers, and
            # silently leaves one with neither behind, as real mail never is.
            message = ("From: alice@mail1.test\r\nTo: alice@mail1.test\r\nSubject: %s\r\n"
                       "Message-ID: <%s.%s@mail1.test>\r\n\r\nBody of %s\r\n"
                       % (subject, subject.replace(" ", "-"), folder, subject))
            status, _ = sender.append(folder, "(%s)" % " ".join(flags), None, message.encode())
            self.assertEqual(status, "OK")
        self.addCleanup(sender.logout)
        self.assertEqual(contents(sender, folder), want)
        empty = "Empty%d" % time.time()
        status, _ = sender.create(empty)
        self.assertEqual(status, "OK")
        self.assertNotIn("\\noselect", folders(sender)[empty])
        self.assertEqual(message_count(sender, empty), 0)

        host = gateway_address()
        print(imapsync(host, folder, empty)[-800:], file=sys.stderr)
        receiver = target(host)
        self.addCleanup(receiver.logout)
        self.assertEqual(contents(receiver, folder), want)
        receiver.close()
        self.assert_empty_folder_arrived(receiver, empty)

        print(imapsync(host, folder, empty)[-800:], file=sys.stderr)
        self.assert_empty_folder_arrived(receiver, empty)
        self.assertEqual(contents(receiver, folder), want, "a second copy changed the folder")
        receiver.close()
        status, _ = receiver.select('"%s"' % folder)
        self.assertEqual(status, "OK")
        status, _ = receiver.expunge()
        self.assertEqual(status, "OK")
        remaining = {subject: flags for subject, flags in want.items() if "\\Deleted" not in flags}
        self.assertEqual(contents(receiver, folder), remaining)
        self.assertEqual(contents(sender, folder), want, "destination EXPUNGE changed the source")

    def assert_empty_folder_arrived(self, receiver, empty):
        """The destination itself lists the empty folder as selectable, and it holds nothing."""
        destination = folders(receiver)
        self.assertIn(empty, destination,
                      "imapsync succeeded but the destination does not list the empty folder (#1834)")
        self.assertNotIn("\\noselect", destination[empty])
        self.assertEqual(message_count(receiver, empty), 0)


class ListParsing(unittest.TestCase):
    """listed() reads the LIST shapes servers send, so a missing folder is never a parse slip."""

    def test_quoted_atom_and_literal_names(self):
        data = [
            b'(\\HasNoChildren) "/" "INBOX"',
            b'(\\HasNoChildren \\Noselect) "." Archive',
            b'() NIL "Say \\"hi\\""',
            (b'(\\HasNoChildren) "/" {9}', b'Empty 123'),
            b"",
        ]
        self.assertEqual(listed(data), {
            "INBOX": {"\\hasnochildren"},
            "Archive": {"\\hasnochildren", "\\noselect"},
            'Say "hi"': set(),
            "Empty 123": {"\\hasnochildren"},
        })

    def test_no_folders(self):
        self.assertEqual(listed([None]), {})

    def test_an_unreadable_line_fails_loudly(self):
        with self.assertRaises(AssertionError):
            listed([b"garbage"])


if __name__ == "__main__":
    unittest.main()
