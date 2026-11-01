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
                      if flag in ("\\Seen", "\\Flagged", "\\Answered"))
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
    def test_a_copy_keeps_what_the_mailbox_held_and_a_second_adds_nothing(self):
        folder = "Cutover%d" % time.time()
        want = {
            "first read": ["\\Seen"],
            "second flagged": ["\\Flagged", "\\Seen"],
            "third unread": [],
        }
        sender = source()
        sender.create(folder)
        for subject, flags in want.items():
            message = "From: alice@mail1.test\r\nTo: alice@mail1.test\r\nSubject: %s\r\n\r\nBody of %s\r\n" % (subject, subject)
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


if __name__ == "__main__":
    unittest.main()
