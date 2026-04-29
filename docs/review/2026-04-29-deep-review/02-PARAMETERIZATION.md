# 02 — Parameterization: spinning up a new domain or instance with one command

This is the headline ask. Right now spinning up a "mail4.test" or a customer-named instance ("acme.example") requires editing 6+ files. The goal: a single command.

```
task instance:new DOMAIN=acme.example
```

…and at the end of that command, you have an SMTP/IMAP/POP3-reachable instance for `acme.example` with sane defaults, joined to the testbed's mailnet, certs auto-issued, DNS records auto-registered with the testbed dnsmasq, and a admin password printed to stdout.

This document walks through:

1. What's hardcoded today (the audit)
2. The proposed parameterization model
3. The end-to-end one-command script (annotated)
4. Migration path — how to get from today to there without breaking things
5. The longer-term: same model for a real public-internet instance, not just the dev testbed

## 1. What's hardcoded today

`grep -rE "mail3.test|10\.99\.0\." --include="*.go" --include="*.yml" .` returns hits in three buckets:

### Bucket A — operational config (the easy bucket)

These are values the Taskfile already centralizes via `MAIL3_*` parent vars:

- IPs: `MAIL3_API_IP=10.99.0.20`, `MAIL3_SMTP_IP=10.99.0.13`, etc.
- Ports: `MAIL3_SMTP_PORT_INBOUND=25`, `MAIL3_API_PORT=8080`, etc.
- Hostname: `MAIL3_HOSTNAME=mail3.test`
- Cert paths: derived from `MAIL3_HOSTNAME`
- DB creds: `MAIL3_DB_NAME`, `MAIL3_DB_USER`, `MAIL3_DB_PASS`
- Container names: `{{.RESTMAIL_PROJECT}}-postgres`, etc.

The good news: the task migration already has all of these as named vars. **Bucket A is parameterized.** What's missing is just a way to *override* them as a group from a CLI invocation.

### Bucket B — code-level constants (the medium bucket)

`grep -r "mail3.test" internal/ tests/ cmd/` shows hits in:

- `tests/e2e/suite_test.go` — test fixture domain hardcoded
- `tests/e2e/stage*_test.go` — same
- `internal/api/handlers/autoconfig.go` — Thunderbird/Outlook autoconfig builds URLs assuming mail3.test
- `internal/api/handlers/mtasts.go` — MTA-STS policy generator hardcodes mail3.test
- `cmd/seed/main.go` — seed data references mail3.test

The bad news: tests rightly hardcode the test substrate's DNS names, but the *production code* shouldn't. Specifically: `autoconfig.go` and `mtasts.go` should derive the hostname from request context (or from a per-request domain identifier), not from a constant.

### Bucket C — DNS substrate (the gnarly bucket)

The testbed dnsmasq has `mail1.test`, `mail2.test`, `mail3.test` baked in via `configs/dnsmasq/zones.conf`. To serve `acme.example`, the dnsmasq needs to know:

- A record: `acme.example` → 10.99.0.X (some assigned IP)
- MX record: `acme.example` → `mail.acme.example`
- SPF TXT: `v=spf1 ip4:10.99.0.X ~all` (or the production equivalent)
- DKIM TXT: `restmail._domainkey.acme.example` → the public key
- DMARC TXT: `_dmarc.acme.example` → `v=DMARC1; p=none; rua=...`
- MTA-STS TXT: `_mta-sts.acme.example` → `v=STSv1; id=...`
- TLS-RPT TXT: `_smtp._tls.acme.example` → `v=TLSRPTv1; rua=...`

That's 7 records per domain. Doing them by hand is an error trap. They should be **rendered from a manifest and pushed to dnsmasq via the testbed's dns:register fragment mechanism**, which already exists (`testbed/Taskfile.yml` has `dns:register` / `dns:unregister`).

## 2. The proposed parameterization model

### A "domain manifest"

Each domain (`mail3.test`, `acme.example`, etc.) gets a manifest YAML at `instances/<domain>/manifest.yml`:

