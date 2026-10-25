# Importing accounts with their existing password hashes

An operator with `mailboxes:write` on a hosted domain can create an account
without resetting its password, one at a time or a whole Dovecot passwd-file at
once. Aliases, forwarding and mail must still be moved separately.

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

Supported hashes are `{ARGON2ID}` (`$argon2id$`), `{ARGON2I}` (`$argon2i$`),
`{BLF-CRYPT}` (`$2a$`, `$2b$`, `$2y$`), `{SHA256-CRYPT}` (`$5$`) and
`{SHA512-CRYPT}` (`$6$`). Raw crypt(3) and PHC strings are accepted and stored
with the explicit scheme. bcrypt costs must be 4–14; SHA-crypt rounds must be
1000–1000000 (omitting rounds means 5000). Argon2 must be version 19, the
PHC string libsodium writes for Dovecot, with at most 256 MiB of memory, 10
passes and 8 lanes, an 8–64 byte salt and a 16–64 byte hash. These limits bound
the work an unauthenticated sign-in can request. MD5-CRYPT, PLAIN, Argon2d and
unrecognised schemes are refused.

Argon2's cost is memory, and every sign-in attempt pays it until the hash is
replaced, wrong passwords included. Dovecot warns that Argon2 "can require
quite a hefty amount of virtual memory" ([password schemes](https://doc.dovecot.org/2.3/configuration_manual/authentication/password_schemes/)).
REST Mail runs at most two Argon2 verifications at once; further attempts wait.
A hash made with more than 256 MiB, such as libsodium's sensitive setting, is
refused at import: that account needs a new password.

The account can use its old password in the app or through IMAP/SMTP/POP3,
subject to the ordinary account and two-factor rules. After a matching
password proof, SHA-crypt or Argon2 is replaced with REST Mail's bcrypt hash, preserving
the credential's identity and sessions. A concurrent password reset or
revocation wins over that replacement. A wrong password never upgrades a hash.

bcrypt can represent at most 72 password bytes. Longer imported passwords
keep their imported hash until the user chooses a shorter password; they
are never truncated. Imported-hash verification accepts at most 4096 password
bytes. Check these bounds before moving accounts whose source passwords or
hash work factors exceed them.

## A whole Dovecot passwd-file

`restmail import-passwd-file` creates one account per user of a Dovecot
passwd-file (`user:{SCHEME}hash:uid:gid:gecos:home:shell:extra`), with the same
request, schemes, limits and refusals as `import-account`:

```sh
restmail import-passwd-file --server https://mail.example.com \
  --domain example.com /etc/dovecot/users
```

A user written without a domain takes `--domain`. Each line succeeds or fails
on its own. An existing account is reported and left alone, never replaced. A
line with no password, a weak or unsupported scheme, or an invalid address is
reported with its line number, and the other users are still imported. The exit
status is 0 only when every user was created or already present, so a second run
after fixing the failed lines imports only those. `--dry-run` checks every line
and sends nothing; `--json` prints one object listing every user by outcome.
Hashes are never printed. Display names, quotas and Dovecot's extra fields are
not imported.

This does not yet move mail, or prove an
imapsync cutover. Keep the source mailbox available while planning that part
of the migration.
