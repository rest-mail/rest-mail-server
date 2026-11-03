# Mail is queuing but not delivering

Outbound mail is accepted from senders and sits in the queue. The queue worker
runs in the SMTP gateway; it delivers to remote servers and retries failures on
a schedule chosen by the kind of failure (see
[A customer reports bouncing mail](customer-bounce.md#failure-categories)).

## Symptoms

- Customers say mail they sent has not arrived, but they got no bounce.
- The admin webmail's Queue page (`/queue`) shows many `pending` or `deferred`
  entries, and few new `delivered` ones.
- The `QueueErrorRateHigh` alert (`rate(restmail_queue_errors_total[5m]) > 0.1`)
  fires.

The `QueueBacklogHigh` and `QueueWorkerStalled` alerts depend on
`restmail_queue_size`, which nothing sets today, so they do not fire
(#1630). Do not take their silence as a healthy queue.

## Confirm

1. Count the queue by status. Admin webmail: Queue (`/queue`). API
   (`queue:read`):

   ```bash
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/queue/stats"
   ```

   A growing `pending` + `deferred` count with a flat `delivered` count means
   nothing is going out.
2. Look at the entries that are waiting, and read why:

   ```bash
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" \
     "$API/api/v1/admin/queue?status=deferred&limit=50"
   ```

   Each entry has `attempts`, `next_attempt`, `last_error`, `last_error_code`
   and `failure_category`. In the admin webmail, open an entry (`/queue/<id>`)
   to see the last error and the next attempt.
3. Decide which case you are in:
   - **Every destination fails** (many domains, same `failure_category`, such
     as `connection`, `dns` or `tls`): the problem is on this side, usually
     outbound port 25, DNS resolution, or the sending IP's reputation.
   - **One destination fails** (filter with `?domain=example.org`): the
     remote side is down, rate limiting (`rate_limited`), greylisting
     (`greylisted`) or refusing us (`policy`).
   - **Nothing has been attempted** (`attempts` is 0 and `next_attempt` is in
     the past): the worker is not running.
4. Check the worker is up and what it says:

   ```bash
   chore server:status              # are the smtp-gateway pods ready?
   chore server:logs smtp --since 1h
   ```

## Act

### The worker is not running

1. `chore server:status` shows the SMTP gateway's replicas. If its pods are
   not ready, read why with `chore server:logs smtp --since 1h`.
2. `chore server:verify` checks the install layer by layer and says whose
   failure it is.
3. Once the gateway is running, entries whose `next_attempt` has passed are
   picked up on its next pass. Nothing needs to be retried by hand.

### Every destination fails

1. `failure_category` `connection`: check that the cluster can open outbound
   TCP port 25. Many cloud providers block it by default and unblock it on
   request.
2. `dns`: check the cluster's resolver answers MX lookups.
3. `tls`: a remote's MTA-STS or DANE policy requires a TLS connection that
   failed. Read `last_error` for the host.
4. `policy` from many domains: the sending IP or a domain is probably on a
   blocklist. Follow [An IP or domain is on a blocklist](ip-or-domain-blocklisted.md).
5. When the cause is fixed, retry what is waiting rather than waiting for
   each entry's backoff.

### One destination fails

- `rate_limited`, `greylisted`, `mailbox_full`: these back off on their own
  and usually clear. Leave them.
- The remote is down: leave it. Transient failures are retried for the
  queue's whole lifetime before they expire and the sender gets a bounce.
- The remote refuses us (`policy`): see the blocklist runbook, and contact the
  remote's postmaster if no listing explains it.

### Retry, bounce or delete entries

Admin webmail: Queue (`/queue`) — select entries, then Retry, Bounce or
Delete; or open one (`/queue/<id>`) for the same actions on it.

API (`queue:manage`):

```bash
# retry one entry now
curl -sS -X POST -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/queue/123/retry"

# retry every deferred entry for one destination
curl -sS -X POST -H "Authorization: Bearer $RESTMAIL_TOKEN" -H 'Content-Type: application/json' \
  -d '{"filter":{"status":"deferred","domain":"example.org"}}' \
  "$API/api/v1/admin/queue/bulk-retry"

# give up on one entry and bounce it to its sender
curl -sS -X POST -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/queue/123/bounce"

# purge entries that have already bounced
curl -sS -X POST -H "Authorization: Bearer $RESTMAIL_TOKEN" -H 'Content-Type: application/json' \
  -d '{"filter":{"status":"bounced"}}' \
  "$API/api/v1/admin/queue/bulk-delete"
```

Bulk requests take `ids` (at most 1000) or a `filter`. An empty filter `{}`
selects every entry the action applies to, so always name a status or a
domain. Deleting an entry drops the message without telling its sender; bounce
it instead when the sender should know.

A queue command for the terminal, so this does not need `curl`: not available
yet (#1627).

## Verify

- `GET /api/v1/admin/queue/stats` shows `delivered` rising and `pending` +
  `deferred` falling.
- The delivery log shows recent deliveries (`messages:read`):

  ```bash
  curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" \
    "$API/api/v1/admin/logs/delivery?status=delivered&since=2026-11-03T12:00:00Z"
  ```

- Send a test message to an outside address you control and watch it arrive.

## Escalate

- The SMTP gateway will not start, or `server:verify` names a layer you do not
  run (cluster, ingress, DNS): to whoever runs that layer.
- Outbound port 25 is blocked: to the hosting provider.
- Entries stay `pending` with `attempts` 0 while the gateway is ready and its
  logs show no errors: open an issue with the output of
  `chore server:status`, the queue stats and an entry's JSON (remove the
  addresses).
