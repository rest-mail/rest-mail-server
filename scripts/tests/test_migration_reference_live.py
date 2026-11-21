import email.utils
import hashlib
import imaplib
import json
import os
from pathlib import Path
import re
import ssl
import subprocess
import unittest
import urllib.parse
import uuid


def imaps_endpoint(url):
    parsed = urllib.parse.urlsplit(url)
    if (parsed.scheme != "imaps" or not parsed.hostname or parsed.username is not None
            or parsed.path not in ("", "/") or parsed.query or parsed.fragment):
        raise ValueError("give an imaps://host[:port] endpoint without credentials")
    return parsed.hostname, parsed.port or 993


def login(endpoint, address, password):
    tls = ssl.create_default_context()
    tls.minimum_version = ssl.TLSVersion.TLSv1_2
    connection = imaplib.IMAP4_SSL(*endpoint, ssl_context=tls, timeout=30)
    try:
        connection.login(address, password)
    except Exception:
        connection.shutdown()
        raise
    return connection


def snapshot(endpoint, address, password):
    folders = {}
    with login(endpoint, address, password) as connection:
        status, listed = connection.list()
        if status != "OK":
            raise AssertionError("reference LIST failed")
        for line in listed:
            if not isinstance(line, bytes):
                raise AssertionError("the fixture needs simple ASCII LIST responses")
            fields = re.fullmatch(rb'\(([^)]*)\) (?:"[^"\r\n]*"|NIL) "?([A-Za-z0-9_-]+)"?', line)
            if fields is None:
                raise AssertionError("the fixture needs flat ASCII folders (letters, digits, _ and -)")
            if b"\\noselect" in fields[1].lower().split():
                continue
            name = fields[2].decode("ascii")
            status, count = connection.select(name, readonly=True)
            if status != "OK":
                raise AssertionError("reference EXAMINE failed")
            if count == [b"0"]:
                continue
            status, fetched = connection.fetch("1:*", "(FLAGS INTERNALDATE BODY.PEEK[])")
            if status != "OK":
                raise AssertionError("reference FETCH failed")
            messages = []
            for item in fetched:
                if not isinstance(item, tuple):
                    continue
                prefix, raw = item
                flags = tuple(sorted(flag.decode("ascii").lower() for flag in imaplib.ParseFlags(prefix)
                                     if flag.lower() != b"\\recent"))
                value = re.search(rb'INTERNALDATE "([^"]+)"', prefix)
                if value is None or not raw:
                    raise AssertionError("reference FETCH omitted INTERNALDATE or message octets")
                received = email.utils.parsedate_to_datetime(value[1].decode("ascii"))
                messages.append((hashlib.sha256(raw).hexdigest(), flags, int(received.timestamp())))
            if len(messages) != int(count[0]):
                raise AssertionError("reference FETCH did not return all selected messages")
            folders[name] = sorted(messages)
    return folders


@unittest.skipUnless(os.environ.get("RESTMAIL_REFERENCE_MIGRATION") == "1",
                     "requires an isolated Dovecot fixture and a REST Mail test installation")
