# Notification quiet hours

`GET /api/v1/accounts/{id}/quiet-hours` reads an account-local setting with
`mail:read`; `PUT` replaces it with `mail:write`. Both follow the account access
checks used by contact reply-time preferences. Webmail exposes the setting in
the account's Details tab. It is disabled by default, with no schedule selected.

The setting has `enabled`, `start`, `end`, and `time_zone` fields. Times are local
`HH:MM` values, start inclusive and end exclusive. A start later than end crosses
midnight. Enabled schedules require distinct times and a valid IANA zone; empty
times and zone are accepted only while disabled. Go embeds time-zone data so
the dispatcher does not depend on the host's zoneinfo installation.

Scheduling follows actual instants and the selected zone's local clock rather
than adding a fixed number of hours. A skipped end time releases at the first
real instant outside the range. Repeated local times remain quiet whenever
their local clock is in the configured range. Tests include overnight ranges,
both daylight-saving transitions, missing and repeated times, and a half-hour
daylight-saving transition.

## Delivery and durability

An additive migration installs `quiet_notifications` and an inbox-insert
trigger. Creating an unread, non-draft, non-deleted inbox message during quiet
hours inserts its notification record in the same transaction. The message
remains immediately available through the ordinary mail API. The outbox stores
only account/message identifiers, arrival time, and the schedule snapshot; it
stores no sender, subject, body, or device token. Reading or deleting a message
does not erase its arrival from the summary. Deleting the account cascades to
its pending notification records.

The push dispatcher checks quiet hours before both immediate and coalesced
sends. It also reads the durable outbox on startup and at most five seconds
apart, including when no in-memory arrival event survived the restart. A
transaction-scoped PostgreSQL advisory lock serializes summary dispatch across
replicas, following the TLS report worker's existing pattern. Only the records
included in a summary are removed; concurrently arriving mail remains pending.

Each entitled live device receives one summary for its account with the current
`unread` count and an additional `restmail.arrived` count. No message content is
added to the ordinary background-push payload. No outbox record means no summary.
An ordinary notification whose coalescing deadline enters quiet hours is also
held, retaining the ordinary unread-count payload when no quiet-hours arrival
was recorded. Mobile rendering of the summary is work in the separate app
repository. Live mailbox updates are not delayed.

The outbox survives a restart during quiet hours, and the replica lock prevents
two live workers from dispatching the same committed summary. As with existing
periodic external sends, APNs acceptance and the database commit are not one
atomic transaction: a process failure after provider acceptance but before the
outbox deletion commits can replay a send. This is not a claim of exactly-once
delivery across that external-side-effect boundary. No new transport retry or
schedule-edit flush/discard policy is introduced here; pending records retain
their captured schedules.

## Contract verification

The API has named successful, authorization, validation, and storage-failure
example pairs. The live Vertrag coverage test uses the configured fixture hook
to resolve the successful account placeholder and give the compiler's unknown
IANA-format placeholder a real zone. It preserves other probe values and
credentials, and requires zero skipped probes. Both frontend clients are
regenerated from the served description using the CI commands.