```yaml
# instances/acme.example/manifest.yml
domain: acme.example
hostname: mail.acme.example          # PTR + EHLO name
postmaster: postmaster@acme.example  # autoresponder identity

# Substrate placement
network: testbed_mailnet
ip:
  api: 10.99.0.50
  smtp: 10.99.0.51
  imap: 10.99.0.52
  pop3: 10.99.0.53
  postgres: 10.99.0.54
  js_filter: 10.99.0.55
  webmail: 10.99.0.56
  admin: 10.99.0.57

# Service config
ports:
  api: 18080            # host port — different per instance to avoid collisions
  smtp_inbound: 25
  smtp_submission: 587
  smtp_submission_tls: 465
  imap: 1143
  imap_tls: 1993
  pop3: 1110
  pop3_tls: 1995

# Per-instance secrets (stored in instances/<domain>/secrets.env, gitignored)
# - DB_PASS, JWT_SECRET, MASTER_KEY auto-generated on `task instance:new`

# DNS records to publish to testbed dnsmasq on `task instance:up`
dns_records:
  - { type: A,    name: "acme.example",                  value: "10.99.0.51" }   # MX target
  - { type: MX,   name: "acme.example",                  value: "10 mail.acme.example" }
  - { type: A,    name: "mail.acme.example",             value: "10.99.0.51" }
  - { type: TXT,  name: "acme.example",                  value: "v=spf1 ip4:10.99.0.51 ~all" }
  - { type: TXT,  name: "_dmarc.acme.example",           value: "v=DMARC1; p=none; rua=mailto:dmarc-reports@acme.example" }
  # DKIM and MTA-STS records are added by `task instance:up` from the
  # generated keys / policies, not hardcoded here.

# Admin bootstrap
admin:
  email: admin@acme.example
  # password printed at create time, not stored
```

That's *one file per instance*, gitignored if it contains real customer data, committed if it's a known fixture (mail3.test, mail4.test).

### Var-resolution at task invocation

The main `Taskfile.yml` keeps its `RESTMAIL_*` and `MAIL3_*` vars as defaults but learns to read `instances/<domain>/manifest.yml` and override them. Roughly:

```yaml
includes:
  postgres:
    taskfile: ./tasks/postgres.yml
    vars:
      POSTGRES_PROJECT: '{{.INSTANCE_PROJECT}}'  # rest-mail-mail3, rest-mail-acme, ...
      POSTGRES_NETWORK: '{{.INSTANCE_NETWORK}}'
      POSTGRES_IP: '{{.INSTANCE_POSTGRES_IP}}'
      ...
```

…where `INSTANCE_*` vars are loaded from the manifest by a new `instance:select` task that reads the YAML, exports the vars, and passes them down. Task supports `dotenv:` for loading vars from a file, so this fits cleanly. With one tweak: instead of a single `.env`, point at a per-instance `.env` rendered from the YAML.

```yaml
# Top of Taskfile.yml
vars:
  INSTANCE: '{{.INSTANCE | default "mail3"}}'

dotenv:
  - 'instances/{{.INSTANCE}}/.env'
```

Now `INSTANCE=mail3 task restmail:up` reads `instances/mail3/.env`, `INSTANCE=acme task restmail:up` reads `instances/acme/.env`. One Taskfile, N instances.

## 3. The one-command bring-up

```bash
$ task instance:new DOMAIN=acme.example
```

What happens, step by step:

