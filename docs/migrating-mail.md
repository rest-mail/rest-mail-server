# Importing historical mail

Create the destination accounts first using [account import](migrating-accounts.md).
Then copy an IMAP source account, or import a mailbox's Maildir or a supported
mbox export, through the public API.
Keep the source server and its archives until you have compared the destination
folders, message counts, flags and original sources.

The commands require an existing destination account ID and `RESTMAIL_API_KEY`
with `mailboxes:write` on its domain or account. A password or app password cannot
authorize this administrative import. Give the key only the destinations needed
for the migration; neither the key nor message contents appear in command output.

## Read-only IMAP source

`import-imap` copies one source account, including all selectable folders, over
implicit TLS. Set `RESTMAIL_IMAP_PASSWORD` from your secret manager without
putting its value in a command argument, URL or shell history. For the destination,
set `RESTMAIL_API_KEY` as described above. A source master-user login can be given
in `--source-user` using that source server's own syntax.

```sh
restmail import-imap --source imaps://old.example.com --source-user alice@example.com --account-id 12 --dry-run
restmail import-imap --source imaps://old.example.com --source-user alice@example.com --server https://mail.example.com --account-id 12
```

The default source port is 993; an explicit `:port` is allowed. Certificate and
hostname verification are mandatory, with TLS 1.2 or newer and the system trust
store. Plaintext IMAP, URL credentials and disabling TLS verification are refused.
This command currently uses a source password/app password; OAuth and STARTTLS
sources are not supported. It does not upload source credentials to REST Mail.

The source is opened with EXAMINE and fetched by UID with BODY.PEEK, so importing
never marks source messages read, edits their flags, expunges or deletes them.
Each folder's UIDNEXT freezes the upper UID bound for that pass. Its original
message octets, INTERNALDATE (including timezone), Seen, Answered, Flagged and
Draft reach the same public import operation as file imports. Recent is a
source-session indicator and is not copied as persistent state. Deleted and
keyword flags cause an explicit refusal rather than losing their meaning.

Source hierarchy delimiters are mapped to `/`. Folder mappings are validated
before any import, and collisions are refused. Non-selectable parents are
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
Folder names and original message octets are preserved. Source symlinks are
refused, including symlinked folders, so a source tree cannot silently import
files outside it.

Supported filename flags are `S` (Seen), `R` (Answered), `F` (Flagged) and
`D` (Draft). `T` (Deleted), `P` (Passed/forwarded) and keyword letters are
refused explicitly. The current IMAP backend cannot restore persistent Deleted
state or arbitrary keywords; importing them as hidden messages or dropping them
would lose migration information. These flags remain part of [#734](https://github.com/rest-mail/rest-mail-server/issues/734).

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

The importer reads Seen from `Status: R` and Answered, Flagged and Draft from
`X-Status: AFT`, following [Dovecot's mbox metadata](https://doc.dovecot.org/main/core/config/mailbox_formats/mbox.html#dovecot-s-metadata).
Deleted state, keywords, metadata-only pseudo-messages and Content-Length-framed
variants are refused. Source headers remain in the imported raw message. Export
format differences are not resolved by deleting or rewriting source data.

## Limits, reruns and cutover

Each request imports one message. The decoded message is limited by
`SMTP_MAX_MESSAGE_SIZE` (10 MiB by default); the general API body limit also
applies to its base64-encoded JSON representation. The CLI defaults to a 10 MiB
source-message limit, adjustable with `--max-message-bytes` up to 1 GiB. Raising
the CLI limit does not raise either server limit or the account's quota.

For Maildir/mbox, `--dry-run` reads and validates source files without an API key
or network calls.
It does not prove server authorization, destination existence or available quota.

Messages commit individually. If a file or request fails, the command exits
nonzero and reports how many imports and skips completed. Earlier successful
imports remain stored. Fix the reported problem and rerun the same command.
`--json` writes one summary on success or one error on failure.

The server deduplicates atomically by account, folder and Message-ID. A matching
copy is skipped, retaining its first imported contents, flags and internal date.
The same Message-ID in another folder or account is independent. Missing or
unusable Message-IDs receive a deterministic identity from the complete raw
message, without inserting that identity into its original headers. Duplicate
detection does not compare or replace an existing Message-ID's contents.

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

Direct IMAP source import is available alongside Maildir/mbox. Persistent Deleted
and keyword restoration, OAuth/STARTTLS source support and the separate imapsync
cutover proof remain outside this implementation. The wider migration outcome
stays tracked in [#734](https://github.com/rest-mail/rest-mail-server/issues/734);
this does not claim its full acceptance criteria are complete.
