# Importing accounts with their existing password hashes

An operator with `mailboxes:write` on a hosted domain can create an account
without resetting its password. This first migration step imports one account
at a time; aliases, forwarding and mail must still be moved separately.

Create the domain first. Set `RESTMAIL_API_KEY` to a scoped API key covering
that domain, then pipe the existing hash into the CLI:

```sh
printf '%s' "$HASH" | restmail import-account \
  --server https://mail.example.com \
  --address user@example.com --display-name 'User Name'
```

`HASH` is the password field from the source's password database, including
the entire hash and its optional Dovecot scheme prefix. The command reads
the hash from stdin and the key from the environment, so neither belongs in
the command's arguments. Protect both as credentials. The CLI never prints
them and refuses redirects. `--json` returns the created account's ID and
address. If that address already exists, import fails with 409 and leaves it
alone; an import cannot replace a live account's credentials.

The API equivalent is `POST /api/v1/accounts` with `address`, optional
`display_name` and `password_hash`, authenticated with a session or scoped API
key. `password` and `password_hash` are mutually exclusive. The same domain
permission, owner grant, transaction and audit rules as ordinary account
creation apply. Hashes are absent from the response and audit entries.

Supported hashes are `{BLF-CRYPT}` (`$2a$`, `$2b$`, `$2y$`),
`{SHA256-CRYPT}` (`$5$`) and `{SHA512-CRYPT}` (`$6$`). Raw crypt(3) strings
are accepted and stored with the explicit scheme. bcrypt costs must be 4–14;
SHA-crypt rounds must be 1000–1000000 (omitting rounds means 5000). These
limits bound the work an unauthenticated sign-in can request. MD5-CRYPT,
PLAIN, Argon2 and unrecognised schemes are refused by this initial importer.

The account can use its old password in the app or through IMAP/SMTP/POP3,
subject to the ordinary account and two-factor rules. After a matching
password proof, SHA-crypt is replaced with REST Mail's bcrypt hash, preserving
the credential's identity and sessions. A concurrent password reset or
revocation wins over that replacement. A wrong password never upgrades a hash.

bcrypt can represent at most 72 password bytes. Longer imported passwords
keep their SHA-crypt hash until the user chooses a shorter password; they
are never truncated. SHA-crypt verification accepts at most 4096 password
bytes. Check these bounds before moving accounts whose source passwords or
hash work factors exceed them.

This does not yet import Dovecot passwd-files in bulk, move mail, or prove an
imapsync cutover. Keep the source mailbox available while planning that part
of the migration.
