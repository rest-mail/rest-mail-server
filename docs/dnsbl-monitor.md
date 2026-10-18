# DNSBL monitor

The API can check the server's own sending IPs against DNS blocklists on a
schedule, and report listings and recoveries in its logs and metrics
(issue #148). It is **off by default**. Off, it creates no resolver and sends no
DNS query.

## Turning it on

Set `RESTMAIL_DNSBL_CONFIG` to the path of a JSON file:

```json
{
  "enabled": true,
  "ips": ["203.0.113.10"],
  "start_delay": "5m",
  "interval": "1h",
  "timeout": "10s",
  "providers": [
    {
      "name": "example",
      "zone": "bl.example.org",
      "listed": ["127.0.0.2/32"],
      "errors": ["127.255.255.0/24"],
      "ipv6": false
    }
  ]
}
```

Nothing has a default, and nothing is discovered:

- `ips`: the public addresses mail actually leaves from. The server does not
  work them out.
- `providers`: each blocklist zone, with the A answers that mean *listed* and
  the A answers that mean *the query failed*. Both are in 127.0.0.0/8 and must
  not overlap. Take them from the provider's own documentation. No provider,
  preset or API key is built in. The zone above is an example only.
- `ipv6`: true only if the zone answers IPv6 (nibble) queries. When it is false,
  an IPv6 address is reported `unsupported`, never clean.
- `start_delay`, `interval`, `timeout`: Go durations. `interval` is at least
  1m and `timeout` is shorter than it. Blocklists rate-limit, so an interval of
  an hour or more and a start delay of a few minutes are sensible.

`"enabled": false`, or no `enabled` at all, leaves the monitor off. The rest of
the file is then not checked, but unknown fields are still refused.

A file that is set but missing, malformed or incomplete stops the API at
startup, before the database or any listener, with
`invalid DNSBL monitor configuration`.

Many blocklists refuse queries that come through large public resolvers, and
some answer them with an error code. Check that the API's resolver is one the
provider accepts, and list that error code in `errors`.

## What it reports

Logs:

- `dnsbl: sending IP listed` (warn): the first listed answer after unknown or
  clean, with the codes and the provider's reason.
- `dnsbl: sending IP no longer listed` (info): the first clean answer after a
  listing, with the listing it ends.

Metrics:

| Metric | Labels | Meaning |
|---|---|---|
| `restmail_dnsbl_checks_total` | provider, status | every completed check |
| `restmail_dnsbl_listed` | ip, provider | 1 listed, 0 not listed, **absent** while unknown |
| `restmail_dnsbl_last_answer_timestamp_seconds` | ip, provider | when the last listed or not-listed answer came |

`provider_error`, `indeterminate` and `unsupported` answers are counted but do
not change `restmail_dnsbl_listed`: an error never clears a listing and never
makes an unknown IP look clean. A check cut short by shutdown is not counted.
`monitoring/alerting-rules.yml` alerts on a listing separately from a provider
that is not answering.

## Durable history

Startup auto-migration adds `dnsbl_listing_events`. Each listing writes one
`listed` row; the first authoritative clean answer after it writes a `delisted`
row. `at` is the event time and `since` is the listing's first-seen time (on
both rows). Each row also records the sending IP, provider name, zone, answer
codes (JSON), TXT reason and DNS query. Unchanged results and provider errors
write no rows. A first clean answer has no listing to resolve and writes none.

Before starting queries, the API loads the latest event by ID for each
configured IP/provider/zone. This preserves listings and their first-seen time
across restarts: an existing listing is not announced again, and a clean answer
can resolve a listing seen before restart. A different zone starts unknown.
A seed read failure refuses startup. An event write failure stops the monitor
and logs `DNSBL monitor stopped`; repair the database and restart the API.
Writes have a three-second timeout. Metrics still count completed DNS checks.

History is retained indefinitely; there is no automatic pruning. Do not remove
the latest event for a pair: it is also the restart seed. Run one enabled DNSBL
monitor for an instance; this slice does not coordinate multiple monitor owners.
Alert delivery, an admin history view, a circuit breaker and keyed zones remain
outside this feature.
