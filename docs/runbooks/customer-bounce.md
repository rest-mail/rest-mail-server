# A customer reports bouncing mail

A customer sent mail and got a bounce, or says a recipient never received it.
Every failed delivery attempt is classified into a failure category, which
decides whether the message is retried or bounced at once, and the bounce
says the category's reason in plain words (#142).

## Symptoms

- The customer got a delivery status notification (bounce) for a message.
- A recipient says the mail never arrived, and the customer got nothing.

## Confirm

1. **Get the details**: the sender address, the recipient address, roughly
   when it was sent, and the bounce itself if they have it.
2. **Find the message in the queue.** Admin webmail: Queue (`/queue`), filter
   by sender or recipient, then open the entry (`/queue/<id>`). API
   (`queue:read`):

   ```bash
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" \
     "$API/api/v1/admin/queue?sender=customer@example.com&recipient=friend@example.org"
   ```

   Older messages are in the delivery log (`messages:read`):

   ```bash
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" \
     "$API/api/v1/admin/logs/delivery?sender=customer@example.com&recipient=friend@example.org&since=2026-11-01T00:00:00Z"
   ```

   `sender` and `recipient` match part of the address, ignoring case.
3. **Read the entry**: `status` (`pending`, `delivering`, `deferred`,
   `delivered`, `bounced`, `expired`), `failure_category`, `attempts`,
   `last_error` (the remote server's reply) and `last_error_code`. The admin
   webmail's entry page shows `last_error`, attempts and the next attempt; the
   category is in the API's answer.

Once PR #1596 is released, the customer can see each recipient's outcome
themselves (`GET /api/v1/messages/{id}/recipients`, with the category's
reason but not the remote's raw reply), and the webmail shows it on the sent
message.

## Failure categories

| Category | Kind | Meaning | What happens |
|---|---|---|---|
| `bad_mailbox` | permanent | no such mailbox, or it is disabled | bounced at once; the address is suppressed for the sending domain |
| `bad_domain` | permanent | no such domain, or it accepts no mail | bounced at once; suppressed |
| `policy` | permanent | refused by policy: spam, reputation, authentication | bounced at once |
| `other_permanent` | permanent | refused for another reason | bounced at once |
| `mailbox_full` | transient | the recipient's mailbox is full | retried, first after an hour |
| `greylisted` | transient | asked to try again later | retried, first after 7 minutes |
| `rate_limited` | transient | the remote is limiting us | retried, first after 15 minutes |
| `connection` | transient | no connection, or it broke off | retried, first after 5 minutes |
| `tls` | transient | no secure connection where one is required | retried, first after 15 minutes |
| `dns` | transient | a lookup failed for now | retried, first after 5 minutes |
| `other_transient` | transient | anything else temporary | retried, first after 5 minutes |

Transient failures back off up to 4 hours between attempts (8 for
`mailbox_full`) and expire after five days in the queue, when the sender gets
a bounce.

## Decide and act

- **`bad_mailbox` or `bad_domain`**: the address is wrong or gone. Tell the
  customer to check it. The address is now suppressed for the customer's
  domain, so later mail to it bounces at once. If the address was wrong only
  for a while and now works, clear the suppression (`domains:read` to list,
  `domains:write` to clear):

  ```bash
  curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/domains/7/suppressions"
  curl -sS -X DELETE -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/domains/7/suppressions/31"
  ```

  and ask the customer to send again.
- **`policy`**: the recipient's server refused us. Read `last_error`: a
  blocklist name means [An IP or domain is on a blocklist](ip-or-domain-blocklisted.md);
  an SPF, DKIM or DMARC failure means a DNS record is missing or wrong
  (`chore dns:check`); a content refusal means the message itself looked
  like spam to them.
- **`other_permanent`**: read `last_error`; it is the remote's own words.
  Often only the recipient's postmaster can say more.
- **Transient and still queued** (`pending` or `deferred`): it will be tried
  again. Tell the customer it is delayed, not lost. When the cause is fixed
  sooner (for example the remote is back), retry it now: Retry on
  `/queue/<id>`, or `POST /api/v1/admin/queue/{id}/retry` (`queue:manage`).
- **`expired`**: it was retried for five days and never accepted. Fix the
  cause, then ask the customer to send again.
- **`delivered`**: the remote accepted it. The recipient should check their
  spam folder; after that, it is the recipient's provider to ask.
- **Not in the queue or the delivery log**: it was never accepted for
  sending. Check the customer's Sent folder, and their client's own error.
  Mail between two mailboxes on this server does not go through the queue.

From a terminal (`RESTMAIL_API_KEY` set to a token with `queue:read`, or
`queue:manage` to retry): `restmail mail-queue list --server "$API" --recipient
someone@example.org` finds the entry, `restmail mail-queue show --server "$API"
<id>` shows its last error, and `restmail mail-queue retry --server "$API" <id>`
retries it now.

## Verify

- A retried entry shows `delivered` in `/queue/<id>` or the delivery log.
- After clearing a suppression, a new message to the address is attempted
  rather than bounced at once.
- The customer or the recipient confirms the mail arrived.

## Escalate

- `policy` refusals from one large provider for many customers: the sending
  reputation is the problem; follow the blocklist runbook and the provider's
  postmaster tools.
- The category does not match `last_error` (for example a reply that is
  clearly temporary classified as permanent): open an issue with
  `last_error_code` and `last_error` (remove the addresses), so the
  classifier can be fixed.
