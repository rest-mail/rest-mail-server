# Spam is reaching a customer's inbox

Inbound mail passes through the domain's inbound pipeline: a list of filters
(SPF, DKIM, DMARC, rspamd, ClamAV, custom JavaScript filters and more) that
decide to continue, reject, quarantine, discard or defer each message. Spam in
an inbox means every filter let that message continue.
[Adapter filters](../adapter-filters.md) describes the filters and the
recommended order.

## Symptoms

- A customer forwards spam that arrived in their inbox, or says spam has
  increased.
- `restmail_pipeline_terminal_total{direction="inbound",outcome="delivered"}`
  rises while `outcome="quarantined"` and `"rejected"` stay flat.

## Confirm

1. **Get the Message-ID** of a spam message from the customer (the
   `Message-ID:` header; ask them for the full headers, not a forward).
2. **Find its pipeline trace.** Admin webmail: Pipelines → Logs
   (`/pipelines/logs`), filter by Message-ID. API (`pipelines:read`):

   ```bash
   curl -sS -G -H "Authorization: Bearer $RESTMAIL_TOKEN" \
     --data-urlencode 'rfc_message_id=<abc123@sender.example>' \
     "$API/api/v1/admin/pipelines/logs"
   ```

   The entry has the `pipeline_id`, `final_action`, `outcome`, `spam_score`,
   `client_ip` and `mail_from`, and the `message_id` of the delivered copy.
3. **Read the trace step by step.** Admin webmail: `/messages/<message_id>/trace`.
   API (`observability:read`):

   ```bash
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/messages/42/trace"
   ```

   Each step is one filter with its decision. Look for: a filter that should
   have caught it and said continue (rspamd's score under its threshold, SPF
   or DMARC failing but set only to tag), a filter that was skipped, or a
   filter missing from the pipeline.

Delivered messages are traced at a sample rate (`TRACE_SAMPLE_RATE`, 10% by
default) and kept for `TRACE_RETENTION_DAYS` (7 by default), so a given
delivered message may have no trace. Rejected and quarantined ones are the
outcomes you most need and are the ones to look for first. If several of the
customer's examples have no trace, raise the sample rate for a while to catch
the next ones.

## Act

1. **Choose the change.** Prefer the smallest one that catches this pattern:
   - Tighten an existing filter's configuration (for example rspamd's
     quarantine or reject threshold, or make a DMARC `p=reject` result
     reject instead of tag).
   - Add a missing filter to the inbound pipeline (rspamd and ClamAV are
     sidecars that must be enabled first:
     [Enabling scanning sidecars](../adapter-filters.md#enabling-scanning-sidecars)).
   - Block one sender or domain for the customer's domain:
     `POST /api/v1/admin/domains/{id}/blocklist` (`domains:write`); list them
     with `GET` on the same path. The admin webmail has no page for this list
     yet.
   - Write a custom JavaScript filter for a pattern the others miss: admin
     webmail Custom filters (`/custom-filters/new`).
2. **Test the change before saving it**, with the spam message as the sample:
   - A custom filter: Custom filters → the filter (`/custom-filters/<id>`) →
     Test Filter, or `POST /api/v1/admin/custom-filters/{id}/test`.
   - One built-in filter with a configuration you are trying:
     `POST /api/v1/admin/pipelines/test-filter` with `filter_name`, `config`
     and `email`.
   - The whole pipeline: `POST /api/v1/admin/pipelines/test` with
     `pipeline_id` and `email` (`pipelines:write`).

   Test a normal message from the same customer's usual correspondents too,
   so the change does not catch good mail.
3. **Save it.** Admin webmail: Pipelines → the pipeline (`/pipelines/<id>`),
   or `PATCH /api/v1/admin/pipelines/{id}` (`pipelines:write`). Custom
   filters are saved on their own page.
4. **Spam already delivered** stays where it is; ask the customer to move it
   to Junk, or delete it from their mailbox.

## Verify

- Run the pipeline test again with the spam sample: `final_action` is now
  `reject` or `quarantine`.
- Watch `/pipelines/logs` filtered by `outcome=quarantined` or `rejected` for
  the next messages of the same kind.
- Quarantined mail for the customer is in their quarantine
  (`GET /api/v1/accounts/{id}/quarantine`); a false positive can be released
  with `POST /api/v1/accounts/{id}/quarantine/{mid}/release`.
- Ask the customer after a day whether the spam has stopped.

## Escalate

- The trace shows a filter misbehaving (an error, a timeout, a sidecar not
  answering): check the sidecar's logs (`chore server:logs js-filter` for
  custom filters) and open an issue with the trace (remove the addresses).
- Spam passes every filter at a normal score: the rspamd rules may be out of
  date; to whoever runs the rspamd sidecar.
