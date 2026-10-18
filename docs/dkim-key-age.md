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