class ReferenceMigration(unittest.TestCase):
    def test_ac05_published_commands_against_dovecot(self):
        def setting(name):
            key = "MIGRATION_PROOF_" + name
            value = os.environ.get(key)
            if not value:
                self.fail("set " + key + " as documented in docs/migrating-mail.md")
            return value

        cli = setting("CLI")
        server = setting("API_URL")
        source_url = setting("SOURCE_IMAPS")
        source, target = imaps_endpoint(source_url), imaps_endpoint(setting("TARGET_IMAPS"))
        self.assertNotEqual(source, target, "use two separate test installations")
        source_revision, target_revision = setting("SOURCE_REVISION"), setting("TARGET_REVISION")
        address, password = setting("ADDRESS"), setting("PASSWORD")
        session, key = setting("ADMIN_SESSION"), setting("IMPORT_KEY")
        self.assertNotEqual(session, key, "use a human session and a separate scoped import key")
        passwd_file = Path(setting("PASSWD_FILE"))
        lines = [line for line in passwd_file.read_text().splitlines() if line and not line.startswith("#")]
        if len(lines) != 1 or lines[0].split(":", 2)[0] != address or ":" not in lines[0]:
            self.fail("passwd-file must contain exactly the isolated address's actual Dovecot record")
        source_hash = lines[0].split(":", 2)[1]
        self.assertTrue(source_hash, "Dovecot record has no hash")
        wrong_password = uuid.uuid4().hex

        def command(arguments, expect=0, token=None, source_password=None):
            env = {name: value for name, value in os.environ.items()
                   if not name.startswith("MIGRATION_PROOF_") and name not in {
                       "RESTMAIL_API_KEY", "RESTMAIL_IMAP_PASSWORD", "RESTMAIL_IMAP_ACCESS_TOKEN"}}
            if token is not None:
                env["RESTMAIL_API_KEY"] = token
            if source_password is not None:
                env["RESTMAIL_IMAP_PASSWORD"] = source_password
            result = subprocess.run([cli, *arguments], env=env, capture_output=True, text=True, timeout=600)
            if any(secret in result.stdout or secret in result.stderr
                   for secret in (session, key, source_hash, password, wrong_password)):
                self.fail("CLI output exposed a migration credential")
            self.assertEqual(result.returncode, expect, f"{arguments[0]} returned an unexpected exit status")
            return result

        def record(output):
            try:
                return json.loads(output)
            except ValueError:
                self.fail("CLI did not write its documented JSON receipt")

        version = command(["version"]).stdout.strip()
        account_args = ["import-passwd-file", "--server", server, "--json", str(passwd_file)]
        source_before = snapshot(source, address, password)
        self.assertGreaterEqual(len(source_before), 2, "seed two nonempty Dovecot folders")
        self.assertIn("INBOX", source_before)
        flags = {flag for messages in source_before.values() for _, item_flags, _ in messages for flag in item_flags}
        self.assertTrue({r"\seen", r"\deleted", "$forwarded"} <= flags,
                        "seed Seen, Deleted and $Forwarded in the Dovecot source fixture")
        total = sum(map(len, source_before.values()))
        with self.assertRaises(imaplib.IMAP4.error):
            login(source, address, wrong_password)

        denied = record(command(account_args, expect=1, token=key).stdout)
        self.assertEqual(len(denied.get("created", [])), 0)
        self.assertEqual(len(denied.get("failed", [])), 1)
        self.assertIn("HTTP 403", denied["failed"][0]["reason"])
        created = record(command(account_args, token=session).stdout)
        self.assertEqual(len(created.get("created", [])), 1, "target must not already have the fixture account")
        account = created["created"][0]["id"]
        self.assertEqual(snapshot(target, address, password), {}, "old password must open the empty target")
        existing = record(command(account_args, token=session).stdout)
        self.assertEqual([item["address"] for item in existing.get("existing", [])], [address])

        args = ["import-imap", "--source", source_url, "--source-user", address,
                "--account-id", str(account), "--server", server, "--json"]
        refusal = command(args, expect=1, token=key, source_password=wrong_password)
        self.assertEqual(refusal.stdout, "")
        error = record(refusal.stderr)["error"]
        self.assertEqual(error["code"], "import_failed")
        self.assertIn("IMAP source sign-in failed", error["message"])
        self.assertIn("completed 0 imports, skipped 0 existing copies", error["message"])
        dry = record(command([*args, "--dry-run"], source_password=password).stdout)
        self.assertEqual((dry["messages"], dry["imported"], dry["skipped"], dry["dry_run"]), (total, 0, 0, True))
        self.assertEqual(snapshot(target, address, password), {}, "failed and dry runs must not write")

        first = record(command(args, token=key, source_password=password).stdout)
        self.assertEqual((first["messages"], first["imported"], first["skipped"]), (total, total, 0))
        after = snapshot(target, address, password)
        self.assertTrue(after == source_before, "independent IMAP client found differing folders, bytes, flags or dates")
        repeat = record(command(args, token=key, source_password=password).stdout)
        self.assertEqual((repeat["messages"], repeat["imported"], repeat["skipped"]), (total, 0, total))
        self.assertTrue(snapshot(source, address, password) == source_before, "source changed during read-only import")
        self.assertTrue(snapshot(target, address, password) == after, "rerun changed the target inventory")
        print(json.dumps({"source_revision": source_revision, "target_revision": target_revision,
                          "cli_version": version, "cli_sha256": hashlib.sha256(Path(cli).read_bytes()).hexdigest(),
                          "source_inventory_sha256": hashlib.sha256(json.dumps(source_before, sort_keys=True).encode()).hexdigest(),
                          "messages": total, "folders": len(after), "first_imported": first["imported"],
                          "rerun_skipped": repeat["skipped"], "bad_source_password_exit": refusal.returncode,
                          "independent_oracle": "Python imaplib"}, sort_keys=True))
