# Operator runbooks

What to do when a REST Mail server is in trouble. Each runbook gives the
symptoms, how to confirm them, the steps with the commands that exist today,
how to check the fix, and when to escalate.

| Runbook | When |
|---|---|
| [Mail is queuing but not delivering](mail-queuing-not-delivering.md) | Sent mail stays in the queue; recipients say nothing arrived |
| [An IP or domain is on a blocklist](ip-or-domain-blocklisted.md) | A DNSBL alert, or remote servers refuse mail for reputation |
| [The database is lost or corrupt](database-lost-or-corrupt.md) | The API cannot start, or data is missing or wrong |
| [Certificates are about to expire](certificates-expiring.md) | A certificate has days left, or a reload is failing |
| [The API is slow](api-slow.md) | The webmail, the app or the admin webmail are slow to answer |
| [Spam is reaching a customer's inbox](spam-reaching-inbox.md) | A customer forwards spam that got through |
| [A customer reports bouncing mail](customer-bounce.md) | A customer got a bounce, or a recipient says mail never came |

## REST Mail runs in one region

A REST Mail install runs in one place: one database, one API, one set of
gateways, in one cluster. It does not replicate across regions or fail over to
another one. Clients far from that region see the round trip on every API call
and IMAP command, and an outage of that region stops the install until it is
back or restored from a backup ([database runbook](database-lost-or-corrupt.md)).

If your users are spread across continents and latency matters to them, or you
need to keep serving through the loss of a whole region, plan for that before
choosing REST Mail. The same note is in [Installing](../installing.md#one-region).

## Conventions in these runbooks

- `chore …` commands are run from a checkout of this repository on the
  operator's machine, against the install (see [Installing](../installing.md)).
- API calls go to the server's public address. The examples use:

  ```bash
  API=https://mail.example.com
  # an API key made in the admin webmail under API keys (/api-keys);
  # keep it out of shell history and logs
  export RESTMAIL_TOKEN=…
  curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/queue/stats"
  ```

  Each call names the capability it needs, such as `queue:read`. The key's
  account must hold it at a scope that covers what you are looking at.
- Admin webmail paths, such as `/queue`, are pages of the admin webmail.
- "Not available yet (#NNNN)" marks a step that has no command today. The
  issue says what the command should do. Use the workaround given, if any.
- Before anything else when the server may be compromised, see
  [When something is wrong](../installing.md#when-something-is-wrong):
  `chore server:offline` stops everything that answers and keeps the data.
