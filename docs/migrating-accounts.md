# Importing accounts with their existing password hashes

An operator with `accounts:write` on a hosted domain can create an account
without resetting its password, one at a time or a whole Dovecot passwd-file at
once. Then import aliases and hosted forwarding with `restmail import-aliases`
as described below, and mail with the [mail migration guide](migrating-mail.md).

Create the domain first. Making an account takes a person who proved a second
factor, so an API key is refused with 403, whatever it holds. Sign in with
`"client": "native"` and your authenticator code, and set `RESTMAIL_API_KEY` to
the session's `access_token`: it must have `accounts:write` on the domain, and
the sign-in must be recent when each account is made, 15 minutes old at most, or
less where the operator has shortened that with `FRESH_TWO_FACTOR_WINDOW`. Then
pipe the existing hash into the CLI:

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
`display_name` and `password_hash`, authenticated with such a session. `password` and `password_hash` are mutually exclusive. The same domain
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

## Aliases and hosted forwarding

After creating the domains and destination accounts, import a text
[Postfix `virtual(5)` map](https://www.postfix.org/virtual.5.html):

```text
# source address       destination addresses
sales@example.com      alice@example.com
bob@example.com        alice@example.com
team@example.com       alice@example.com,
                       bob@example.com
```

`sales` is an alias; the `bob` account's address is a hosted forwarding source.
Both use the existing alias table, one active row per source/destination pair;
there is no separate forwarding table or imported keep-a-copy setting.
The command imports routing configuration, not delivery implementation. Alias
delivery is tracked separately in [issue 1533](https://github.com/rest-mail/rest-mail-server/issues/1533)
and [PR 1539](https://github.com/rest-mail/rest-mail-server/pull/1539); this
importer does not depend on that branch or certify delivery on its own.

```sh
restmail import-aliases --server https://mail.example.com \
  /etc/postfix/virtual
```

Set `RESTMAIL_API_KEY` to the administrator session used above, or a scoped API
key with `accounts:read` and `accounts:write` on the source domains and
`accounts:read` on each destination account. The CLI reads the existing
configuration with `GET /api/v1/admin/mailboxes` and
`GET /api/v1/admin/aliases`, including all pages. Each new pair is submitted to
`POST /api/v1/admin/aliases` with `source_address` and `destination_address`.
Normal API authorization, validation and duplicate constraints still apply;
there are no direct database writes or special migration privileges.

The issue specifies no alias-export format, so Postfix's existing plain text
map is the interchange format, not a new bundle-specific format.
[docker-mailserver's `postfix-virtual.cf`](https://docker-mailserver.github.io/docker-mailserver/latest/config/advanced/optional-config/)
can be used directly for supported address-to-address mappings. Export equivalent
explicit mappings from mailcow or Mailu into this format; their SQL databases,
JSON backups and bundle configuration archives are not read automatically.
Dovecot's [passwd-file](https://doc.dovecot.org/2.3/configuration_manual/authentication/passwd_file/)
describes authentication/user fields, not a virtual alias map: continue using
`import-passwd-file` for that file, and export routing from the associated MTA.

Blank and comment lines (first non-whitespace character `#`) are ignored.
Indented lines continue the preceding logical line. Destinations are
comma-separated; repeated destinations in a line are collapsed. A bare user
name on either side needs `--domain example.com`; explicit addresses keep
their domain. Hosted identities use REST Mail's lowercase/NFC/IDNA account
normalization. Only bare, unquoted addresses are supported.

Every destination must already be an active, readable hosted mailbox. External
forwarding is refused pending an owner policy decision on
[issue 734](https://github.com/rest-mail/rest-mail-server/issues/734#issuecomment-6610152862):
the alias API currently checks destination syntax but has no external-forward
verification/authorization policy. An unreadable or inactive mailbox is also
refused. Alias chains, catch-alls, domain patterns, self redirects, file/pipe
destinations and Sieve or `.forward` programs are not translated. Such a line
fails before any of its destinations are written; it is never partly converted
into a different routing rule.

Reruns report identical active rows as `existing` and add nothing. They never
replace targets or reactivate an inactive rule. An existing source with targets
outside the requested set, inactive rows or legacy noncanonical spellings is
reported as conflicting and left alone. A partially imported fan-out may be
completed on a later run if its existing rows are an active subset of that
line's destinations. HTTP failures can leave earlier destinations imported;
the API has no atomic multi-destination operation. Do not modify the same
source's routing concurrently with migration; a conflicting create race is
re-read, but the public API has no whole-source compare-and-swap operation.

Failures include the starting physical line number and do not stop later
lines. Exit status is 0 when all pairs were created/already present, 1 for
failures and 2 for usage errors. `--json` groups pairs under `created`,
`existing`, `would_create` and `failed`, with source, destination, ID and line
where applicable. `--dry-run` performs syntax checks only and sends no API
requests: it cannot certify destination existence, permissions, conflicts or
forwarding policy. Input is limited to 16 MiB and each logical line to 64 KiB;
an oversized file is refused before writing anything. Requests never follow
redirects, and credentials and server diagnostic bodies are not printed.

Keep the source server available while importing
[mail](migrating-mail.md) and planning the final cutover.
