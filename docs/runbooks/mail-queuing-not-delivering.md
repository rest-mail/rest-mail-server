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
- `QueueBacklogHigh`, `QueueWorkersSaturated`, `QueueWorkerStalled` or
  `QueueOldestMessageOld` fires. Which one says what kind of wait it is: see
  [Saturated, held back or a backlog](#saturated-held-back-or-a-backlog).

## Saturated, held back or a backlog

Mail can wait in the queue for three different reasons, and each needs a
different fix. The SMTP gateway's `/metrics` tells them apart. It recounts the
queue every 15 seconds, splitting the mail that is due now by what stands
between it and a worker:

| Gauge | What it counts |
|---|---|
| `restmail_queue_waiting_for_worker` | Mail due now that a worker could send: its destination has a place free under `QUEUE_MAX_PER_DESTINATION`. |
| `restmail_queue_waiting_for_worker_oldest_seconds` | How long the longest-waiting of those has been due. |
| `restmail_queue_held_by_destination` | Mail due now that no worker may send yet, because its recipient domain already has `QUEUE_MAX_PER_DESTINATION` messages in flight. |
| `restmail_queue_size` | Every message still waiting to leave, including retries not yet due. |
| `restmail_queue_due` | Mail due now, all of it, held or not, plus the messages in flight. Do not read it as a backlog. |

- **Saturated workers** (`QueueWorkersSaturated`): mail a worker could send has
  waited more than 5 minutes for one. A free worker takes due mail within
  `QUEUE_POLL_INTERVAL` (5 seconds by default), so every worker is busy and
  more mail is due than they get through. `restmail_queue_waiting_for_worker`
  is high and its oldest keeps growing. Raise `QUEUE_WORKERS`, or run more SMTP
  gateway replicas, keeping the database's connection limit in mind. If
  `QueueWorkerStalled` fires as well, nothing is being sent at all: follow
  [The worker is not running](#the-worker-is-not-running) first.
- **A destination held back** (no alert of its own): one recipient domain has
  more mail due than the per-destination limit lets out at once, so most of it
  waits its turn. `restmail_queue_held_by_destination` is high while
  `restmail_queue_waiting_for_worker` is near 0 and its oldest stays near 0.
  This is `QUEUE_MAX_PER_DESTINATION` working as meant: it stops one slow or
  large receiver from tying up every worker. Adding workers does not help, and
  `QueueBacklogHigh` does not count this mail. Find the domain:
  `restmail mail-queue list --server "$API" --status delivering` lists the
  messages in flight, and the domain most of their recipients share is the one
  held back. Leave it if it is draining, even slowly. If that domain answers
  slowly or not at all, go to [One destination fails](#one-destination-fails).
  Raise `QUEUE_MAX_PER_DESTINATION` only if the receiver accepts more
  connections from you; many large receivers throttle senders that open more.
  `QueueOldestMessageOld` still fires if held mail waits more than 6 hours.
- **A backlog** (`QueueBacklogHigh`): more than 1000 messages are waiting to
  leave, not counting held mail. Most of a backlog is usually deferred mail
  waiting for its next retry after failures. Follow [Confirm](#confirm) below
  to find why the attempts fail.

In Prometheus or Grafana, compare the three over the time the problem started:

```promql
restmail_queue_waiting_for_worker
restmail_queue_waiting_for_worker_oldest_seconds
restmail_queue_held_by_destination
```

A rise in the first two is saturation. A rise in the third alone is a
destination held back.

Set `smtpGateway.queueMaxPerDestination` in Helm to configure
`QUEUE_MAX_PER_DESTINATION` (default `2`; `0` disables the cap). The cap applies
per recipient domain across all gateway replicas sharing the database. Lowering
it can reduce throughput to a busy domain; raising or disabling it lets a slow
domain occupy more workers. Keep it below `smtpGateway.queueWorkers` if a single
gateway must retain spare workers for other destinations.

## Confirm

1. Count the queue by status. Admin webmail: Queue (`/queue`). Terminal:
   `restmail mail-queue stats --server "$API"`. API (`queue:read`):

   ```bash
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/queue/stats"
   ```

   A growing `pending` + `deferred` count with a flat `delivered` count means
   nothing is going out.
2. Look at the entries that are waiting, and read why
   (`restmail mail-queue list --server "$API" --status deferred`, or):

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

# give up on one entry and mark it bounced (its sender is not told, see below)
curl -sS -X POST -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/queue/123/bounce"

# purge entries that have already bounced
curl -sS -X DELETE -H "Authorization: Bearer $RESTMAIL_TOKEN" -H 'Content-Type: application/json' \
  -d '{"filter":{"status":"bounced"}}' \
  "$API/api/v1/admin/queue/bulk-delete"
```

Bulk requests take `ids` (at most 1000) or a `filter`. An empty filter `{}`
selects every entry the action applies to, so always name a status or a
domain. Neither action tells the sender. A delete drops the entry; a bounce
stops delivery and keeps the entry as `bounced`, with `last_error` saying an
admin did it, but sends no bounce message (#1950). When the sender should know,
tell them yourself.

From a terminal, `restmail mail-queue` does the same through the public API,
with the token in `RESTMAIL_API_KEY`:

```bash
export RESTMAIL_API_KEY=$RESTMAIL_TOKEN
restmail mail-queue stats   --server "$API"
restmail mail-queue list    --server "$API" --status deferred --domain example.org
restmail mail-queue show    --server "$API" 123
restmail mail-queue retry   --server "$API" 123
restmail mail-queue retry   --server "$API" --status deferred --domain example.org
restmail mail-queue bounce  --server "$API" 123
restmail mail-queue purge   --server "$API" --status bounced
```

`retry`, `bounce` and `purge` with a filter first say how many entries they
will touch and ask; `--yes` skips the question in a script. They refuse an
empty filter: name a status, domain, sender or recipient, or give `--all`.
`list` shows each entry's `attempts`, `next_attempt`, `failure_category` and
`last_error`; add `--json` to any command for the API's own fields.

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
