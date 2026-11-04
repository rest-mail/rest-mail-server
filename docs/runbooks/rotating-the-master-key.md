# Rotating the master key

`MASTER_KEY` encrypts the private keys and credentials the server keeps in
Postgres. `cmd/rotate-key` decrypts them with the old key and encrypts them
again with the new one, one row at a time, with no transaction across rows.

It changes the encryption and nothing else. The DKIM keys, TLS keys, TOTP
secrets and ACME account keys stay the same, so no DNS record changes and nobody
enrolls again. The 2026-04-29 review says the ACME account key rotates this way.
It does not: the stored key is encrypted again, and the account key is the same.

There is no way to rotate without stopping the server: a process holds one
key, and the data must be under that key.

**How to read this page.** Everything here was observed in the runs described
in [What was run](#what-was-run-and-what-was-not), unless it is marked *(not
run)*. Those parts, including every `kubectl` and `chore` step, come from reading
the code and have never been run on a cluster.

## Symptoms

There is no alert for this. You are here because:

- you have decided to replace the key. When to do that has not been decided
  ([Not decided](#not-decided));
- the key may have leaked. Read [If the key leaked](#if-the-key-leaked) first;
- a rotation went wrong, and a service holds a different key from the data. On
  the API that looks like:
  - signing in with 2FA answers `500` `Failed to verify two-factor
    authentication`;
  - activating a domain answers `409` `certificate_required`, `its private key
    cannot be read`;
  - `restmail-api dns` reports `DKIM key unreadable` for a domain.

## Confirm

1. **Count what will be rotated.** Keep these numbers: they are how you tell
   that a run has finished. *(The SQL was run; `kubectl exec` was not.)*

   ```bash
   kubectl -n rest-mail exec -i statefulset/restmail-postgres -c postgres -- \
     sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
   SELECT 'domains.dkim_private_key', count(*) FROM domains WHERE dkim_private_key <> ''
   UNION ALL SELECT 'dkim_rotation_keys.private_key', count(*) FROM dkim_rotation_keys
   UNION ALL SELECT 'credentials.encrypted_secret', count(*) FROM credentials WHERE kind = 'totp' AND encrypted_secret <> ''
   UNION ALL SELECT 'certificates.key_pem', count(*) FROM certificates WHERE key_pem <> ''
   UNION ALL SELECT 'acme_accounts.key_pem', count(*) FROM acme_accounts
   UNION ALL SELECT 'domain_relays.password_enc (not rotated)', count(*) FROM domain_relays WHERE password_enc <> '';
   SQL
   ```

   The total of the first five is what the command should report. The sixth is
   [the column it misses](#relay-passwords-are-not-rotated).
2. **Find the rows it cannot rotate.** A value stored unencrypted is counted as
   a failure, stays as it is, and makes every run exit 1:

   ```sql
   SELECT 'domains', name FROM domains
    WHERE dkim_private_key <> '' AND dkim_private_key NOT LIKE 'dkim:v1:%'
   UNION ALL SELECT 'certificates', id::text FROM certificates
    WHERE key_pem LIKE '-----BEGIN%';
   ```

3. **Get the relay passwords.** The command cannot do this for you, and each
   one must be sent again afterwards:

   ```sql
   SELECT d.id, d.name FROM domain_relays r JOIN domains d ON d.id = r.domain_id
    WHERE r.password_enc <> '';
   ```

## Act

### Before you start

- **Build the command.** It is in no image (#1912). You need a checkout and a Go
  toolchain. Use the tag your install runs: a different version was not tried.

  ```bash
  go build -o rotate-key ./cmd/rotate-key
  ```

- **Make a backup.** `chore backup:create --to ~/backups`
  ([Backups](../installing.md#backups)). If the new key is lost partway through,
  this is the only way back. *(Not run.)*
- **Make the new key, and store it before you use it.**
  `openssl rand -base64 32`.

### Rotate

1. **Stop the server.** It must be stopped, not just quiet (see
   [Why the services must stop](#why-the-services-must-stop)). This stops every
   Deployment and leaves Postgres running. Mail servers sending to you get no
   answer while it is stopped. *(Not run.)*

   ```bash
   chore server:offline
   ```

2. **Reach the database.** For the chart's Postgres, from a second terminal
   *(not run)*:

   ```bash
   kubectl -n rest-mail port-forward svc/restmail-postgres 15432:5432
   ```

   If Postgres is yours, connect to it directly and set `DB_SSLMODE` as your
   server does; the default is `disable`.
3. **Set the environment.** The keys are read only from the environment, never
   from arguments. The Secrets are named for a release called `restmail`;
   `api.existingSecret` may name another. *(The `kubectl` reads were not run.)*

   ```bash
   secret() { kubectl -n rest-mail get secret "$1" -o jsonpath="{.data.$2}" | base64 -d; }
   export DB_HOST=127.0.0.1 DB_PORT=15432
   export DB_NAME=$(secret restmail-postgres POSTGRES_DB)
   export DB_USER=$(secret restmail-postgres POSTGRES_USER)
   export DB_PASS=$(secret restmail-postgres POSTGRES_PASSWORD)
   export ROTATE_OLD_MASTER_KEY=$(secret restmail-api MASTER_KEY)
   read -rs ROTATE_NEW_MASTER_KEY && export ROTATE_NEW_MASTER_KEY

   # The command loads the production config, which refuses to start without
   # these. It uses neither value. MASTER_KEY set to the new key makes that
   # check refuse a new key under 16 bytes, which the server would not start
   # with (#1913). Leave ENVIRONMENT unset: under development the command logs
   # every UPDATE, with its ciphertext.
   export JWT_SECRET=$(openssl rand -hex 32)
   export MASTER_KEY=$ROTATE_NEW_MASTER_KEY
   ```

4. **Run it.** The summary goes to stdout. The log goes to stderr and names
   domains and row IDs, not secrets.

   ```bash
   ./rotate-key 2>rotate-key.log; echo "exit status: $?"
   ```

   You want exit status 0, and counts that match the first five numbers from
   [Confirm](#confirm). The line says `Master key rotation complete` even when
   nothing was processed, so read the counts and the exit status, not the words.
5. **Run it again, and expect exit status 1.** This is the check that nothing
   is left under the old key: a row still under it would be rotated now. It
   changes nothing that is already rotated.

   ```bash
   ./rotate-key 2>>rotate-key.log; echo "exit status: $?"
   ```

   You want `0 … processed`, and a failure count equal to your total.
6. **Give the services the new key.** *(Not run.)*

   ```bash
   kubectl -n rest-mail patch secret restmail-api --type merge \
     -p "{\"stringData\":{\"MASTER_KEY\":\"$ROTATE_NEW_MASTER_KEY\"}}"
   ```

   If the release's values set `api.masterKey`, change it too. The chart prefers
   that value to the Secret, so the next upgrade would put the old key back.

   **Do not run `chore backup:create` between step 4 and this step.** It takes
   the key from the Secret, and would pair new data with the old key.
7. **Start the server.** Every pod then starts with the new key. *(Not run.)*

   ```bash
   chore server:online
   ```

8. **Send the relay passwords again**
   ([below](#relay-passwords-are-not-rotated)).
9. **Make a new backup.** Older ones hold the old key.
10. **Clear your shell:** `unset ROTATE_OLD_MASTER_KEY ROTATE_NEW_MASTER_KEY
    MASTER_KEY JWT_SECRET DB_PASS`.

### Why the services must stop

With a real API running on the old key during a rotation:

- signing in with 2FA failed (`500`);
- activating a domain was refused (`409 certificate_required`);
- restarting it on the new key fixed both.

A process that writes during a run is worse, because the command reads each
table first and writes the rows afterwards. A scratch program stood in for the
API's writes, using the same model helpers:

- It installed a new DKIM key on a domain the command had read but not reached.
  **The command overwrote it with the old key**, reported success and said
  nothing.
- It stored a key under the old key on a domain the command had skipped. That
  row stayed under the old key, and only a later run found it.

The API's DKIM rotation and ACME renewal workers write these columns on their
own schedule *(not run)*.

### When each service gets the new key

Each reads `MASTER_KEY` once, when its process starts. Nothing reloads it, and
each holds only one key, so a mix of old and new does not work. All of them must
get the new key after the rotation and before serving. Stopping them first, then
`server:online`, does that. *(Read from `helm template` and the code; only the
API was run.)*

| Deployment | Gets `MASTER_KEY` | Reads it for |
|---|---|---|
| `restmail-api` | Yes | Everything below, 2FA, and ACME keys |
| `restmail-smtp-gateway` | Yes | DKIM signing, relay passwords, TLS keys from the database |
| `restmail-imap-gateway`, `restmail-pop3-gateway` | Yes | TLS keys from the database |
| `restmail-js-filter`, `restmail-webmail`, `restmail-admin` | No | Nothing |

The gateways have no `checksum/secret` annotation, so changing the Secret and
running an upgrade restarts the API but not them. If you ever change the key
without `server:offline`, restart them yourself. They also cache a TLS key they
have read for 5 minutes.

### If a run fails or stops

No run left a row half-written. A stopped run leaves each row wholly under one
key or the other, and neither key reads everything until you finish or back out.

| What happened | What you see | State | What to do |
|---|---|---|---|
| Wrong old key | `0 … processed`, every row a failure, exit 1 | Every row unchanged, byte for byte | Fix the key and run again |
| Killed (`SIGKILL`, `SIGTERM`) | No summary; exit 137 or 143 | Rows so far under the new key, the rest under the old | Run again with the same keys |
| Database lost | `key rotation failed: update DKIM key for domain …`, exit 1 | The same | Restore the connection, run again |
| Run after a partial run | The rest processed, rows already done counted as failures, exit 1 | All rotated | Expected. Run once more to check |
| Run after a full run | `0 … processed`, every row a failure, exit 1 | Unchanged, byte for byte | Expected: this is step 5 |
| Same key twice | `old and new keys must be different`, exit 1 | Unchanged | Fix the key |
| Row under neither key, or unencrypted | `WARN: failed to decrypt …` naming it, exit 1 on every run | That row unchanged | Replace it: see [Escalate](#escalate) |

To tell a real failure from a row already done, look at the failure count once
nothing more is processed. It should equal your total from [Confirm](#confirm).
Anything above that is a row the command cannot read.

**To back out instead,** swap the keys. On a run stopped partway, this moved the
rows it had done back to the old key and left the others alone. It exits 1,
because those others count as failures. Then every row read under the old key
again and none under the new.

```bash
old=$ROTATE_OLD_MASTER_KEY new=$ROTATE_NEW_MASTER_KEY
ROTATE_OLD_MASTER_KEY=$new ROTATE_NEW_MASTER_KEY=$old MASTER_KEY=$old ./rotate-key
```

**If the new key is lost** after any row was rotated, those rows cannot be read.
Restore the backup: `chore backup:restore` puts the backup's key back in the
Secret as well ([database runbook](database-lost-or-corrupt.md)). *(Not run.)*

### How long it takes

| Rows rotated | Took | On |
|---|---|---|
| 12 | 0.21 s | Local Postgres, see [What was run](#what-was-run-and-what-was-not) |
| 12,000 | 21.75 s (4.61 s user, 2.14 s system) | The same, on a heavily loaded machine |

That is about 1.8 ms a row. Domain DKIM keys are slower than the rest, because
the command also makes a test signature with each. **The time on a real install
has not been measured**, and neither has the time to stop and start the server,
which is what the outage will mostly be made of. A cluster's port-forward will be
slower than the local connection used here.

### Relay passwords are not rotated

`domain_relays.password_enc` is encrypted with `MASTER_KEY`, and the command
leaves it out (#1911). The command reports success, and its summary does not
mention them. After a rotation, every relay password is still under the old key.

The API gives no sign of this: it answers `"password_set": true`. Mail from that
domain retries and never goes out direct, until the password is set again
*(retrying: not run)*.

For each domain from [Confirm](#confirm), with the server on the new key, send
the password again. Sending only the settings is not enough: a `PUT` with no
`password` keeps the old ciphertext. The password comes from your relay provider.

```bash
curl -sS -X PUT -H "Authorization: Bearer $RESTMAIL_TOKEN" -H 'Content-Type: application/json' \
  -d '{"transport":"relay","host":"smtp.example.com","port":587,"tls_mode":"starttls","username":"…","password":"…"}' \
  "$API/api/v1/admin/domains/$ID/relay"
```

Use the settings `GET` on the same path shows. This needs `domains:write` across
all domains. *(The call was run with an admin's session, not an API key.)*

### If the key leaked

Rotating stops the old key opening the database from now on. It does not change
what the key protected. Anyone who had the old key and a copy of the database,
or a backup, which holds both, can still read every secret in it. Rotating does
not replace those secrets; each has its own way to replace it *(none of these
was run)*:

- DKIM keys: [DKIM key age](../dkim-key-age.md).
- TLS keys: reissue the certificate
  ([certificates runbook](certificates-expiring.md)).
- TOTP secrets: each account enrolls again.
- Relay passwords: change them at the provider.
- ACME account keys: this runbook has no procedure for replacing one.

[When something is wrong](../installing.md#when-something-is-wrong) covers
taking the server offline and ending sessions. What a leak requires of you has
not been decided ([Not decided](#not-decided)).

## Verify

- Step 4 exited 0 with the counts you expected, and step 5 reported `0 …
  processed` with a failure count equal to your total.
- `restmail-api dns` shows no `DKIM key unreadable` for any domain, and the DKIM
  record is the same as before the rotation. *(The binary was run. In a pod it
  would be `kubectl -n rest-mail exec deploy/restmail-api -- restmail-api dns
  --mail-host mail.example.com`, which was not run.)*
- An account with 2FA can sign in.
- A domain with a stored certificate can be activated.
- `chore server:verify` passes. *(Not run.)*
- Mail from a DKIM domain arrives signed, and mail from a relay domain leaves the
  queue. *(Not run: no mail was sent in these tests.)*

## Escalate

- A row stays under neither key, or stays unencrypted: replace what it holds.
  Upload the certificate again, or install a new DKIM key. The `WARN` line names
  the row. *(Not run.)*
- Neither key reads a row that should have rotated: stop, keep the log, and open
  an issue with the log lines (no key material).
- The new key is lost, or the data cannot be trusted: restore
  ([database runbook](database-lost-or-corrupt.md)).
- Postgres or the cluster is not yours: to whoever runs it, for the port-forward
  and the Secret.

## What was run, and what was not

**Run** on 2026-11-04, from main at `01c844bb`:

- `cmd/rotate-key` and `cmd/api`, built with Go 1.26.10 and run directly on an
  arm64 Mac, against Postgres 16.15 in a Docker container on the same machine,
  over a published port. The 1-minute load average was 38 to 48 around the timed
  12,000-row run, and about 100 earlier in the session.
- Throwaway keys, generated for the test and since destroyed.
- A scratch program seeded each kind of secret with the model helpers, then read
  it back with the same functions the server uses. With 2 domains: live DKIM
  keys, staged and previous rotation keys, live and revoked TOTP secrets,
  certificate keys, ACME account keys and relay passwords. With 2,000 domains:
  12,000 rotated rows and 2,000 relay rows.
- After a clean rotation, every rotated row decrypted to its original value under
  the new key, and none decrypted under the old. Relay passwords were the reverse.
  A domain with no key and a password credential were left alone, and rotation
  metadata and `dkim_created_at` did not change. `updated_at` did.
- Every case in [If a run fails or stops](#if-a-run-fails-or-stops), and the
  back-out. "Byte for byte" was checked with a digest of all six columns. The
  database was lost by refusing new connections and ending the running ones.
- A real API with an admin who enrolled TOTP through it, and a DKIM key,
  certificate and relay password installed through it. It was restarted on each
  key to give the API results above.
- The SQL in [Confirm](#confirm), the environment and commands in steps 3 to 5
  with values set by hand in place of the `kubectl` reads, and the back-out block.

**Not run:**

- Any cluster install: `kubectl`, `chore server:offline`, `server:online`,
  `backup:create`, `backup:restore`, `server:verify`, the port-forward and the
  Secret change.
- A rotation on real data, at any real size.
- The SMTP, IMAP and POP3 gateways. No mail was sent, signed or queued, and no
  mail client connected, so what they do with a stale key is read from the code.
- ACME issuance or renewal with a rotated key. The stored value was decrypted, not
  used against a CA.
- Postgres over TLS, more than one replica of anything, and a `rotate-key` built
  from a different version than the database.

## Not decided

The owner has not decided these, and this runbook does not decide them for you:

- When to rotate, and how often.
- How long to keep the old key and the backups made before a rotation. Those
  backups hold the old key, and restoring one puts it back.
- What must be replaced after a suspected leak, as well as rotating.
- How much notice to give before the outage, and who gets it.
