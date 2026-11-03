# Exporting an account's settings

`export-settings` writes one account's own settings to one JSON file, through
the public API. Mail is exported separately, as a Maildir, by `export-maildir`
(see [importing and exporting mail](migrating-mail.md)).

```sh
restmail export-settings --server https://mail.example.com --account-id 12 --path ./settings.json
```

`RESTMAIL_API_KEY` needs `mail:read` on the account or its domain. Every account
holds `mail:read` on its own mailbox, so an account can export itself with a key
of its own, and the admin role holds it at its scope. The command only reads,
through existing routes, each with its usual `mail:read` check and request
audit:

| Section | Route |
| --- | --- |
| `account` | `GET /api/v1/accounts/{id}` |
| `folders` | `GET /api/v1/accounts/{id}/folders` |
| `sieve` | `GET /api/v1/accounts/{id}/sieve` |
| `vacation` | `GET /api/v1/accounts/{id}/vacation` |
| `quiet_hours` | `GET /api/v1/accounts/{id}/quiet-hours` |
| `contact_reply_time` | `GET /api/v1/accounts/{id}/contact-reply-time` |

## The file

```json
{
  "format": "restmail.settings.v1",
  "exported_at": "2026-11-03T14:04:05Z",
  "account": { "id": 12, "address": "ada@example.com", "display_name": "Ada Lovelace" },
  "folders": [ { "name": "Archive/2020" }, { "name": "INBOX" }, { "name": "Sent" } ],
  "sieve": { "script": "require \"fileinto\"; …", "active": true },
  "vacation": {
    "enabled": true, "subject": "Away", "body": "Back on Monday.",
    "start_date": "2026-12-20T08:00:00Z", "end_date": "2026-12-28T00:00:00Z"
  },
  "quiet_hours": { "enabled": true, "start": "22:00", "end": "07:30", "time_zone": "Europe/Berlin" },
  "contact_reply_time": { "enabled": true }
}
```

- `format` names this layout. Renaming or removing a field, or changing what
  one means, makes a new version; a reader should refuse a version it does not
  know.
- `exported_at` and the vacation dates are UTC, in RFC 3339. A vacation date
  that is not set is `null`.
- `folders` are names only, as the API lists them, the standard folders
  included. Their messages are `export-maildir`'s.
- `sieve` is the stored script exactly, and whether it is active. An account
  without a script has an empty `script`.

Every section is always present, with the server's defaults when the account
never set it.

## What is never exported

The command writes only the fields above. A field the server returns beyond
them is dropped, so no new field reaches the file unnoticed. In particular the
file holds no password hash, no API key, app password or client secret, no
two-factor (TOTP) seed, recovery code, session or refresh token, and no push
device token: the command reads no credential, two-factor, session or device
route. Message counts, quota usage and the Sieve script's hash and timestamps
are server state, not settings, and are left out too.

The file is written beside `--path` and renamed into place when complete,
readable only by its owner (mode 0600). An existing file is refused unless
`--overwrite` is given. A refused or failed read writes nothing. `--json` writes
one summary (`path`, `format`, `folders`) or one error; the API key is never
printed.

## Not in the export yet

The public API has no read route for these, so they are not exported
([#1611](https://github.com/rest-mail/rest-mail-server/issues/1611)):

- the roles (special use) of folders, which the IMAP gateway gives the standard
  folders by name;
- the aliases and sending identities that deliver to or send as the account,
  which only the administrators' alias list shows;
- a signature, which REST Mail does not store.

Contacts ([#1570](https://github.com/rest-mail/rest-mail-server/issues/1570))
and calendar events ([#1569](https://github.com/rest-mail/rest-mail-server/issues/1569))
are not part of this export.
