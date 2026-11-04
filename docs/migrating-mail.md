# Importing and exporting mail

To move an account's mail out of REST Mail, see
[Exporting an account's mail](#exporting-an-accounts-mail).

Create the destination accounts first using [account import](migrating-accounts.md).
Then copy an IMAP source account, or import a mailbox's Maildir or a supported
mbox export, through the public API, or copy over IMAP with
[imapsync](#copying-with-imapsync).
Keep the source server and its archives until you have compared the destination
folders, message counts, flags and original sources.

The commands require an existing destination account ID and `RESTMAIL_API_KEY`
with `mail:import` on its domain or account. Every new account is granted `mail:import` on
its own mailbox through the revocable `importer` role, and the admin role holds it at its scope. Give the key only the destinations needed
for the migration; neither the key nor message contents appear in command output.

## Read-only IMAP source

`import-imap` copies one source account, including all selectable folders, over
implicit TLS by default, or an explicitly required STARTTLS upgrade. Set
`RESTMAIL_IMAP_PASSWORD` (password or app password), or `RESTMAIL_IMAP_ACCESS_TOKEN`
(an OAuth access token you obtain from the source provider), from your secret manager without
putting its value in a command argument, URL or shell history. For the destination,
set `RESTMAIL_API_KEY` as described above. A source master-user login can be given
in `--source-user` using that source server's own syntax.

```sh
restmail import-imap --source imaps://old.example.com --source-user alice@example.com --account-id 12 --dry-run
restmail import-imap --source imap://old.example.com --starttls --source-user alice@example.com --account-id 12 --dry-run
restmail import-imap --source imaps://old.example.com --source-user alice@example.com --server https://mail.example.com --account-id 12
```

The default source port is 993; an explicit `:port` is allowed. Certificate and
hostname verification are mandatory, with TLS 1.2 or newer and the system trust
store. Plaintext IMAP, URL credentials and disabling TLS verification are refused.
For a source that requires STARTTLS, use `--source imap://old.example.com --starttls`
(default port 143, or an explicit port). STARTTLS must succeed with
certificate/hostname verification before any credentials are sent. A refusal,
invalid certificate, untrusted issuer or canceled upgrade stops the import; the
command never falls back to unprotected authentication. Do not combine
`--starttls` with an `imaps://` source. This
command does not upload source credentials to REST Mail.

For OAuth, set only `RESTMAIL_IMAP_ACCESS_TOKEN` and unset `RESTMAIL_IMAP_PASSWORD`.
Supplying both is a usage error. The command uses the source's advertised
`OAUTHBEARER` mechanism, or `XOAUTH2` if `OAUTHBEARER` is not advertised, only after
verified TLS. It does not fall back to password authentication, run a device flow,
obtain a token, or refresh one. If sign-in fails or the source stops a copy when
the token expires, the command exits with an error and the completed import/skip
counts. Obtain a valid token and rerun the same command to resume; already imported
messages remain stored. Token lifetime and revocation are enforced by the source;
the command does not infer an expiry time from an opaque token.

The source is opened with EXAMINE and fetched by UID with BODY.PEEK, so importing
never marks source messages read, edits their flags, expunges or deletes them.
Each folder's UIDNEXT freezes the upper UID bound for that pass. Its original
message octets, INTERNALDATE (including timezone), Seen, Answered, Flagged and
Draft, Deleted and keywords reach the same public import operation as file imports. Recent is a
source-session indicator and is not copied as persistent state. Deleted messages
retain their flag and use the same IMAP EXPUNGE path as messages marked Deleted
by a client; ordinary message listings hide them until the flag is cleared.
Keywords are preserved case-insensitively (first spelling retained), with at most
100 per message and 255 ASCII bytes per keyword. Each must be an RFC 3501 atom
without a backslash. Invalid metadata is refused clearly, never silently dropped.

Source hierarchy delimiters are mapped to `/`. Every folder is named before
any import, and a folder name never stops the import or leaves mail behind: see
[Folder names that collide or are invalid](#folder-names-that-collide-or-are-invalid). Non-selectable parents are
skipped; their selectable children are still copied. Empty source folders cannot
be created by this message-import operation. The same Message-ID in different
folders remains a separate destination copy, including provider label folders.

One message is buffered at a time, bounded by `--max-message-bytes`; declared
size and actual fetched octets must agree. Missing metadata, source changes that
leave the examined message count incomplete, network or destination failures
stop the command with the completed import/skip counts. Rerun after correcting
the problem; the destination's atomic deduplication retains earlier successes.
Ctrl-C or termination closes the source connection. No source checkpoints or
credentials are stored on disk, and provider diagnostics are not echoed.

For IMAP, `--dry-run` still signs in to and reads the source over TLS, but makes
no destination API calls and needs no destination key. It does not establish
destination authorization, available quota, or successful write acceptance.
The source reader uses the MIT-licensed `github.com/emersion/go-imap/v2` client,
pinned in `go.mod`; it does not replace REST Mail's own IMAP server engine.

## Maildir

Preserve message modification times when making a source copy. Dovecot uses the
message file's `mtime` as IMAP INTERNALDATE, rather than the timestamp embedded in
its filename. See [Dovecot's Maildir timestamp rules](https://doc.dovecot.org/2.4.0/core/config/mailbox/formats/maildir.html#usage-of-timestamps).

```sh
restmail import-maildir --account-id 12 --path ./Maildir --dry-run
restmail import-maildir --server https://mail.example.com --account-id 12 --path ./Maildir
```

The source root must contain `cur` and `new`. Both are read; `tmp` is skipped.
Maildir++ `.Archive.2020` becomes `Archive/2020`, and the root becomes INBOX.
Each `.`-separated level is a [modified UTF-7](https://www.rfc-editor.org/rfc/rfc3501#section-5.1.3)
name, as Dovecot writes it, and is decoded: `.R&AOk-sum&AOk-.&ZcWITA-` becomes
`Résumé/旅行`. A level with octets outside US-ASCII cannot be modified UTF-7, so
it is kept as the UTF-8 name it already is. A level that is neither, such as
`.Tom & Jerry` (Dovecot writes `Tom & Jerry` as `.Tom &- Jerry`), is still
imported, under its literal text, and the rename is reported. Original message
octets are preserved. Source symlinks are
refused, including symlinked folders, so a source tree cannot silently import
files outside it.

### Folder names that collide or are invalid

`import-maildir` and `import-imap` bring every message across; a folder name
never stops the import or skips mail. All source folders are named before the
first message is sent:

- A name that is not a legal REST Mail folder name is changed as little as
  possible: invalid UTF-8 and control characters become `�`, `..` becomes
  `. .`, surrounding spaces are dropped and an overlong name is shortened.
- When two source folders map to the same name, the one whose own name maps
  cleanly keeps it, otherwise the first in source order. The other is imported
  as `<name> (2)`, then `(3)` and so on, skipping any name another source
  folder already has. `.Tom &- Jerry` becomes `Tom & Jerry` and `.Tom & Jerry`
  becomes `Tom & Jerry (2)`.

Each rename is a line on standard output (`Source folder ".Tom & Jerry" is
imported as "Tom & Jerry (2)": …`), or an entry of `renamed_folders` (`source`,
`folder`, `reason`) in the `--json` summary. The names depend only on the
source, so a rerun maps every folder the same way and resumes into it. Source
folders are imported into destination folders of the same name that already
exist, such as `INBOX`, `Sent` or a folder an earlier run created, alongside
the mail already there. `import-mbox --folder` writes to the folder you name
and refuses a name that is not legal. Outside import, creating a folder that
already exists is an error.

Supported filename flags are `S` (Seen), `R` (Answered), `F` (Flagged),
`D` (Draft) and `T` (Deleted). Lowercase keyword letters `a`–`z` are restored
using that folder's `dovecot-keywords` file, whose lines are a zero-based index
and keyword name (for example, `0 $Forwarded`). Each folder has its own mapping;
missing, invalid, duplicate or oversized mappings are refused. The mapping file
is bounded to 64 KiB. `P` (Passed/forwarded) remains unsupported because it has
no unambiguous IMAP system flag; export a named keyword instead.

## Mbox

Choose the export's escaping format explicitly. The command does not guess:

```sh
restmail import-mbox --account-id 12 --path ./archive.mbox --mbox-format mboxrd --dry-run
restmail import-mbox --server https://mail.example.com --account-id 12 --path ./archive.mbox --folder Archive --mbox-format mboxrd
```

`rfc4155` keeps body quoting unchanged; `mboxrd` removes one `>` from body lines
matching `>+From `; `mboxo` removes it only from `>From ` lines. These formats can
represent quoted body lines differently, so use the format of the source export.
Message boundaries require a complete From separator with a recognized timestamp;
ordinary body text starting with `From ` is retained. The separator's date becomes
INTERNALDATE; timestamps without a zone use UTC, as in [RFC4155's default format](https://www.rfc-editor.org/rfc/rfc4155#appendix-A).

The importer reads Seen from `Status: R`, Answered, Flagged, Draft and Deleted
from `X-Status: AFTD`, and keywords from every `X-Keywords` header, following [Dovecot's mbox metadata](https://doc.dovecot.org/main/core/config/mailbox_formats/mbox.html#dovecot-s-metadata).
Metadata-only pseudo-messages and Content-Length-framed variants are refused.
Once read into flags, Dovecot's metadata fields (`Status`, `X-Status`,
`X-Keywords`, `X-UID` and `X-IMAPbase`) are removed from the stored message, as
Dovecot itself leaves them out when it serves the message over IMAP. Every other
octet is kept, so a flag-only edit on the source does not make the message
different, and the same message imported from mbox or over IMAP is the same
message. Export format differences are not resolved by deleting or rewriting
source data.

Every occurrence of source metadata is checked. Supported flags across repeated
`Status` and `X-Status` headers are combined; a later header cannot hide invalid
flags, keywords, pseudo-message metadata or Content-Length framing.

## Copying with imapsync

[imapsync](https://imapsync.lamiral.info/) copies one account from any IMAP
server to another, and REST Mail's IMAP service accepts it like any mail client.
It is an alternative to `import-imap`, not a part of it: it signs in to the
destination over IMAP instead of using the import API, and behaves differently
on reruns (below). Which route a migration takes is a decision for the person
running it; this section only shows how to run imapsync safely. The options
here were checked against imapsync's own documentation and source (2.314), and
a trial migration from Dovecot used 2.326. The E2E job's
`scripts/tests/test_imapsync_live.py` runs imapsync inside the test topology
with test passwords and an unverified test certificate: do not copy that recipe.

Before the first run:

- **The destination account exists.** Create it with
  [account import](migrating-accounts.md), so it keeps its old password. imapsync
  signs in as that account over IMAP on port 993. An account with two-factor
  authentication, or still in setup, cannot sign in to IMAP with its password:
  give imapsync an [app password](MANUAL.md#33-auth) for it instead.
- **imapsync can sign in to the source.** Use the user's own password, or the
  source's administrator or master login with `--authuser1` (imapsync then
  signs in as that administrator and opens `--user1`'s mailbox), if the
  source supports one.
- **The passwords are in files, not on the command line.** Each file holds the
  password on its first line. Write them from your secret manager, readable only
  by you, and delete them when the migration ends:

  ```sh
  umask 077
  mkdir -p ~/migration
  # write the source password to ~/migration/alice.old and the
  # destination password to ~/migration/alice.new, without echoing them
  ```

  `--passfile1` and `--passfile2` read them, so the passwords do not appear in
  `ps`, shell history or imapsync's log.
- **The certificates are checked.** imapsync does not verify the server's
  certificate unless told to: its default is `SSL_verify_mode=0`, and it says so
  when it starts. Pass `--sslargs1 SSL_verify_mode=1` and
  `--sslargs2 SSL_verify_mode=1` every time. With implicit TLS (`--ssl1`,
  `--ssl2`, port 993) imapsync then also checks that the certificate names the
  host, so give `--host1` and `--host2` as the names on the certificates, not
  IP addresses. A server whose certificate comes from a private CA needs that CA:
  `--sslargs1 SSL_ca_file=/path/to/old-ca.pem`. If the old server offers only
  STARTTLS on port 143, use `--tls1` instead of `--ssl1` and add
  `--sslargs1 SSL_verifycn_scheme=imap --sslargs1 SSL_verifycn_name=old.example.com`,
  because imapsync's STARTTLS path does not check the name by itself.

Check that both sign-ins work, then do a dry run, then copy:

```sh
cd ~/migration
imapsync --justlogin \
  --host1 old.example.com --ssl1 --sslargs1 SSL_verify_mode=1 \
  --user1 alice@example.com --passfile1 alice.old \
  --host2 mail.example.com --ssl2 --sslargs2 SSL_verify_mode=1 \
  --user2 alice@example.com --passfile2 alice.new \
  --noreleasecheck --logdir ~/migration/logs

# the same options with --dry instead of --justlogin: shows what would be copied
# the same options with neither: copies
```

`--noreleasecheck` keeps imapsync from fetching its latest version number from
its website during the migration. Logging stays on: the log in `--logdir`
records what was copied, with passwords shown as `MASKED`. Keep it with the
password files and delete it with them. With `--dry`, a folder that does not
yet exist on REST Mail is listed but its messages are not counted; the real run
creates it.

imapsync copies every folder, each message's original octets, its internal date
and its flags, including Deleted and keywords. It does not delete anything on
either side, and its exit status is 0 when it found no errors. That status is
not enough to accept a copy: in the trial migration, imapsync reported `4/4`
folders synced and `0` errors while REST Mail had not kept an empty folder
([#1834](https://github.com/rest-mail/rest-mail-server/issues/1834)). Until that
is fixed, an empty folder exists on REST Mail only once a message is in it.

Run the same command again with nothing changed on the source: it should say
`Messages transferred : 0`. Run it again at the cutover to copy what arrived
since. On each rerun imapsync recognises messages it already copied by their
`Message-ID` and `Received` headers, and by default copies their current flags
again (`--noresyncflags` turns that off). That differs from the import API,
where the first imported copy keeps its flags and a rerun changes nothing about
it. A message deleted or moved on the source after it was copied stays on REST
Mail under both routes. A message with neither a `Message-ID` nor a `Received`
header is not copied by imapsync, which is one more reason to count.

## Checking a copy

Compare the two accounts yourself after every pass, whichever route copied the
mail. This script signs in to both over implicit TLS on port 993, verifying the
certificate and host name against the system's trusted CAs or a CA file given
after the other arguments, and reads both read-only.
For every selectable folder on the old server it checks that REST Mail has that
folder, then compares each message's Message-ID, flags, internal date and size
(which stands in for its octets and attachments). Save it as
`compare-mailboxes.py`:

```python
import imaplib, re, ssl, sys, time

LIST = re.compile(rb'\((?P<flags>[^)]*)\) (?:"(?P<delim>[^"]*)"|NIL) (?P<name>.+)$')


def connect(host, user, passfile, cafile=None):
    context = ssl.create_default_context(cafile=cafile)  # verifies certificate and name
    client = imaplib.IMAP4_SSL(host, 993, ssl_context=context, timeout=60)
    client.login(user, open(passfile).readline().rstrip("\r\n"))
    return client


def folders(client):
    """{name with "/" as delimiter: name as the server spells it}, selectable only."""
    found = {}
    for line in client.list()[1]:
        match = LIST.match(line)
        if not match or b"\\noselect" in match["flags"].lower():
            continue
        name = match["name"].decode()
        if name.startswith('"'):
            name = name[1:-1].replace('\\"', '"').replace("\\\\", "\\")
        delim = (match["delim"] or b"/").decode()
        found[name.replace(delim, "/")] = name
    return found


def messages(client, name):
    """Sorted (Message-ID, flags, INTERNALDATE, size) of every message in a folder."""
    status, data = client.select('"%s"' % name.replace("\\", "\\\\").replace('"', '\\"'), readonly=True)
    if status != "OK":
        raise SystemExit("cannot open %r" % name)
    if int(data[0]) == 0:
        return []
    found = []
    for item in client.fetch("1:*", "(FLAGS INTERNALDATE RFC822.SIZE BODY.PEEK[HEADER.FIELDS (MESSAGE-ID)])")[1]:
        if not isinstance(item, tuple):
            continue
        flags = re.search(rb"FLAGS \(([^)]*)\)", item[0]).group(1).decode().split()
        date = time.mktime(imaplib.Internaldate2tuple(item[0]))  # the instant, any zone
        size = int(re.search(rb"RFC822\.SIZE (\d+)", item[0]).group(1))
        found.append((item[1].decode(errors="replace").strip(),
                      " ".join(sorted(f.lower() for f in flags if f != "\\Recent")), date, size))
    return sorted(found)


args = sys.argv[1:] + ["", ""]
old = connect(*args[0:3], args[6] or None)
new = connect(*args[3:6], args[7] or None)
old_folders, new_folders = folders(old), folders(new)
problems = 0
for folder in sorted(old_folders):
    if folder not in new_folders:
        print("MISSING FOLDER %s" % folder)
        problems += 1
        continue
    a, b = messages(old, old_folders[folder]), messages(new, new_folders[folder])
    print("%-40s %6d %6d %s" % (folder, len(a), len(b), "ok" if a == b else "DIFFERENT"))
    problems += a != b
print("%d problem(s)" % problems)
sys.exit(1 if problems else 0)
```

```sh
python3 compare-mailboxes.py old.example.com alice@example.com alice.old \
  mail.example.com alice@example.com alice.new [old-ca.pem [new-ca.pem]]
```

It prints each folder with both message counts, `MISSING FOLDER` for a folder
REST Mail does not have, including an empty one, and `DIFFERENT` when the
messages differ, and exits 1 if anything is wrong. Folder names are printed as
the servers send them, in modified UTF-7 (`R&AOk-sum&AOk-` is `Résumé`), with
either side's hierarchy separator turned into `/`. A folder whose name contains
the other server's separator compares as a different name; check it by hand.
If a size differs, compare that message's source on both sides.

## Cutover checklist

1. Every destination account exists and signs in, and a first copy has been
   checked with the script above.
2. Stop new mail changing the old account: point the MX at REST Mail
   ([the migration chapter](email-explained/landscape/migrating.md#3-cut-over))
   with the old server forwarding late arrivals, or stop delivery on the old
   server so mail waits in senders' queues. Ask users to stop using the old
   server, or turn off their sign-in there.
3. Run the final pass: the same import or imapsync command again.
4. Check every account again with the script: every folder present, including
   empty and non-ASCII ones, the same count in every folder, and no message
   `DIFFERENT`. An exit status of 0 from the copy is not this check.
5. Move the mail apps to REST Mail. Keep the old server, its archives and the
   imapsync logs until users confirm nothing is missing.

## Limits, reruns and cutover

Each request imports one message. The decoded message is limited by
`SMTP_MAX_MESSAGE_SIZE` (10 MiB by default); the general API body limit also
applies to its base64-encoded JSON representation. The CLI defaults to a 10 MiB
source-message limit, adjustable with `--max-message-bytes` up to 1 GiB. Raising
the CLI limit does not raise either server limit or the account's quota.
For mbox, the From envelope separator is framing and does not count toward the
source-message limit.

For Maildir/mbox, `--dry-run` reads and validates source files without an API key
or network calls.
It does not prove server authorization, destination existence or available quota.

Messages commit individually. If a file or request fails, the command exits
nonzero and reports how many imports and skips completed. Earlier successful
imports remain stored. Fix the reported problem and rerun the same command.
`--json` writes one summary on success or one error on failure.

The server deduplicates atomically by account, folder, Message-ID and message
octets. A message already there with the same Message-ID and the same octets is
skipped, retaining its first imported flags and internal date. The same
Message-ID with different octets (an edited draft, a reused ID) is stored as
another message; existing contents are never replaced. Flags are not part of the
octets: Maildir keeps them in the filename, IMAP apart from the message, and
mbox metadata fields are removed as described above, so a flag-only edit on the
source is skipped on a rerun. The same Message-ID in another folder or account
is independent. Missing or unusable Message-IDs receive a deterministic identity
from the complete raw message, without inserting that identity into its original
headers.

Import once while both servers are available, then stop source-side changes for
the final pass and repeat the import before moving clients. A rerun adds new
Message-IDs; it does not reconcile later flag edits, moves or deletions of copies
already imported. Compare the destination inventory
([Checking a copy](#checking-a-copy), [Cutover checklist](#cutover-checklist))
and retain source archives before retiring the old server.

## Public operation

`POST /api/v1/accounts/{id}/messages/import` accepts `raw_message` as base64,
`folder` (default INBOX), supported IMAP `flags`, and required RFC3339
`internal_date`. Its 200 response contains `data.id`, `data.message_id`,
`data.folder` and `data.imported`; `false` identifies an existing copy.

Imports preserve original octets and attachment contents without running spam,
Sieve, forwarding, vacation, notification or other delivery behavior. Successful
imports and skips have a transactional `messages.import` audit entry; the admin
request audit also records refusals without message contents. Quota and message
metadata commit together; storage failures do not acknowledge a partial import.
Attachment objects live outside the database transaction. A refused transaction
may leave an unreferenced content-addressed object; it does not leave a message
or attachment reference, and a rerun reuses the same object content.

Direct IMAP source import, including OAuth, is available alongside Maildir/mbox,
and all three restore Deleted and keywords. Copying with imapsync also works
([Copying with imapsync](#copying-with-imapsync)): the E2E job copies a folder
from Dovecot with it and compares folders, counts, subjects and flags
(`scripts/tests/test_imapsync_live.py`). The wider migration outcome
stays tracked in [#734](https://github.com/rest-mail/rest-mail-server/issues/734);
this does not claim its full acceptance criteria are complete.

## Exporting an account's mail

`export-maildir` writes one account's mail as a Maildir, through the public API,
in exactly the layout `import-maildir` reads. It imports into another REST Mail
server, or into Dovecot or any other Maildir++ server.

```sh
restmail export-maildir --server https://mail.example.com --account-id 12 --path ./Maildir
restmail import-maildir --server https://new.example.com --account-id 7 --path ./Maildir
```

`RESTMAIL_API_KEY` needs `mail:read` on the account or its domain. Every account
holds `mail:read` on its own mailbox, and the admin role holds it at its scope.
The command reads only existing routes, each with its usual `mail:read` check and
request audit: `GET /api/v1/accounts/{id}/folders`,
`GET /api/v1/accounts/{id}/folders/{folder}/messages?include_imap_deleted=true`
and `GET /api/v1/messages/{id}/raw`. It adds no audit entry of its own.

INBOX becomes the Maildir root and every other folder a Maildir++ folder, `/`
becoming `.` and each level written in modified UTF-7, as Dovecot names them:
`Archive/2020` is `.Archive.2020` and `Résumé/旅行` is `.R&AOk-sum&AOk-.&ZcWITA-`. Empty folders get their
directories too, but an import cannot create a folder without messages. A
folder whose name contains `.` is refused before anything is written, since
Maildir++ would read it back as a subfolder; rename it first.

Each message is its stored source, byte for byte, in the folder's `cur/`. Its
flags are the filename's `:2,` letters: `S` Seen, `R` Answered, `F` Flagged
(flagged or starred), `D` Draft and `T` Deleted. Keywords are letters `a`–`z`,
mapped by that folder's `dovecot-keywords` file; a folder with more than 26
keywords is refused, because Maildir cannot name more. INTERNALDATE is the
file's modification time. Keep modification times when copying the export.

Messages are downloaded one at a time into the folder's `tmp/`, synced, checked
against the size the server lists, and only then renamed into `cur/`, so no
reader sees a half-written message and no whole message or folder is held in
memory. A message the server keeps no source for stops the export rather than
being written differently. Ctrl-C stops it and removes the unfinished message.

The destination is created when missing. One that already holds files is
refused unless `--allow-non-empty` is given; the export then writes beside them,
reusing the keyword letters of an existing `dovecot-keywords` file. A message
exported again gets the same filename, so rerunning into the same directory
replaces it, unless its flags changed in between, in which case both copies
remain. `--max-message-bytes` (default and maximum 1 GiB) refuses larger
messages. `--json` writes one summary (`folders`, `messages`, `bytes`) or one
error.

Messages in a snoozed thread are not listed by the API until the snooze ends,
so the export does not contain them
([#1599](https://github.com/rest-mail/rest-mail-server/issues/1599)). Contacts,
calendars and settings are not part of this export
([#1577](https://github.com/rest-mail/rest-mail-server/issues/1577)).
