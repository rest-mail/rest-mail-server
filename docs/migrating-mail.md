# Importing and exporting mail

To move an account's mail out of REST Mail, see
[Exporting an account's mail](#exporting-an-accounts-mail).

Create the destination accounts first using [account import](migrating-accounts.md).
Then copy an IMAP source account, or import a mailbox's Maildir or a supported
mbox export, through the public API.
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
already imported. Compare the destination inventory and retain source archives
before retiring the old server.

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
and all three restore Deleted and keywords. Copying with imapsync also works: the E2E job copies a folder
from Dovecot with it and compares folders, counts, subjects and flags
(`scripts/tests/test_imapsync_live.py`). Over IMAP, APPEND and COPY always add a
message, as the protocol requires, so it is imapsync's own comparison of Message-Id
and Received headers that makes a second imapsync run add nothing. The wider migration outcome
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
`GET /api/v1/accounts/{id}/folders/{folder}/messages?include_imap_deleted=true&include_snoozed=true`
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

The messages of a snoozed thread are exported like any others; the snooze itself
is not kept, so the thread is back in its folder after an import. Contacts,
calendars and settings are not part of this export
([#1577](https://github.com/rest-mail/rest-mail-server/issues/1577)).
