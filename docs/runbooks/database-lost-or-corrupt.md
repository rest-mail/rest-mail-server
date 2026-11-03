# The database is lost or corrupt

The Postgres database holds mailboxes, messages' metadata, the queue, keys
(encrypted under the master key), grants and the audit log. Without it the
server cannot sign anyone in or deliver mail. The way back is a restore from a
backup made with `chore backup:create`
([Backups](../installing.md#backups)).

## Symptoms

- The API does not become ready; `chore server:logs api` shows it cannot
  connect to the database, or migrations fail.
- `chore server:logs postgres` shows the database refusing to start, or
  reporting corrupt pages.
- Data is missing or wrong after a storage incident: mailboxes gone, folders
  empty, the queue reset.

## Confirm

1. `chore server:status` — is the Postgres pod running, and the API ready?
2. `chore server:verify` — which layer fails first, and whose it is.
3. `chore server:logs postgres --since 1h` and `chore server:logs api --since
   1h` — read the database's own error before deciding it is lost. A full
   volume or a pod that cannot mount its volume is fixed in the cluster, not
   by a restore.

Only restore when the data is gone or cannot be trusted. A restore replaces
everything the server holds with the backup's contents: mail that arrived
after the backup was made is not in it.

## Act

1. **Stop the server answering**, so nothing writes to a database you are
   about to replace:

   ```bash
   chore server:offline
   ```

2. **Pick the backup.** The newest one made by `chore backup:create`, from the
   place you keep them (`--to`, or the release's `backupTarget`). The file
   holds the database, the master key and the install's own facts. It is a
   secret: it holds the master key.
3. **If the install itself is gone**, deploy it again first, with the same
   release name and namespace; the backup's install facts say what it was
   ([Installing](../installing.md)).
4. **Restore:**

   ```bash
   chore backup:restore --from FILE
   ```

   It says what the file holds and what it does not, asks for a typed code,
   takes the server offline, restores, brings it back and runs
   `server:verify`. `--name` and `--namespace` name a release other than
   `restmail` in `rest-mail`.
5. **Attachments** are not in that file: they live on their own volume and are
   backed up with `chore backup:attachments`. If the attachments volume was
   lost too, restore it from that backup before letting users back in.
6. **If you ran `server:offline` yourself** and the restore did not bring
   everything back, `chore server:online` puts back what offline stopped.

## Verify

1. `chore server:verify` passes every layer.
2. Sign in to the admin webmail and check that the domains (`/domains`) and
   mailboxes (`/mailboxes`) are those you expect.
3. **Send mail from each domain.** For every hosted domain, sign in as one of
   its mailboxes (webmail, or `POST /api/v1/messages/send` with that
   mailbox's credentials) and send:
   - one message to an outside address you control, and check it arrives
     with DKIM passing (the DKIM keys are decrypted with the restored master
     key, so this proves the key and the database match);
   - one message from outside to that mailbox, and check it arrives.
4. Check the outbound messages left the queue:

   ```bash
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/queue/stats"
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" \
     "$API/api/v1/admin/logs/delivery?domain=example.com&since=2026-11-03T12:00:00Z"
   ```

5. `chore dns:check` — the records each domain needs are still published, and
   the DKIM selectors match the restored keys.
6. Take a fresh backup now: `chore backup:create --to DIR`.

## Escalate

- The restore fails part way, or verify fails after it: keep the server
  offline and open an issue with the output of `backup:restore` and
  `server:verify` (never attach the backup file).
- No usable backup exists: the data cannot be recovered by REST Mail. Ask the
  storage or hosting provider whether the volume has a snapshot.
- DKIM fails after the restore: the master key in the backup does not match
  the database; restore a backup where both were taken together, or rotate
  the DKIM keys ([DKIM rotation](../dkim-rotation-alerts.md)).
