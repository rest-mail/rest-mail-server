# The API is slow

The webmail, the mobile app, the admin webmail and the IMAP/POP3/SMTP
gateways all go through the API, so a slow API is felt everywhere at once.

## Symptoms

- Users say the webmail or the app is slow to load folders or send.
- IMAP clients time out.
- The `APIResponseTimeSlow` alert fires: the 95th percentile of
  `restmail_http_request_duration_seconds` is over 2 seconds for 5 minutes.
- The `HTTPErrorRateHigh` alert fires alongside it, when slow requests start
  timing out.

## Confirm

1. **Is it slow everywhere, or on some routes?** From Prometheus, scraping the
   API's `/metrics`:

   ```promql
   histogram_quantile(0.95, sum by (le, path) (rate(restmail_http_request_duration_seconds_bucket[5m])))
   ```

   One route (for example a message search or a folder list) points at its
   query; every route points at the database, the node or the network.
2. **Is it REST Mail or what is in front of it?** Time a request that does
   almost nothing:

   ```bash
   curl -sS -o /dev/null -w '%{time_total}\n' "$API/api/health"
   ```

   Fast here and slow elsewhere means the API itself is slow on real work.
   Slow here too means the ingress, the network, or a starved API pod.
3. **Is the API process starved?** `chore server:status` shows replicas and
   pods; `kubectl top pods -n rest-mail` (or your platform's equivalent) shows
   CPU and memory. A pod restarting repeatedly shows in `server:status`.
4. **Is the database the bottleneck?** `chore server:logs postgres --since
   1h` for lock waits and long checkpoints, and `chore server:logs api
   --since 1h` for context deadline errors from database calls.

   Database pool saturation is not exported yet (#1579).
5. **Which statements are slow?** With an API key holding `database:read`
   (the admin role has it):

   ```bash
   RESTMAIL_API_KEY=... restmail slow-statements list --server "$API" --sort total
   RESTMAIL_API_KEY=... restmail slow-statements list --server "$API" --sort mean --limit 10
   ```

   `--sort total` shows what costs the database most overall; `--sort mean`
   shows the statements that are slow on each call. Each statement is the
   text Postgres records, constants replaced by `$1`, `$2`, so no address or
   message content is shown. The figures count from the last reset, so a
   statement that was slow last week can still lead the list: reset, let
   traffic run for a few minutes while the problem shows, and list again.
   `--json` gives the whole statement text. The same view is
   `GET /api/v1/admin/database/statements`.

   `statistics_unavailable` means the database does not preload
   `pg_stat_statements`. The chart's bundled Postgres does
   (`postgres.statStatements.enabled`, on by default); an external database
   needs `shared_preload_libraries = 'pg_stat_statements'` set by whoever runs
   it and a restart, after which the API creates the extension when it
   starts.

   **Reset the counters** with an API key holding `database:manage` (the
   admin role has it):

   ```bash
   RESTMAIL_API_KEY=... restmail slow-statements reset --server "$API"
   ```

   This empties only the current database's statistics
   (`POST /api/v1/admin/database/statements/reset`), and is recorded in the
   audit log.

## Act

- **One route is slow**: note the `path` label and the time it started, and
  check whether it follows an upgrade (`chore server:status` shows the
  version and revision). Open an issue with the route, the p95, the version
  and the top statements from `restmail slow-statements list --json`.
- **The API pod is starved**: raise its CPU and memory requests and limits in
  the chart values and upgrade (`chore upgrade:helm`, which reads back every
  other value from the release). Adding replicas helps only when the
  database is not the bottleneck.
- **The database is the bottleneck**: check the database volume is not full
  and the database pod has the CPU and memory it asks for. Long-running
  imports (`console import-*`) or a large bulk queue action can load it;
  wait for them to finish or stop them.
- **The front proxy or network is slow**: to whoever runs the ingress; the
  API is not the cause.

## Verify

- The p95 query above is back under the alert's 2 seconds, and the alert has
  resolved.
- `curl` timings against `/api/health` and a real route (for example a
  folder list) are back to normal.
- Users confirm the webmail and their mail clients are responsive.

## Escalate

- The slow route has no cause you can see in the logs: open an issue with the
  route, the p95 over time, the version, and the database and API logs for
  the window (remove addresses and message content).
- The node or volume is slow: to the hosting provider.
- REST Mail runs in one region; users far from it see the network round trip
  on every request. That is not a fault to fix here
  ([REST Mail runs in one region](README.md#rest-mail-runs-in-one-region)).
