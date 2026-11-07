"""Copy a mailbox into REST Mail with imapsync, the tool operators reach for (#734).

Opt-in: it needs the E2E topology running, so it runs only when
RESTMAIL_LIVE_IMAPSYNC is set. It fills a new folder on the reference Dovecot,
copies that account with imapsync over IMAP into REST Mail's gateway, compares
folders, counts, subjects and flags over IMAP, then copies again and expects
nothing added.
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


def imapsync(host, folder):
    result = subprocess.run(
        ["docker", "run", "--rm", "--network", NETWORK, IMAGE, "imapsync",
         "--host1", SOURCE_HOST, "--port1", "143", "--user1", SOURCE_USER, "--password1", PASSWORD,
         "--host2", host, "--port2", "993", "--ssl2", "--user2", TARGET_USER, "--password2", TARGET_PASSWORD,
         "--folder", folder, "--nolog", "--noreleasecheck"],
        capture_output=True, text=True, timeout=600, check=False,
    )
    if result.returncode != 0:
        raise AssertionError("imapsync exited %d:\n%s" % (result.returncode, result.stdout[-4000:]))
    return result.stdout


@unittest.skipUnless(os.environ.get("RESTMAIL_LIVE_IMAPSYNC"), "set RESTMAIL_LIVE_IMAPSYNC with the topology up")
class ImapsyncCutover(unittest.TestCase):
    def test_create_then_append_literals_preserves_flags_and_internal_dates(self):
        receiver = target(gateway_address())
        self.addCleanup(receiver.logout)
        folder = "AppendCutover%d" % time.time_ns()
        status, response = receiver.status(folder, "(MESSAGES)")
        self.assertEqual(status, "NO", response)
        self.assertIn(b"[NONEXISTENT]", response[0])
        for readonly in (False, True):
            status, response = receiver.select(folder, readonly=readonly)
            self.assertEqual(status, "NO", response)
            self.assertIn(b"[NONEXISTENT]", response[0])
        status, response = receiver.create(folder)
        self.assertEqual(status, "OK", response)
        status, response = receiver.select(folder)
        self.assertEqual((status, response), ("OK", [b"0"]))
        status, response = receiver.status(folder, "(MESSAGES)")
        self.assertEqual(status, "OK", response)
        self.assertIn(b"(MESSAGES 0)", response[0])
        want = {}
        for date in (None, '"07-Nov-2020 15:01:32 +0000"'):
            for flags in (None, [], ["\\Seen"], ["\\Flagged", "\\Seen"],
                          ["$Forwarded", "$Junk"], ["\\Deleted", "\\Seen", "$Forwarded"]):
                with self.subTest(date=date, flags=flags):
                    subject = "append-%d" % (len(want) + 1)
                    message = ("From: alice@mail1.test\r\nTo: %s\r\nSubject: %s\r\n"
                               "Message-ID: <%s.%s@mail1.test>\r\n\r\nLiteral body\r\n"
                               % (TARGET_USER, subject, subject, folder)).encode()
                    flag_list = None if flags is None else "(%s)" % " ".join(flags)
                    status, response = receiver.append(folder, flag_list, date, message)
                    self.assertEqual(status, "OK", response)
                    want[subject] = sorted(flags or [])
                    self.assertEqual(contents(receiver, folder), want)
                    if date is not None:
                        status, response = receiver.fetch(str(len(want)), "(INTERNALDATE)")
                        self.assertEqual(status, "OK", response)
                        self.assertIn(b'INTERNALDATE "07-Nov-2020 15:01:32 +0000"', response[0])

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

        host = gateway_address()
        print(imapsync(host, folder)[-800:], file=sys.stderr)
        receiver = target(host)
        self.addCleanup(receiver.logout)
        self.assertEqual(contents(receiver, folder), want)

        print(imapsync(host, folder)[-800:], file=sys.stderr)
        receiver.close()
        self.assertEqual(contents(receiver, folder), want, "a second copy changed the folder")
        receiver.close()
        status, _ = receiver.select('"%s"' % folder)
        self.assertEqual(status, "OK")
        status, _ = receiver.expunge()
        self.assertEqual(status, "OK")
        remaining = {subject: flags for subject, flags in want.items() if "\\Deleted" not in flags}
        self.assertEqual(contents(receiver, folder), remaining)
        self.assertEqual(contents(sender, folder), want, "destination EXPUNGE changed the source")


if __name__ == "__main__":
    unittest.main()