```
task instance:new
  ├─ instance:scaffold DOMAIN=acme.example
  │    1. Create instances/acme.example/{manifest.yml,secrets.env,configs/}
  │    2. Render manifest.yml from a template, picking unused IPs from
  │       the 10.99.0.0/16 block (scan running containers, find first
  │       unused octet)
  │    3. Render secrets.env with newly-generated DB_PASS, JWT_SECRET,
  │       MASTER_KEY (40+ random bytes each, hex)
  │    4. Render the per-instance .env from manifest.yml (via a small
  │       Go template so the manifest stays declarative)
  │
  ├─ instance:certs:issue DOMAIN=acme.example
  │    1. docker run reference-certgen with CERTGEN_DOMAINS="acme.example
  │       mail.acme.example mta-sts.acme.example", appends to existing CA
  │    2. After the certgen fix in this review, `task certs:issue` is
  │       idempotent and per-domain
  │
  ├─ instance:dkim:keygen DOMAIN=acme.example
  │    1. openssl genrsa for the DKIM private key
  │    2. Compute the DNS public-key TXT record value (base64 of the
  │       public key)
  │    3. Stash private key in instances/acme.example/secrets.env
  │    4. Append TXT record to manifest.yml's dns_records
  │
  ├─ instance:dns:register DOMAIN=acme.example
  │    1. Render manifest.yml's dns_records → dnsmasq addn-hosts /
  │       --txt-record fragments
  │    2. Write fragments to testbed_dnsmasq_fragments volume:
  │       /fragments/acme.example.conf
  │    3. Reload dnsmasq (testbed:dns:reload)
  │
  ├─ instance:up DOMAIN=acme.example
  │    Same as the existing restmail:mail3:up but with INSTANCE=acme.example
  │    1. postgres:up (new container rest-mail-acme.example-postgres)
  │    2. js-filter:up
  │    3. api:up
  │    4. smtp/imap/pop3-gateway:up
  │    5. webmail:up
  │    6. admin:up
  │
  ├─ instance:bootstrap DOMAIN=acme.example
  │    1. Wait for API health
  │    2. POST /api/v1/admin to create the platform admin user from
  │       manifest.admin.email + a generated password
  │    3. POST /api/v1/domains to register acme.example with the API
  │    4. POST /api/v1/dkim to install the keygen'd DKIM key as the
  │       active selector ("restmail._domainkey")
  │    5. POST /api/v1/mtasts to publish the MTA-STS policy
  │    6. Echo the admin password to stdout (and stash a one-time copy
  │       in instances/acme.example/admin-bootstrap.txt with chmod 600
  │       — the user is told to read+delete it)
  │
  └─ instance:smoke DOMAIN=acme.example
       1. nslookup acme.example via testbed dnsmasq
       2. curl -k https://mail.acme.example:8080/api/health
       3. SMTP HELO via openssl s_client
       4. IMAP LOGIN test (one round-trip)
       Print summary banner.
```

Total time end-to-end: maybe 90 seconds (mostly the 30s wait for postgres health + cert generation).

The output the user sees (proposed):

