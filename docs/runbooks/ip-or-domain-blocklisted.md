# An IP or domain is on a blocklist

A DNS blocklist (DNSBL) has listed the server's sending IP, or a remote
server refuses one of our domains for its reputation. Mail to many
destinations starts failing with `policy` refusals.

[DNSBL monitor](../dnsbl-monitor.md) describes the check that watches the
sending IPs: how to turn it on, its logs, its metrics and its history. This
runbook uses it and does not repeat it.

## Symptoms

- The `DNSBLSendingIPListed` alert fires.
- The API logs `dnsbl: sending IP listed`, with the provider, codes and
  reason.
- Queue entries for many domains fail with `failure_category` `policy`, and
  `last_error` mentions a blocklist, "reputation" or "spam".

## Confirm

1. Which IP, which list, since when:
   - Metric `restmail_dnsbl_listed{ip, provider} == 1`.
   - `chore server:logs api --since 24h` and look for `dnsbl: sending IP
     listed`.
   - The durable history is the `dnsbl_listing_events` table
     ([Durable history](../dnsbl-monitor.md#durable-history)); there is no
     admin view of it yet.
2. Look the IP up on the provider's own site: it says why it listed the IP
   and how to ask for removal.
3. For a domain (not an IP) refusal, read the refusals in the queue:

   ```bash
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" \
     "$API/api/v1/admin/queue?status=bounced&limit=100"
   ```

   and check which sending domain they share.

## Act

### 1. Find the sender

A listing almost always follows a burst of mail from one place: a
compromised mailbox, a customer's bulk send, or a forwarding loop.

- Delivery log since shortly before the listing, by domain or sender
  (`messages:read`):

  ```bash
  curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" \
    "$API/api/v1/admin/logs/delivery?since=2026-11-03T06:00:00Z&limit=500"
  curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" \
    "$API/api/v1/admin/logs/delivery?sender=someone@example.com&since=2026-11-03T06:00:00Z"
  ```

  Look for one sender or one domain with far more mail than usual, or many
  bounces.
- The queue now (`/queue` in the admin webmail, or
  `GET /api/v1/admin/queue?sender=…`), for mail still waiting to go.
- Failed sign-ins before the burst point at a stolen password:
  `restmail_auth_failures_total`, and the audit log
  (`GET /api/v1/admin/audit`).

### 2. Stop them

- **One mailbox**: disable it. Admin webmail: Mailboxes → the mailbox
  (`/mailboxes/<id>`) → turn it off; or
  `PATCH /api/v1/admin/mailboxes/{id}` with `{"active": false}`. Disabling
  ends every session it has. If the password was stolen, also set a new one;
  that revokes its app passwords.
- **Mail it already queued**: select those entries in `/queue` and Delete (or
  Bounce, if the sender should hear), or use `bulk-delete` with
  `{"filter":{"sender":"someone@example.com","status":"pending"}}` (see the
  [queue runbook](mail-queuing-not-delivering.md#retry-bounce-or-delete-entries)).
- **One whole domain**: stopping one domain's outbound mail (held in the
  queue, not lost, restarted later) is in PR #1615. Once #1615 is released it
  is `PUT /api/v1/admin/domains/{id}/outbound-stop` with a reason, and the
  section "When one domain is the cause: stop its outbound mail" in the
  DNSBL monitor doc. Until then, disable that domain's sending mailboxes.
- **An attacking IP** (sign-in guessing): ban it in the admin webmail under
  Settings → Bans (`/settings/bans`), or `POST /api/v1/admin/bans`. See
  [fail2ban setup](../fail2ban-setup.md).

### 3. Request delisting

Only after the source is stopped: most lists relist quickly an IP that is
still sending.

1. Use the removal form on the provider's site for the listed IP. Give the
   cause and what you changed.
2. Some lists expire listings on their own after a quiet period and offer no
   form; their site says how long.
3. Check SPF, DKIM and DMARC for each domain while you wait:
   `chore dns:check`. Missing records make a listing more likely to return.

## Verify

- The API logs `dnsbl: sending IP no longer listed`, and
  `restmail_dnsbl_listed` drops to 0 for that IP and provider.
- Retry the mail that was refused (`bulk-retry` with
  `{"filter":{"status":"deferred"}}`), and watch the delivery log for
  `delivered`.
- The sender's volume in the delivery log is back to normal.

## Escalate

- The provider refuses delisting, or relists at once: to the hosting provider,
  who may give a new sending IP, and to the customer whose mail caused it.
- You cannot find a source in the delivery log: the listing may be for an
  earlier user of the IP; give the provider the dates you have held it.
- `DNSBLProviderNotAnswering` or `DNSBLAnswerStale`: the check itself is not
  working; see [DNSBL monitor](../dnsbl-monitor.md).
