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
Key rotation is the worker described below; alert rules on key age are still to come (#728).

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

The rotation worker (below) acts on a nonzero interval. Rotation status in
the admin API and the mobile admin, alerts, rollback and manual-key
reconciliation are still to come (#728).

## The rotation worker

The API runs `dkimrotate.Worker` every 15 minutes. It never writes DNS: the
records a rotation needs are in the operator's record list (`restmail-api dns`,
which `tasks/lifecycle/dns.sh` prints and checks), and the worker moves on
when they resolve. Each tick takes every domain with an interval, or with a
rotation under way, one step:

1. **Stage.** A domain whose key has reached its interval gets a staged key
   (`dkimrotate.Stage`, with the DKIM record TTL of one hour). Its record,
   `restmail-<n>._domainkey.<domain>`, joins the record list beside the
   current one.
2. **Wait for publication.** On each tick the worker resolves the staged
   record. The first time it carries the staged key, its publication time is
   recorded. Until then nothing moves.
3. **Activate.** One TTL after publication, `dkimrotate.Activate` re-checks
   the record and makes the staged key the signing key. The old key is kept
   as the previous key, still in the record list.
4. **Retire.** One TTL after the switch, the previous key is let go and its
   record leaves the record list, so mail signed just before the switch has
   had a TTL to be verified.

A step that fails is logged and retried on the next tick. Several API
replicas can run the worker at once: staging and activation lock the
domain's row, and recording publication only fills an empty time.

## Durable rotation key slots

The additive startup migration creates `dkim_rotation_keys` for the planned
worker. The current signing key stays in `domains`; writing a rotation row
cannot change signing. There are no HTTP operations on these slots yet; the
worker (above) uses `dkimrotate.Stage` and `dkimrotate.Activate` (below) for
staging and activation.

Each domain can retain one `staged` key and one `previous` key. A database
constraint rejects other slots, and unique indexes prevent duplicate slots or
reuse of a selector (case-insensitively) across these two slots for the same
domain. Each row stores its selector, private key, original installation time
(nullable for legacy keys), DNS publication time, DNS TTL in seconds, retirement deadline and row
creation/update times. An unpublished key has a null publication time. A staged
key has no retirement deadline; a previous key requires one. TTLs must be
between 1 and 2147483647 seconds. These fields record state; the worker
enforces the waits and verifies propagation before activation.

Private material uses the existing `dkim:v1:` encryption helpers and is excluded
from JSON. The model's `SetPrivateKey` helper follows the current key's
`MASTER_KEY` policy, including the existing development mode without encryption.
The `rotate-key` command re-encrypts both slots along with current DKIM keys,
preserving lifecycle metadata and leaving undecryptable rows untouched while
reporting a failure count. An explicit cascading foreign key removes the slots
when their domain is deleted. No existing domain or key is backfilled or
changed by the migration.

## Staging the next key

`dkimrotate.Stage` is the first step of a rotation. Under
a lock on the domain row it does nothing when the domain has no current key,
its interval is zero, or its key is younger than the interval (a key with an
unknown installation time is due). When a staged key already exists it returns
that key unchanged, so an interrupted rotation resumes with the same key.
Otherwise it generates a 2048-bit RSA key, chooses the next `restmail-<n>`
selector that is not the current or previous selector, checks with
`outbound.CheckDKIMKey` that the domain can sign with it, and saves it encrypted
in the staged slot with the TTL the caller gives. It never publishes DNS or
changes the current key.

## Activating a published key

`dkimrotate.Activate` implements the signing switch. It
locks the domain row, rechecks that rotation is enabled and the current key is
still due, and requires a staged key whose publication time is recorded and at
least one staged TTL in the past. It then decrypts and checks both keys for
signability and uses `Published` with the caller's resolver to confirm that the
staged selector resolves to the matching public key. A missing or mismatched
record returns `ErrNotPublished`; the signing key stays unchanged.

On success a single transaction copies the current selector, ciphertext and
original installation time (including unknown legacy times) to the previous
slot, sets its retirement deadline to the signing switch time plus the
staged TTL, installs the staged selector/ciphertext with a new installation
time, and removes the staged slot. Database failures roll back all three
changes. The caller supplies `time.Now` as the clock and a bounded context for
the DNS lookup and transaction. The clock is read again after DNS verification
so a slow lookup does not consume the old key's retention overlap. Private key
material is neither returned nor logged.

An existing previous slot returns `ErrPreviousKeyRetained`, even when its
retirement deadline has passed: activation never silently discards a rollback
key. A staged selector matching the current selector case-insensitively returns
`ErrSelectorConflict` for explicit reconciliation. Disabling rotation or
replacing the current key with a fresh key prevents activation; existing
staged state is retained for later reconciliation.

Rollback, manual-key reconciliation and rotation status still need to be
implemented. Should REST Mail ever publish records itself, publication must
preserve the entire existing record set:
the current dnsmasq and external-dns providers replace their output when
`EnsureRecords` runs, so passing only a staged TXT record would lose existing
records. Successful provider submission alone also does not prove propagation;
the resolver check is mandatory before activation. During both TTL overlaps,
the worker must preserve the current, staged and retained previous TXT records.