```
$ task instance:new DOMAIN=acme.example
  bootstrapping acme.example
  • allocated IPs: api=10.99.0.50  smtp=10.99.0.51  imap=10.99.0.52  pop3=10.99.0.53
  • generated secrets: DB_PASS=<40-char>, JWT_SECRET=<...>, MASTER_KEY=<...>
  • issued certs: acme.example, mail.acme.example, mta-sts.acme.example
  • generated DKIM key (rsa-2048, selector "restmail")
  • registered DNS records with testbed dnsmasq:
      A     acme.example         → 10.99.0.51
      MX    acme.example         → 10 mail.acme.example
      A     mail.acme.example    → 10.99.0.51
      TXT   acme.example         "v=spf1 ip4:10.99.0.51 ~all"
      TXT   _dmarc.acme.example  "v=DMARC1; p=none; rua=mailto:..."
      TXT   restmail._domainkey.acme.example  "v=DKIM1; k=rsa; p=..."
      TXT   _mta-sts.acme.example  "v=STSv1; id=..."
  • starting containers: 9 (postgres, js-filter, api, smtp, imap, pop3, webmail, admin, website)
  • waiting for postgres to become healthy ✓
  • waiting for api health endpoint ✓
  • created platform admin: admin@acme.example
  • smoke test: dns ✓  api ✓  smtp ✓  imap ✓
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  acme.example ready

  Webmail   →  http://acme.example/webmail
  Admin     →  http://acme.example/admin
  API       →  http://acme.example/api
  SMTP      →  acme.example:25 (inbound), acme.example:587 (submission)
  IMAP      →  mail.acme.example:1993 (TLS)
  POP3      →  mail.acme.example:1995 (TLS)

  Admin bootstrap password: <printed-once-then-deleted>
  (saved to instances/acme.example/admin-bootstrap.txt — read it then `rm` it)

  task instance:down DOMAIN=acme.example   stop everything
  task instance:purge DOMAIN=acme.example  destructive: drop volumes + remove DNS
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

## 4. Migration path — getting from today to there

I'd do this in five small PRs, in order. None of them break existing `mail3.test` workflows.

**PR 1 — `instances/mail3.test/manifest.yml` from the existing hardcoded values.**

Move the current `MAIL3_*` vars from `Taskfile.yml`'s top-level `vars:` block into `instances/mail3.test/manifest.yml`. Add a `dotenv: ['instances/{{.INSTANCE | default "mail3.test"}}/.env']` line. Keep the existing `MAIL3_*` names. `task restmail:up` still works exactly as today.

**PR 2 — `instance:scaffold` task that creates a new instance dir from a template.**

Read DOMAIN, find unused IPs, render manifest.yml + secrets.env. Doesn't bring anything up. Lets you `task instance:scaffold DOMAIN=mail4.test` and inspect the output to verify it's right before you bring it up.

**PR 3 — `instance:dns:register` / `instance:dns:unregister`.**

Renders the manifest's `dns_records` to dnsmasq fragments, writes to the shared volume, reloads dnsmasq. The testbed already has the `dns:register` task pattern; this builds on it. Crucial: use unique fragment file names (`<domain>.conf`) so registering acme.example doesn't stomp on mail3.test.

**PR 4 — production-code dehardcoding.**

Strip mail3.test from `internal/api/handlers/autoconfig.go` and `internal/api/handlers/mtasts.go`. They should derive the hostname from request context (Host header, or an explicit domain query param). Tests stay hardcoded — that's fine.

**PR 5 — `instance:new` umbrella that ties it all together.**

Calls scaffold → certs:issue → dkim:keygen → dns:register → up → bootstrap → smoke. Each step is already a separate task by this point, so this is just orchestration.

Total scope: maybe 1500 lines of code across 5 PRs. Estimable.

## 5. The longer-term: same model for production

Everything above is dev-substrate (the testbed). For a real production instance, two things change:

**(a) DNS isn't dnsmasq.** It's whoever runs the customer's domain — Cloudflare, Route 53, a registrar's nameservers. The `instance:dns:register` task becomes pluggable: a `dns_provider` field in the manifest selects between `testbed-dnsmasq` and `route53` and `cloudflare` and `manual` (which just emits a list of records the user has to add by hand).

**(b) Certs aren't from reference-certgen, they're from Let's Encrypt.** The existing `internal/acme/` package handles this. The `instance:certs:issue` task forks based on the manifest's `cert_provider`: `testbed-certgen` (self-signed) or `letsencrypt`.

The manifest format is unchanged. The two new fields:

```yaml
dns_provider: testbed-dnsmasq    # or: cloudflare, route53, manual
cert_provider: testbed-certgen   # or: letsencrypt-http01, letsencrypt-dns01
```

Same `task instance:new` command works in dev and in prod. The manifest tells the orchestrator which substrate to talk to.

## 6. Things to specifically NOT do

A few well-meaning patterns that fail at scale:

**Don't auto-detect IPs from a "next available" lookup at runtime.** You'll race against yourself, and "what IP did this instance get last week?" becomes a forensics question. Allocate at scaffold time, write to manifest, treat manifest as the source of truth.

**Don't store secrets in manifest.yml.** `secrets.env` is a sibling file, gitignored. Put the manifest in version control if you want; secrets stay local. Production deploys generate secrets on first bring-up and stash them in your secrets manager.

**Don't bake the customer name into container names directly.** Use a stable token (`acme` for `acme.example`, deterministic from the domain via a slug-and-truncate). When the customer renames their domain, the containers don't have to be renamed.

**Don't reuse `INSTANCE` across machines.** Two devs running `INSTANCE=acme task up` on different laptops will both think they own `acme`. Add a `--scope=local|shared` flag if you ever go multi-tenant in dev.

**Don't make `instance:purge` skip a "are you sure?" prompt.** Permanent deletion of a customer's data, even if it's the dev testbed copy, deserves an interactive confirmation. The user has stated "don't delete anything important"; treat instance:purge accordingly.

## 7. What to build first

If you only do *one* thing from this document, do PR 1: move the existing hardcoded `MAIL3_*` values into `instances/mail3.test/manifest.yml`. Everything else builds on that one move. Without it, the rest of the parameterization story is hand-wavy. With it, the next four PRs become incremental and obvious.
