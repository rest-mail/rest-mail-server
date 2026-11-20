# Exporting contacts and calendar invitations

These commands complement `restmail export-maildir` and `restmail export-settings`:

```sh
restmail export-contacts --server https://mail.example.com --account-id 12 --path ./contacts.vcf
restmail export-calendar --server https://mail.example.com --account-id 12 --path ./calendar
```

Both use `RESTMAIL_API_KEY` and only the public API. The key needs `mail:read`
and `mail:export` on the account. Every account holds both on its own mailbox,
and the `owner` role holds both on the accounts it owns. A grant to read
someone else's mail does not include `mail:export`, and the admin role does not
hold it: an administrator exporting another account needs a role that names it
(see `docs/MANUAL.md`). A different account's key, or a key without `mail:read`
or `mail:export`, cannot export the data. HTTPS is required except on localhost. Redirects are
not followed, and credentials and exported content are never printed.

## Contacts

`export-contacts` reads `GET /api/v1/accounts/{id}/contacts` in pages of 100,
writing each contact immediately rather than collecting the address book.
The result is a UTF-8 vCard 4.0 file with CRLF line endings and folded lines.
Each contact has an `EMAIL`, an `FN` (the email address when its stored name is
empty), and a `UID` naming its original server, account and contact ID.

Additional stored information is retained in extension properties:

| Property | Stored field |
| --- | --- |
| `X-RESTMAIL-TRUST-LEVEL` | `trust_level` (`auto`, `trusted`, `blocked`) |
| `X-RESTMAIL-SOURCE` | `source` (`sent`, `manual`, `import`) |
| `X-RESTMAIL-CREATED-AT` | `created_at`, UTC RFC 3339 with fractional seconds |
| `X-RESTMAIL-UPDATED-AT` | `updated_at`, UTC RFC 3339 with fractional seconds |
| `REV` | `updated_at`, UTC vCard revision timestamp |

The database IDs are source identifiers, not IDs to restore on another server.
The derived usual-reply-time estimate is not stored contact data and is omitted.
Other address books may ignore REST Mail extension properties.

The destination must not exist unless `--overwrite` is given. A temporary file
beside the destination is synced and closed before publication, with mode 0600.
A failed export does not replace an existing destination. An empty address book
produces an empty file.

`POST /api/v1/accounts/{id}/contacts/import` takes this file with
`Content-Type: text/vcard`, on this server or another, and restores the name,
source and both timestamps for addresses the account does not already have. A
contact it already has is skipped and left unchanged.

The trust level is restored only when the request asks for it with
`?restore_trust_level=true`, and only for a caller holding `mail:write` on the
account, which is what setting a trust level directly through the contacts API
takes. Without it every imported contact is `auto`, whatever the file says, so
opening a file someone sent cannot trust or block a sender. Restoring your own
export to another server:

```sh
curl -X POST -H "Authorization: Bearer $RESTMAIL_API_KEY" \
  -H 'Content-Type: text/vcard' --data-binary @contacts.vcf \
  "https://mail.example.com/api/v1/accounts/12/contacts/import?restore_trust_level=true"
```

The same route reads vCard 2.1, 3.0 and 4.0 files from other address books:
each `EMAIL` becomes a contact named by the card's first `FN`, or by its first
`N` (prefixes, given, additional and family names, then suffixes) when the card
has no `FN`. A card with no `EMAIL` is counted as skipped. A quoted-printable
name or address, as vCard 2.1 exporters write for text that is not ASCII, is
decoded and must then be UTF-8. A name or address in base64 or a character set
other than UTF-8 is refused, and nothing from that file is imported. CardDAV is
unrelated to the file format and remains an owner question on #1570.

## Calendar invitations

`export-calendar` produces a new directory containing `<message-id>-<part>.ics`
files. It lists folders, then follows each folder's message cursor with
`include_imap_deleted=true` and `include_snoozed=true`. Only messages that the API
reports as containing calendar events need a stored-source download. The
unpaginated `calendar-events` summary route is deliberately not used: it would
load the account's complete calendar index, and omits event details.

Each message source is downloaded one at a time into a private temporary file,
then its MIME tree is streamed. Each `text/calendar` part is transfer-decoded
and charset-decoded into its own `.ics` file. The original calendar content is
not regenerated from parsed event summaries: invitation methods (`REQUEST`,
`CANCEL`, `REPLY`), timezones, recurrence rules, alarms, extensions and multiple
events in a part are preserved. Attachment filenames are never used as paths.
Duplicate invitations and versions remain separate files rather than choosing
an event's current state or interpreting cancellation policy.

The directory must not already exist. Files have mode 0600 and the directory
0700. The complete staged directory is published only after every page and
source succeeds. No source temporary files remain in it. Missing source,
invalid MIME/encoding, an invitation without a `text/calendar` source part,
or a source over `--max-message-bytes` fails the export rather than silently
omitting that invitation. The default and maximum source size is 1 GiB; MIME
depth and part counts have the parser's default limits. An account without
stored invitations produces an empty directory.

The files can be imported by calendar software. REST Mail has no standalone
iCalendar import command. Its existing `import-maildir` can import mail with a
`text/calendar` MIME part; the round-trip test wraps an exported `.ics` in such a
message, imports it into another server, then exports it unchanged. For
restoring the account's original mail, folders and flags, use `export-maildir`
and `import-maildir` rather than rebuilding mail from `.ics` files.

This is an export of stored invitation content, not a calendar view, free/busy
service, CalDAV server, or reconstruction of index-only event records after their
source mail has been permanently removed. Those are separate scope questions.

## Completion and limits

`--json` returns a summary containing `path` and either `contacts` or `calendars`
(the latter counts MIME calendar parts, not distinct event UIDs, and comes with
`skipped_folders`). Exit status is
0 for completion, 2 for usage/destination errors, and 1 for export failures.
`export-calendar` also fails on a folder list that says it is incomplete, and on
a message page that says more follows with no messages or without a cursor it
has not used in that folder.
Ctrl-C or SIGTERM cancels requests and cleans up staged output. HTTP requests
have a 30-minute timeout and each list page is capped at 64 MiB. A folder's
message listing carries each message's decoded bodies, so a page of 100 large
messages can pass that cap: `export-calendar` then asks for the same page again
with half as many messages, down to one, keeping only the ids of the
invitations on a page and downloading them after the page is read. A folder
whose single message is still over the cap cannot be exported; the export fails
naming that folder and the narrower request that would succeed, the same
command with `--skip-folder "<folder>"` (repeatable, calendar only). The
summary of such an export lists the folders it left out, and a `--skip-folder`
naming no folder of the account is refused. Memory is bounded
by a single decoded list item/MIME header, not by the number of contacts or
invitations. Message bodies are spooled to disk, never accumulated in memory.
Allow disk space for the output plus one message source.

These commands are not point-in-time snapshots. Avoid modifying contacts or
mail during export: offset/cursor pagination is not a snapshot transaction.

## Audit

Before anything is written, each command opens the export with
`POST /api/v1/accounts/{id}/exports`, which the server refuses without
`mail:export` and records as an `account.export` audit entry naming the actor,
the credential, the account and the data and format (`contacts` as `vcard`,
`calendar` as `icalendar`). When the export ends, the command closes it with
`POST /api/v1/accounts/{id}/exports/{session}/close`, recorded as a second
entry with `result` `completed` or `aborted`, the rows (contacts, or calendar
parts) and bytes written, and any folders left out. Ctrl-C records the export
as aborted; an export never closed is closed as aborted by the server a day
after it started. If the server cannot record the end, the command exits 1.
