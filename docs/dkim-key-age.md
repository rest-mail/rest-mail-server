# DKIM key age

`GET /api/v1/admin/dkim` lists each domain's current configured signing key,
including domains that are not yet serving mail. Each entry includes
`domain_id`, `domain`, `selector`, `has_key`, `created_at`, and `age_seconds`.
The existing PUT endpoint returns the same entry after installing a key.
Neither endpoint returns private key material. Algorithm and key size are not
stored in the domain model.

`created_at` records installation of the current key, rather than creation of
the domain or generation of an imported key outside REST Mail. New keys installed
through the admin API or `dkim-key ensure` receive this timestamp. Re-importing
identical PEM preserves it; replacing the key records a new installation time.
Master-key re-encryption does not change it. Deleting the key clears it.

Existing keys have no historical installation timestamp. Their `created_at`
and `age_seconds` are both `null`; REST Mail does not infer key age from a
domain's creation or last-update time. An unchanged legacy key remains unknown
when re-imported. Age is clamped to zero if the stored timestamp is in the future.

The API process's existing, network-restricted `/metrics` endpoint exports:

```text
restmail_dkim_key_age_seconds{domain="example.com",selector="mail"} 86400
```

The gauge queries public metadata from the database at each scrape, with a
five-second query timeout. Its labels cover only configured domains and their
current selectors. Retired selectors and removed keys disappear on the next
scrape. Unknown ages have no series; absence must not be interpreted as a fresh
key. Database query failures fail the scrape rather than returning a misleading
empty inventory. The protocol gateway metrics servers continue serving their
process metrics; key inventory is exported only by the API process.

The nullable `domains.dkim_created_at` column is added by the API's existing
additive migration on startup. There is no backfill or configuration change.
Key rotation, DNS publishing and alert rules are separate work tracked in #151.

## Per-domain rotation policy

`GET /api/v1/admin/dkim/{id}/rotation` returns the domain's saved settings:

```json
{"data":{"domain_id":1,"rotation_interval_days":90}}
```

`PUT /api/v1/admin/dkim/{id}/rotation` accepts a single JSON object:

```json
{"rotation_interval_days":90}
```

The integer is a requested key lifetime in days of 24 hours. Zero disables
rotation; values from 1 through 3650 are accepted. Missing, null, negative and
out-of-range values return 422 with `details.fields.rotation_interval_days`.
Malformed JSON, noninteger values, unknown fields and trailing JSON return 400.
Reading requires `domains:read`, writing requires `domains:write`, and the
grant must cover the requested domain. Settings can be saved before a signing
key is installed. Responses contain only the domain ID and the interval.

The API's additive startup migration adds `domains.dkim_rotation_interval_days`
with a non-null default of zero and a database range constraint. New and
existing domains therefore start with rotation disabled. Changing the interval
preserves the current selector, encrypted key and installation timestamp.
Installing or deleting a signing key preserves the saved interval.

These endpoints persist policy for the planned worker; **scheduled rotation is
not implemented yet**. A nonzero interval does not currently generate keys,
publish DNS records or switch signing. The rotation worker, staged and previous
keys, DNS TTL handling, rollback and rotation status remain tracked in #151.
