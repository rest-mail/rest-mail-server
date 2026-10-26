# Deleting a domain

Delete the domain's accounts and aliases first. The API refuses deletion with
`409 domain_not_empty` while either remains.

Deleting an empty domain removes its live pipelines, custom filters, sender
rules, certificates, DKIM rotation keys, MTA-STS policy, outbound relay and
suppressions in the same transaction as the domain. That includes encrypted
relay passwords and rotation key material stored in those rows. Configuration
belonging to other domains, installation settings and shared ACME accounts stay.

Grants at the domain scope and mailbox scopes within it are removed through the
same audited mechanism used when deleting an account. This also ends and audits
eligible grant activations. It removes authority on the deleted resource; it
does not remove roles an account holds elsewhere. The shared permissions cache
is invalidated after the transaction commits. A cleanup or audit failure, or a
cancelled request, rolls all database changes back.

Deletion preserves historical TLS reports, message traces, DMARC aggregates,
rollups and the append-only audit chain. They remain subject to their existing
retention rules. A retained TLS report keeps its original domain ID and policy
domain. A newly created domain with the same name has a new ID and does not
inherit those reports or the removed grants. No historical row is reassigned,
and no extra purge runs as a side effect of deleting live configuration.
