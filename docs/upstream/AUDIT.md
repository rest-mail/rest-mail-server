# Audit: restmail-specific leakage in `docker/` daemons

**Date:** 2026-04-28
**Scope:** every daemon in `docker/` that's "not ours" — postfix, dovecot, rspamd, clamav, fail2ban, dnsmasq, certgen.
**Purpose:** identify what must be made generic before extracting each into a `reference-*` upstream repo.

## Findings

### `docker/postfix/`

| File | Leak | Required fix before extraction |
|------|------|-------------------------------|
| `conf/main.cf.tmpl:16` | `mynetworks = 127.0.0.0/8 10.99.0.0/16` — hardcoded testbed subnet | Template via `${POSTFIX_MYNETWORKS}` env var, default `127.0.0.0/8 [::1]/128` |
| `entrypoint.sh:10-12` | DB defaults `restmail/restmail/restmail` | Defaults removed — env vars become required (or default to `mail/mail/mail`) |
| `entrypoint.sh:35` | `cp /certs/ca.crt /usr/local/share/ca-certificates/restmail-ca.crt` | Use `${CA_NAME:-local}-ca.crt` |

### `docker/dovecot/`

| File | Leak | Required fix |
|------|------|--------------|
| `entrypoint.sh:9-11` | DB defaults `restmail/restmail/restmail` | Same as postfix — remove or neutralize |
| `entrypoint.sh:31` | `restmail-ca.crt` | Same as postfix |

### `docker/rspamd/`

Clean. No restmail-specific references found. Direct extraction candidate.

### `docker/clamav/`

Not yet inspected (not present as a directory — service uses the upstream image directly per current compose). When extracting, the image itself is upstream `clamav/clamav` or similar; only any custom config we add belongs in `reference-clamav`.

### `docker/dnsmasq/`

The **entire `dnsmasq.conf`** is testbed data, not image data:

- IP addresses: `10.99.0.11`, `10.99.0.12`, `10.99.0.13`, `10.99.0.20`, `10.99.0.41-43`
- Domain definitions: `mail1.test`, `mail2.test`, `mail3.test`, `postgres-mail1-3`, `api`
- MX/SPF/DMARC/DKIM records for `*.test`
- Autoconfig/autodiscover/MTA-STS subdomain entries
- SRV records for IMAP/POP3/Submission

**Conclusion:** the `reference-dnsmasq` image is just upstream dnsmasq + a clean entrypoint that reads `/etc/dnsmasq-overlay/`. The current `dnsmasq.conf` is **testbed configuration** and moves to `testbed/configs/dnsmasq.conf`. The image knows nothing about restmail or `*.test`.

### `docker/fail2ban/`

Genuinely restmail-specific:

- `Dockerfile:8` — `mkdir -p /var/log/restmail`
- `jail.local` — three jails (`restmail-smtp`, `restmail-imap`, `restmail-pop3`) targeting `/var/log/restmail/<gateway>.log`

**Conclusion:** the `reference-fail2ban` image is generic upstream fail2ban with overlay support. The restmail-specific jail definitions and filter regexes move *into the restmail repo* (likely `docker/fail2ban-config/`) and get mounted into the generic image at runtime. Same model as dnsmasq.

### `docker/certs/` (certgen)

Not yet inspected in detail. Likely needs minor genericization (cert names, default CN strings) but is otherwise testbed-only infrastructure.

## Summary

| Daemon | Effort to make generic | Notes |
|--------|------------------------|-------|
| rspamd | trivial | already clean |
| clamav | trivial | upstream image as-is |
| postfix | small | three env-var substitutions in template + entrypoint |
| dovecot | small | same pattern as postfix |
| certgen | small (TBD) | needs full inspection |
| dnsmasq | zero (delete) | move config to testbed; image is vanilla |
| fail2ban | zero (delete) | move jails to restmail; image is vanilla |

The daemons split cleanly into two groups: those with real entrypoint/template logic worth keeping (postfix, dovecot, rspamd, clamav, certgen) and those whose current "image" is really just config-as-image (dnsmasq, fail2ban) — for those, the upstream extraction is little more than a Dockerfile and entrypoint, with all current substance moving to the consuming repo.
