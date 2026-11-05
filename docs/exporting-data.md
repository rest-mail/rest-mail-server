# Exporting contacts and calendar invitations

These commands complement `restmail export-maildir` and `restmail export-settings`:

```sh
restmail export-contacts --server https://mail.example.com --account-id 12 --path ./contacts.vcf
restmail export-calendar --server https://mail.example.com --account-id 12 --path ./calendar
```

Both use `RESTMAIL_API_KEY` and only the public API. The account owner can use a
key with `mail:read` on that account. An administrator needs the same existing
capability at a scope covering the account; the commands add no authority or
administrator bypass. A different account's key, or a key without `mail:read`,
cannot export the data. HTTPS is required except on localhost. Redirects are
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

REST Mail currently imports contacts through its JSON `contacts/import` route,
not a vCard importer. Converting `EMAIL` and `FN` to that route's `email` and
`name` fields restores those two fields; that importer sets trust to `auto` and
source to `import`. Native vCard import and full metadata restoration remain
part of #1570, not functionality provided by this command. CardDAV is unrelated
to exporting the format and remains an owner question on that issue.

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
(the latter counts MIME calendar parts, not distinct event UIDs). Exit status is
0 for completion, 2 for usage/destination errors, and 1 for export failures.
Ctrl-C or SIGTERM cancels requests and cleans up staged output. HTTP requests
have a 30-minute timeout and each list page is capped at 64 MiB. Memory is bounded
by a single decoded list item/MIME header, not by the number of contacts or
invitations. Message bodies are spooled to disk, never accumulated in memory.
Allow disk space for the output plus one message source.

These commands are not point-in-time snapshots. Avoid modifying contacts or
mail during export: offset/cursor pagination is not a snapshot transaction.

These commands only read existing public routes and do not write an
export-specific audit entry. Whole-account migration and audit requirements
are discussed in #1577; this document describes only these two commands. No
new server route, audit event, import command or API schema is added by this
change.
