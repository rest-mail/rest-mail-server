# Instant Mail Check — Complete Reference

A standalone CLI tool for comprehensive mail server diagnostics, security auditing, and deliverability testing. Built for administrators testing their own servers.

## Table of Contents

1. [Quick Start](#quick-start)
2. [Why This Tool Exists](#why-this-tool-exists)
3. [Architecture](#architecture)
4. [Design Decisions & Rationale](#design-decisions--rationale)
5. [Tier System](#tier-system)
6. [All Checks Reference](#all-checks-reference)
7. [Scoring System](#scoring-system)
8. [Security Audit Mode](#security-audit-mode)
9. [Output Formats](#output-formats)
10. [Files & Code Map](#files--code-map)
11. [Adding New Checks](#adding-new-checks)
12. [Roadmap](#roadmap)

---

## Quick Start

```bash
# Build (via Taskfile — includes version injection from git)
task build:instantmailcheck

# Or build manually
go build -o instantmailcheck ./cmd/instantmailcheck

# Basic scan (Tier 1 only — no credentials needed)
./instantmailcheck example.com

# With specific DKIM selector
./instantmailcheck example.com --dkim-selector default

# With send test (Tier 2)
./instantmailcheck example.com --send-to test@example.com

# Full authenticated test (Tier 3) — includes round-trip and header analysis
./instantmailcheck example.com --user test@example.com --pass secret --send-to test@example.com

# Security audit mode (Tier 4) — exploit simulation (safe, non-destructive)
./instantmailcheck example.com --security-audit

# Full everything
./instantmailcheck example.com --user test@example.com --pass secret --send-to test@example.com --security-audit -v

# JSON output for CI/scripting
./instantmailcheck example.com --json

# Markdown report (to stdout or file)
./instantmailcheck example.com --markdown
./instantmailcheck example.com --markdown --output report.md

# Filter to specific check categories
./instantmailcheck example.com --checks dns
./instantmailcheck example.com --checks security

# CI/CD with custom pass threshold (exit 2 if below)
./instantmailcheck example.com --threshold 80

# Custom timeout
./instantmailcheck example.com --timeout 15s

# Cross-compile for all platforms
task build:instantmailcheck:all
```

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success — score >= threshold (default 50%) |
| 1 | Invalid arguments (missing domain) |
| 2 | Poor health — score < threshold |

---

## Why This Tool Exists

Running a mail server in 2026 is one of the most complex sysadmin tasks. Unlike a web server where you configure TLS and you're done, a mail server must satisfy dozens of interlocking standards — and getting any one of them wrong means your emails silently vanish into spam folders or get rejected entirely. The problem is that failures are **invisible**: you won't know your SPF is misconfigured until a customer tells you they never got your email.

Existing tools like MXToolbox are web-based, scan from outside, and don't test your server with credentials. They can't tell you if your IMAP IDLE works, if your round-trip delivery takes too long, if your DKIM key is about to be crackable, or if an attacker could enumerate your users via RCPT TO response codes.

**Instant Mail Check exists to give mail server administrators a single command that answers: "Is my mail server configured correctly, secure, and deliverable?"** It does this by testing everything from the outside (like an attacker or another mail server would see) and from the inside (with credentials, like a mail client would experience).

### Why standalone?

The tool is a single Go binary with zero runtime dependencies on the rest-mail API, database, or any external service. This is intentional:

- **You can run it on any mail server**, not just rest-mail servers
- **No network dependency** — it talks directly to DNS and your mail server
- **CI/CD friendly** — just copy the binary, run it, check the exit code
- **Portable** — cross-compiles to Linux/macOS/Windows, no containers needed
- **Trustworthy** — you can read every line of code, no data leaves your machine

---

## Design Decisions & Rationale

### Why 4 tiers?

Mail server testing has fundamentally different levels of access:

1. **Tier 1 (Public Probe)** — Tests what anyone on the internet can see. This is what another mail server checks before deciding whether to accept your email. If these fail, your mail is going to spam or being rejected. These checks need zero credentials because attackers don't have credentials either.

2. **Tier 2 (Send Test)** — Tests actual mail delivery. You need to specify a target address because we're sending a real email. This verifies your server can actually deliver, not just that it's configured correctly on paper.

3. **Tier 3 (Authenticated)** — Tests the mail client experience. With credentials, we can log into IMAP, send authenticated mail, do a round-trip test, and analyze headers on received messages. This catches issues like "DKIM signing isn't actually happening" or "delivery takes 30 seconds."

4. **Tier 4 (Security Audit)** — Simulates attacker techniques. These are more aggressive tests (rapid connections, user enumeration, SMTP smuggling) that a paranoid admin should run but that we don't want running by default because they can trigger fail2ban or IDS alerts.

### Why raw protocol implementations instead of libraries?

We implement SMTP, IMAP, and POP3 at the TCP level using `bufio` instead of using Go SMTP/IMAP libraries. Reasons:

- **Control over error handling** — Libraries abstract away the exact response codes we need to analyze. We need to see the raw `250` vs `550` vs `421` responses to distinguish between "user exists" and "user doesn't exist" for enumeration testing.
- **STARTTLS inspection** — We need to connect in plaintext, inspect the pre-TLS state, then upgrade. Libraries often handle TLS transparently.
- **Intentional misuse** — Security tests like SMTP smuggling require sending malformed data that libraries would refuse to send.
- **Fewer dependencies** — The only external dependency is `lipgloss` for terminal styling and `miekg/dns` for advanced DNS queries. Everything else is Go stdlib.

### Why miekg/dns?

Go's standard library `net.Resolver` can only query A, AAAA, MX, TXT, NS, SRV, and PTR records. It cannot query:
- **TLSA** (type 52) — needed for DANE. DANE lets you pin your TLS certificate in DNS so that even if a CA is compromised, other servers can verify your cert via DNS.
- **CAA** (type 257) — Certificate Authority Authorization, specifying which CAs can issue certs for your domain.
- **DNSSEC flags** — the AD (Authenticated Data) flag tells us whether the DNS response was cryptographically verified. Without DNSSEC, an attacker can spoof DNS responses and undermine SPF, DKIM, and DMARC entirely.

`miekg/dns` is the standard Go library for raw DNS protocol access, maintained since 2010, and used by CoreDNS, Caddy, and many other production tools.

### Why weighted scoring?

Not all checks are equally important. An open relay (weight 10) is a critical emergency — your server will be blacklisted within hours and used to send spam. A missing BIMI record (weight 2) is nice-to-have for brand display in Gmail. The scoring reflects real-world impact:

- **Weight 10**: Open relay, blacklists, SPF, DKIM, DMARC, SMTP TLS cert, round-trip delivery — things that directly cause mail to be rejected or sent to spam
- **Weight 5-8**: STARTTLS, submission port, PTR, DANE, security probes — things that reduce deliverability or expose attack surface
- **Weight 1-3**: POP3S, PIPELINING, SIZE, BIMI — nice-to-haves that indicate good practice

### Why remediation advice (the Fix field)?

A diagnostic tool that tells you "FAIL" without telling you how to fix it is only half useful. Every check that can fail includes a `Fix` field with a concrete, actionable command. We target Postfix and Dovecot because they're the most common self-hosted mail servers, but the advice applies conceptually to any MTA.

### Why the banner leak check?

When your SMTP banner says `220 mail.example.com Postfix 3.5.6`, an attacker now knows exactly which software and version you're running. They can look up CVEs for Postfix 3.5.6 and exploit known vulnerabilities. A generic banner like `220 mail.example.com ESMTP` reveals nothing useful. This is the same principle as removing `Server:` headers from web servers.

### Why test SMTP smuggling?

CVE-2023-51766 (December 2023) was a devastating vulnerability that affected Postfix, Sendmail, and other MTAs. It allowed attackers to bypass SPF, DKIM, and DMARC by exploiting differences in how mail servers interpret line endings. An attacker could send an email that appeared to come from any domain. Our smuggling test reproduces the attack safely to verify your server is patched.

### Why test brute force protection?

If your SMTP server allows unlimited failed login attempts without rate-limiting, an attacker with a list of common passwords can try every one. Combined with user enumeration (which tells them which accounts exist), this is a complete credential compromise pathway. We test this by sending 3 rapid bad AUTH attempts — if the server doesn't block us, it won't block an attacker either.

### Why analyze DKIM key strength?

DKIM works by signing emails with an RSA (or Ed25519) private key. The public key is published in DNS. If the RSA key is too small:
- **512-bit**: Can be factored in minutes on a laptop. Game over.
- **1024-bit**: Theoretically factorable with significant resources. Google deprecated 1024-bit DKIM in 2022. Should upgrade.
- **2048-bit**: Currently secure. Standard recommendation.
- **Ed25519**: Modern elliptic curve, much smaller keys with equivalent security to RSA 3072+.

We parse the actual DKIM public key from DNS and calculate its bit length because many administrators set up DKIM years ago with 1024-bit keys and never upgraded.

---

## Architecture

```
cmd/instantmailcheck/main.go          CLI entry point, flag parsing, version via ldflags
internal/mailcheck/
├── report.go         Data models: CheckResult, Report, Options, Status, ScoreWeights
├── runner.go         Orchestration: Run(opts) executes all checks in order
├── display.go        Terminal output (lipgloss), JSON, Markdown export, check filtering
├── dnsutil.go        Low-level DNS helpers using miekg/dns (TLSA, CAA, DNSSEC queries)
├── dns.go            DNS checks: MX, SPF, DKIM, DMARC, PTR, DANE, MTA-STS, TLS-RPT,
│                     DNSSEC, CAA, BIMI, FCrDNS, IPv6 readiness, autoconfig
├── smtp.go           SMTP checks: banner, STARTTLS, cert, submission, SMTPS 465,
│                     extensions, open relay, send tests
├── tls.go            TLS probing: direct TLS connections, certificate inspection
├── imap.go           IMAP checks: connection, TLS cert, login, capabilities, IDLE, quota
├── pop3.go           POP3 checks: connection, login, STAT
├── blacklist.go      DNSBL: parallel IP + domain blacklist lookups
├── security.go       Security probes: banner leak, VRFY/EXPN, plaintext ports, TLS versions,
│                     self-signed cert, user enumeration (RCPT/VRFY), brute force, smuggling,
│                     rate limiting, auth mechanisms, plaintext auth, password strength
└── headers.go        Header analysis: Authentication-Results, Received chain, DKIM signature,
                      SPF alignment, spam score, ARC chain,
                      round-trip test (send via SMTP + fetch via IMAP)
```

### Dependencies

- `github.com/charmbracelet/lipgloss` — terminal styling (colors, borders)
- `github.com/miekg/dns` — advanced DNS queries (TLSA, DNSSEC) that Go stdlib can't handle
- Go stdlib for everything else: `net`, `crypto/tls`, `crypto/x509`, `bufio`, `encoding/json`, `flag`, `time`, `regexp`, `sync`

### Core Data Types

```go
// Status values (ordered by severity)
StatusPass  = 0  // Check succeeded
StatusWarn  = 1  // Potential issue (scores 50% weight)
StatusFail  = 2  // Failed (scores 0%)
StatusSkip  = 3  // Not applicable / skipped (scores 0%)
StatusError = 4  // Infrastructure failure (scores 0%)

// Every check returns this
type CheckResult struct {
    Name     string        // Display name (must match ScoreWeights key)
    Category string        // Grouping: dns, smtp, tls, imap, pop3, security, reputation, headers, roundtrip
    Status   Status        // Outcome
    Summary  string        // One-line result
    Detail   string        // Extended info (shown in verbose mode)
    Fix      string        // Remediation advice (shown for FAIL/WARN)
    Duration time.Duration // How long the check took
}

// CLI configuration
type Options struct {
    Domain        string        // Required: domain to check
    DKIMSelector  string        // Optional: specific DKIM selector
    SendTo        string        // Optional: email for send tests (Tier 2/3)
    User          string        // Optional: username for auth tests (Tier 3)
    Pass          string        // Optional: password for auth tests (Tier 3)
    Verbose       bool          // Show details for every check
    JSON          bool          // Output as JSON
    SecurityAudit bool          // Run Tier 4 exploit simulation
    Timeout       time.Duration // Per-check timeout (default 10s)
}
```

---

## Tier System

The tool uses a 4-tier system. Higher tiers require more access.

### Tier 1 — Public Probe (domain only)

**No credentials needed.** Tests everything visible from the outside.

| Check | What It Does | Weight |
|-------|-------------|--------|
| MX Records | Looks up MX records, reports count and priorities | 5 |
| SPF Record | Finds SPF TXT record, validates policy (+all, ~all, -all) | 10 |
| DKIM Record | Brute-forces 17 common selectors in parallel, or checks specific one | 10 |
| DMARC Record | Validates p=none/quarantine/reject, checks for rua= reporting | 10 |
| MTA-STS | Checks for `_mta-sts.` TXT record + HTTPS policy (RFC 8461) | 5 |
| TLS-RPT | Checks for `_smtp._tls.` TXT record, validates rua= (RFC 8460) | 5 |
| Reverse DNS (PTR) | Reverse lookup on MX IP | 5 |
| DANE/TLSA | Checks for TLSA records at `_25._tcp.<mx>` via miekg/dns | 5 |
| DNSSEC | Checks AD flag and DNSKEY records for domain | 3 |
| CAA Records | Certificate Authority Authorization — which CAs can issue certs | 2 |
| BIMI Record | Brand Indicators for Message Identification (logo + VMC) | 2 |
| Forward-Confirmed rDNS | Verifies PTR hostname resolves back to original IP | 5 |
| IPv6 Readiness | AAAA records for MX host, IPv6 PTR records | 2 |
| Client Autoconfig | SRV records, Mozilla autoconfig XML, Microsoft Autodiscover | 2 |
| SMTP Banner | Connects to port 25, reads greeting | 3 |
| SMTP STARTTLS | Tests STARTTLS upgrade on port 25 | 5 |
| SMTP TLS Certificate | Inspects cert after STARTTLS (issuer, expiry, SANs, chain) | 10 |
| Submission Port 587 | Tests port 587 for STARTTLS and AUTH | 5 |
| SMTPS Port 465 | Tests implicit TLS on port 465 (RFC 8314) | 3 |
| SMTP Extensions | Parses SIZE, PIPELINING, REQUIRETLS, 8BITMIME, CHUNKING, SMTPUTF8 | 1 |
| IMAPS Port 993 | Connects to IMAPS, reads greeting | 5 |
| IMAPS TLS Certificate | Inspects IMAPS certificate | 5 |
| POP3S Port 995 | Connects to POP3S (optional — skip if unavailable) | 2 |
| Open Relay Test | Attempts unauthenticated relay to external address | 10 |
| IP Blacklists | Parallel DNSBL lookups on 12 major lists | 10 |
| Domain Blacklists | Domain-based blackhole lists (Spamhaus DBL, SURBL, URIBL) | 5 |

**Security checks (also Tier 1, always run):**

| Check | What It Does | Weight |
|-------|-------------|--------|
| Banner Info Leak | Detects software name/version in SMTP banner via regex | 5 |
| VRFY/EXPN Commands | Tests if VRFY and EXPN are enabled (user enumeration vectors) | 5 |
| Plaintext Ports | Checks if ports 110 (POP3) and 143 (IMAP) are open | 8 |
| TLS Minimum Version | Attempts TLS 1.0/1.1 connection + checks cipher strength | 8 |
| Self-Signed Cert | Full chain analysis: self-signed, missing intermediates, SHA-1, key size | 5 |
| Plaintext Auth | Tests if AUTH is advertised on port 25 before STARTTLS | 8 |
| Auth Mechanisms | Enumerates AUTH mechanisms on port 587 (PLAIN, LOGIN, CRAM-MD5) | 3 |

### Tier 2 — Send Test (`--send-to`)

Sends a test email via port 25 (unauthenticated) to the target address.

| Check | What It Does | Weight |
|-------|-------------|--------|
| SMTP Send Test | Delivers test message via port 25 MX relay | 1 |

### Tier 3 — Authenticated (`--user` + `--pass`)

Full account testing with credentials.

| Check | What It Does | Weight |
|-------|-------------|--------|
| Password Strength | Evaluates length, character diversity, common patterns | 3 |
| IMAP Capabilities | Parses CAPABILITY for useful extensions (IDLE, CONDSTORE, etc.) | 2 |
| IMAP IDLE Support | Tests push notification support by issuing IDLE command | 2 |
| Mailbox Quota | GETQUOTAROOT INBOX — reports usage vs limit, warns at 90%+ | 2 |
| IMAP Login | LOGIN on port 993, LIST folders, count | 5 |
| POP3 Login | USER/PASS on port 995, STAT | 3 |
| Authenticated SMTP Send | AUTH PLAIN on port 587, send test email | 5 |
| Email Round-Trip | Send via 587, poll IMAP for arrival, measure latency | 10 |
| Header Analysis | Parse Authentication-Results for SPF/DKIM/DMARC verdicts | 5 |
| SPF Alignment | Verifies envelope sender matches From header (strict + relaxed) | 5 |
| Spam Score Estimate | Checks for missing headers, ALL CAPS, common spam triggers | 3 |
| ARC Chain | Checks ARC-Seal, ARC-Message-Signature, ARC-Authentication-Results | 1 |
| Received Chain | Count hops, detect internal IP leakage (RFC 1918 addresses) | 1 |
| DKIM Signature | Verify DKIM-Signature header present on sent message | 1 |

**Note:** Round-trip test only runs when both `--user`/`--pass` AND `--send-to` are provided (or user sends to self).

### Tier 4 — Security Audit (`--security-audit`)

Exploit simulation checks. Safe and non-destructive, but more aggressive.

| Check | What It Does | Weight |
|-------|-------------|--------|
| User Enumeration (RCPT) | Compares RCPT TO responses for valid vs random addresses | 8 |
| User Enumeration (VRFY) | Compares VRFY responses for valid vs random users | 5 |
| Brute-Force Protection | Sends 3 rapid bad AUTH attempts, checks for blocking/deferral | 10 |
| SMTP Smuggling | Sends ambiguous line endings to detect CVE-2023-51766 style vulns | 8 |
| Rate Limiting | Makes 5 rapid connections, checks for throttling | 5 |

---

## All Checks Reference

### DNS Checks (dns.go)

#### MX Records
- **Why:** MX records tell other mail servers where to deliver email for your domain. Without them, nobody can send you email. This is the most fundamental check — if it fails, everything else is irrelevant.
- **Function:** `CheckMX(ctx, domain) (CheckResult, []string)`
- **How:** `net.DefaultResolver.LookupMX(ctx, domain)`
- **Pass:** At least one MX record found
- **Fail:** No MX records (all subsequent connection checks are skipped)

#### SPF Record
- **Why:** SPF (Sender Policy Framework) tells receiving servers which IP addresses are authorized to send email for your domain. Without SPF, anyone can forge emails as you. Gmail, Microsoft, and Yahoo all check SPF — failing it means your mail goes to spam. We specifically check the `all` mechanism because `+all` (permit all) is worse than having no SPF at all, and `~all` (softfail) provides weaker protection than `-all` (hardfail).
- **Function:** `CheckSPF(ctx, domain) CheckResult`
- **How:** TXT lookup, find record starting with `v=spf1`
- **Pass:** Record with `-all` (hardfail) or `redirect=`
- **Warn:** `~all` (softfail), `?all` (neutral), or no `all` mechanism
- **Fail:** `+all` (allows anyone) or no SPF record

#### DKIM Record
- **Why:** DKIM (DomainKeys Identified Mail) cryptographically signs your outgoing emails. Receiving servers verify the signature against a public key published in your DNS. This proves the email wasn't tampered with in transit and actually came from your server. We brute-force 17 common selectors because there's no standard way to discover which selector a domain uses — you just have to guess. We also analyze the RSA key size because keys smaller than 2048 bits are considered weak (1024-bit DKIM keys have been factored in academic settings).
- **Function:** `CheckDKIM(ctx, domain, selector) CheckResult`
- **How:** Parallel TXT lookup on `<selector>._domainkey.<domain>` for 17 common selectors. Parses the public key to check RSA bit length.
- **Selectors tried:** default, dkim, mail, email, selector1, selector2, s1, s2, k1, google, google2048, everlytickey1, everlytickey2, dkim1, smtp, ses, mailjet
- **Pass:** DKIM record found with strong key (RSA >= 2048 or Ed25519)
- **Warn:** Record found but key is 1024-bit (should upgrade), or no record with common selectors
- **Fail:** Specific selector provided but no record found, or key < 1024 bits

#### DMARC Record
- **Why:** DMARC ties SPF and DKIM together and tells receiving servers what to do when authentication fails. Without DMARC, even if SPF and DKIM fail, the receiving server has no instruction to reject the forgery — it might deliver it anyway. The `p=reject` policy is the strongest: it tells receivers to reject unauthenticated mail entirely. `p=none` is monitoring-only and provides no protection. We also check for `rua=` because without it, you never receive aggregate reports telling you who's sending (or forging) email from your domain.
- **Function:** `CheckDMARC(ctx, domain) CheckResult`
- **How:** TXT lookup at `_dmarc.<domain>`
- **Pass:** `p=quarantine` or `p=reject`
- **Warn:** `p=none` (monitoring only), no `rua=` tag
- **Fail:** No DMARC record found

#### Reverse DNS (PTR)
- **Why:** Reverse DNS maps your mail server's IP address back to a hostname. When your server connects to Gmail, Gmail does a reverse lookup on your IP. If there's no PTR record, or if the PTR hostname doesn't match your server's name, Gmail considers this suspicious — legitimate mail servers always have PTR records. This is one of the most common deliverability problems because PTR records are set by your hosting provider, not in your own DNS zone.
- **Function:** `CheckPTR(ctx, ip) CheckResult`
- **How:** `net.DefaultResolver.LookupAddr(ctx, ip)` on the first MX IP
- **Pass:** PTR record exists
- **Fail:** No PTR record (major deliverability impact)

#### DANE/TLSA
- **Why:** DANE (DNS-based Authentication of Named Entities) publishes your TLS certificate's fingerprint in DNS via TLSA records. This means other mail servers can verify your TLS certificate using DNS instead of trusting the CA system. If a certificate authority is compromised (which has happened — DigiNotar 2011, Symantec 2017), DANE still protects you. It requires DNSSEC to be effective (otherwise an attacker could spoof the TLSA record too). We use `miekg/dns` for proper TLSA type 52 queries because Go's stdlib can't query this record type.
- **Function:** `CheckDANE(ctx, mxHost, timeout) CheckResult`
- **How:** `miekg/dns` query for TLSA records at `_25._tcp.<mx>`. Parses usage, selector, matching type, and certificate association data.
- **Pass:** TLSA record(s) found with valid structure
- **Skip:** No record (DANE is optional, requires DNSSEC)

#### MTA-STS
- **Why:** MTA-STS (RFC 8461) is like HSTS for email. It tells sending servers "you MUST use TLS when delivering mail to me, and here's which MX hosts to use." Without MTA-STS, a network attacker can downgrade STARTTLS connections to plaintext (STARTTLS is opportunistic by default — if it fails, mail is sent unencrypted). We check both the DNS TXT record AND the HTTPS policy file because both are required — the TXT record signals that a policy exists, and the HTTPS policy contains the actual rules.
- **Function:** `CheckMTASTS(ctx, domain, timeout, mxHosts) CheckResult`
- **How:** TXT lookup at `_mta-sts.<domain>`, then HTTP GET to `https://mta-sts.<domain>/.well-known/mta-sts.txt`. Parses mode, mx patterns, max_age. Validates MX patterns match actual MX hosts.
- **Pass:** Record found AND policy fetched with `mode: enforce`
- **Warn:** Policy in `testing` mode, or policy unreachable, or MX patterns don't match
- **Skip:** No MTA-STS record (optional but recommended)

#### TLS-RPT
- **Why:** TLS-RPT (RFC 8460) tells other servers where to send reports when TLS connections to your server fail. Without it, if Gmail can't establish a TLS connection to your server, you'll never know — the email will either be sent in plaintext or not sent at all, silently. With TLS-RPT, you get daily aggregate reports showing exactly what went wrong. We validate the `rua=` reporting address because a TLS-RPT record without a `rua=` field is useless.
- **Function:** `CheckTLSRPT(ctx, domain) CheckResult`
- **How:** TXT lookup at `_smtp._tls.<domain>`, parses `rua=` field, validates mailto: or https: URI
- **Pass:** Record found with valid `rua=` reporting address
- **Warn:** Record exists but missing or malformed `rua=`
- **Skip:** No record (optional but recommended)

### SMTP Checks (smtp.go)

#### SMTP Banner
- **Why:** Port 25 is the standard mail delivery port. If your server doesn't respond with a proper SMTP banner, no other mail server can deliver email to you. The banner also indicates whether your server is properly identifying itself — RFC 5321 requires the server to include its hostname. A missing or broken banner means your mail infrastructure is fundamentally broken.
- **Function:** `CheckSMTPBanner(host, timeout) CheckResult`
- **How:** TCP connect to port 25, read first line
- **Pass:** Banner received (typically `220 mail.example.com ESMTP`)
- **Fail:** Cannot connect or no banner

#### SMTP STARTTLS
- **Why:** STARTTLS upgrades a plaintext SMTP connection to encrypted TLS. Without it, emails between your server and others are sent in cleartext — any network observer can read the content. Since 2014, major providers (Gmail, Microsoft) have been gradually requiring STARTTLS. Google publicly reports which domains don't support it. In 2025+, not supporting STARTTLS is a deliverability red flag. We test both that it's advertised AND that the handshake actually succeeds, because misconfigured TLS (expired cert, wrong hostname) can cause STARTTLS to be advertised but fail, which is worse than not offering it at all.
- **Function:** `CheckSTARTTLS(host, timeout) CheckResult`
- **How:** Connect port 25, EHLO, check for STARTTLS in capabilities, attempt upgrade
- **Pass:** STARTTLS supported and TLS handshake succeeds
- **Warn:** STARTTLS advertised but handshake fails
- **Fail:** STARTTLS not advertised

#### SMTP TLS Certificate
- **Why:** After STARTTLS upgrades the connection, the certificate your server presents matters enormously. An expired certificate, one for the wrong hostname, or one from an untrusted CA will cause other mail servers to either reject the connection or downgrade to plaintext. This is your server's identity proof to the entire email ecosystem. We inspect the full chain: TLS version (must be 1.2+), cipher suite (no weak algorithms), certificate issuer, expiry date, and Subject Alternative Names (the hostnames the cert is valid for).
- **Function:** `CheckSMTPTLSCert(host, timeout) CheckResult`
- **How:** STARTTLS on port 25, inspect certificate chain
- **Reports:** TLS version, cipher suite, issuer, expiry, SANs
- **Pass:** Valid cert, >= TLS 1.2
- **Warn:** Expires in < 7 days
- **Fail:** Verification fails (untrusted CA, wrong hostname)

#### Submission Port 587
- **Why:** Port 587 is the standard email submission port (RFC 6409). This is how your users' mail clients (Thunderbird, Apple Mail, Outlook) send email through your server. Unlike port 25 (which is for server-to-server relay), 587 requires authentication. If port 587 doesn't support STARTTLS, your users' passwords are sent in cleartext. If it's not reachable at all, your users can't send email. Many ISPs block outbound port 25 but allow 587, making this the primary sending port for most users.
- **Function:** `CheckSubmission(host, timeout) CheckResult`
- **How:** Connect port 587, EHLO, check for STARTTLS and AUTH
- **Pass:** STARTTLS supported
- **Warn:** STARTTLS not supported

#### Open Relay Test
- **Why:** An open relay accepts email from anyone and forwards it to anyone — no authentication required. This is the single most critical mail server misconfiguration. Spammers actively scan the internet for open relays and will start using your server to send spam within hours. Once that happens, your IP gets blacklisted on every DNSBL, your legitimate email stops being delivered, and you may face abuse complaints from your hosting provider. We test this by attempting to relay a message through your server from a fake sender to an external address. Your server MUST reject this.
- **Function:** `CheckOpenRelay(host, timeout) CheckResult`
- **How:** Port 25, EHLO, `MAIL FROM:<test@instantmailcheck.example>`, `RCPT TO:<relay-test@example.com>`
- **Pass:** Server rejects relay (550 or similar)
- **Fail:** Server accepts relay (250) — CRITICAL security issue
- **Error:** Cannot complete the test sequence

#### SMTP Send Test (Tier 2)
- **Why:** All the DNS and protocol checks can pass, but the ultimate question is: "Can email actually be delivered to this server?" This test sends a real email through your MX on port 25, exactly like another mail server would when sending you email. If this fails, it means your server is rejecting legitimate inbound mail despite having correct DNS records — usually due to recipient verification, relay restrictions, or content filtering.
- **Function:** `SMTPSendTest(mxHost, sendTo, timeout) CheckResult`
- **How:** Port 25, unauthenticated delivery with test message
- **Pass:** Server accepts message (250)
- **Warn:** RCPT TO rejected (address may not exist)

#### Authenticated SMTP Send (Tier 3)
- **Why:** This tests the complete authenticated sending flow that your mail clients use. It exercises the full chain: connect to 587, upgrade to TLS, authenticate with your credentials, and send a message. Each step can fail independently (port blocked, STARTTLS broken, credentials wrong, sender policy rejection), so we track exactly where the failure occurs. This is the same path a user reports as "I can't send email" — we break it down into diagnostic steps.
- **Function:** `AuthSMTPSend(host, user, pass, sendTo, timeout) CheckResult`
- **How:** Port 587, EHLO, STARTTLS, AUTH PLAIN, MAIL FROM, RCPT TO, DATA
- **Pass:** Message accepted for delivery
- **Fail:** Any step in the sequence fails

### TLS Checks (tls.go)

#### ProbeTLS (shared helper)
- **Why:** IMAPS (993) and POP3S (995) use "implicit TLS" — the connection starts encrypted immediately, unlike SMTP which starts plaintext and upgrades via STARTTLS. This shared helper handles the common pattern of connecting with TLS, inspecting the certificate, and reporting the TLS version and cipher suite. We reuse it to avoid duplicating certificate inspection logic across multiple checks.
- **Function:** `ProbeTLS(host, port, timeout) TLSCheckResult`
- **How:** Direct TLS connection via `tls.DialWithDialer`, inspect cert chain
- **Returns:** TLSCheckResult with TLS version, cipher suite, cert expiry, SANs, issuer
- **Used by:** IMAPS TLS Certificate check

### IMAP Checks (imap.go)

#### IMAPS Port 993
- **Why:** IMAP is how modern email clients (Thunderbird, Apple Mail, mobile apps) access your mailbox. Port 993 is IMAPS — IMAP over implicit TLS. If this port isn't reachable or doesn't respond with a proper IMAP greeting, your users can't read their email. We test the greeting specifically because some firewalls allow the TCP connection but block the IMAP protocol, which confuses mail clients.
- **Function:** `CheckIMAPS(host, timeout) CheckResult`
- **How:** TLS connect to port 993, read IMAP greeting
- **Pass:** Greeting contains "OK" or "IMAP"
- **Fail:** Cannot connect

#### IMAPS TLS Certificate
- **Why:** Your IMAP server's TLS certificate is what your users' mail clients verify when they connect. An expired or untrusted certificate will cause their client to show scary warnings or refuse to connect entirely. This is separate from the SMTP TLS certificate check because mail servers often serve different certificates on different ports (e.g., SMTP might use a cert for `mail.example.com` while IMAP uses one for `imap.example.com`).
- **Function:** `CheckIMAPSTLSCert(host, timeout) CheckResult`
- **How:** Delegates to `ProbeTLS(host, 993, timeout)`

#### IMAP Login (Tier 3)
- **Why:** The port being open and the cert being valid doesn't mean authentication works. This tests the full login flow with real credentials: connect, authenticate, list mailbox folders, and logout cleanly. The folder count tells you if the account is properly provisioned. Login failures here usually indicate credential issues, account lockouts, or ACL problems that your users experience as "I can't check my email."
- **Function:** `IMAPLogin(host, user, pass, timeout) CheckResult`
- **How:** TLS connect, LOGIN, LIST "*", count folders, LOGOUT
- **Pass:** Login succeeds, reports folder count
- **Fail:** Login rejected

### POP3 Checks (pop3.go)

#### POP3S Port 995
- **Why:** POP3 is the legacy mailbox access protocol. While IMAP has largely replaced it, many older clients and automated systems still use POP3. Port 995 is POP3 over implicit TLS. We mark this as a skip (not fail) if the port is closed because not all servers enable POP3 — it's increasingly optional. However, if you claim to support POP3 (e.g., in your documentation or autoconfig), it should actually work.
- **Function:** `CheckPOP3S(host, timeout) CheckResult`
- **How:** TLS connect to port 995, read greeting
- **Pass:** Greeting starts with `+OK`
- **Skip:** Cannot connect (POP3 may not be enabled)

#### POP3 Login (Tier 3)
- **Why:** Like the IMAP login test, this verifies that POP3 authentication actually works with real credentials. POP3's STAT command reports the number of messages and total size, which confirms the account has a working mailbox. POP3 auth failures often have different root causes than IMAP (different authentication backends, different ACLs), so testing both is important.
- **Function:** `POP3Login(host, user, pass, timeout) CheckResult`
- **How:** TLS connect, USER, PASS, STAT, QUIT
- **Pass:** Login succeeds, STAT reports message count

### Security Checks (security.go)

#### Banner Info Leak
- **Why:** Your SMTP banner is the first thing any connecting client sees. If it says `Postfix 3.5.6`, an attacker knows exactly which CVEs to try. Even the software name without a version narrows the attack surface — Postfix, Exim, and Sendmail each have different vulnerability classes. A generic banner like `220 mail.example.com ESMTP` gives away nothing useful. We check against 30+ mail server names because many administrators don't realize their banner reveals this information.
- **Function:** `CheckBannerLeak(host, timeout) CheckResult`
- **How:** Reads SMTP banner on port 25, matches against regex for 30+ mail server names with version numbers
- **Detected software:** Postfix, Dovecot, Exim, Sendmail, Exchange, Zimbra, Cyrus, OpenSMTPD, Haraka, hMailServer, MailEnable, MDaemon, Kerio, Axigen, CommuniGate, Courier, qmail, James, Mercury, SurgeMail, IceWarp, Mimecast, Barracuda, Proofpoint, IronPort, Cisco, Fortinet, Sophos
- **Pass:** No version info in banner
- **Warn:** Software name/version detected

#### VRFY/EXPN Commands
- **Why:** VRFY ("verify") and EXPN ("expand") are legacy SMTP commands from the 1980s that were designed for debugging. VRFY confirms whether a mailbox exists, and EXPN expands mailing list membership. If enabled, an attacker can probe your server to build a list of every valid email address on your domain. This list is then used for targeted phishing, spam, or credential stuffing. There is zero legitimate reason to have these enabled in production.
- **Function:** `CheckVRFYEXPN(host, timeout) CheckResult`
- **How:** Sends `VRFY postmaster` and `EXPN postmaster` on port 25
- **Pass:** Both return 502 (disabled) or error
- **Fail:** Either returns 250/251/252 (leaks user info)

#### Plaintext Ports
- **Why:** Ports 110 (POP3) and 143 (IMAP) transmit everything — including passwords — in plaintext. Any network observer (wifi sniffer, compromised router, ISP) can read the credentials. The encrypted alternatives are POP3S (995) and IMAPS (993). Having plaintext ports open is like having a locked front door but leaving a window open. Even if your users "should" use the encrypted ports, keeping plaintext ports open means a misconfigured client will silently send credentials in the clear. The only safe option is to close them entirely.
- **Function:** `CheckPlaintextPorts(host, timeout) CheckResult`
- **How:** TCP connect to ports 110 and 143
- **Pass:** Both ports are closed
- **Warn:** One or both ports are open (plaintext credential risk)

#### TLS Minimum Version
- **Why:** TLS 1.0 (1999) and TLS 1.1 (2006) have known vulnerabilities: BEAST, POODLE, Lucky13, and others allow attackers to decrypt traffic or downgrade connections. PCI DSS banned TLS 1.0 in 2018. RFC 8996 (2021) formally deprecated both versions. Modern servers should require TLS 1.2 at minimum. We probe by actually attempting a TLS 1.0 and 1.1 handshake — if the server accepts, it's vulnerable. We also check the negotiated cipher suite because even with TLS 1.2, a weak cipher (RC4, DES, 3DES, export-grade) can be broken.
- **Function:** `CheckTLSVersions(host, timeout) CheckResult`
- **How:** STARTTLS on port 25, attempt TLS 1.0 and 1.1 handshakes, also check negotiated cipher strength
- **Pass:** Server rejects TLS 1.0 and 1.1, uses strong ciphers
- **Warn:** Good TLS version but weak cipher negotiated
- **Fail:** Server accepts TLS 1.0 or 1.1

#### Certificate Chain Analysis (Self-Signed Cert)
- **Why:** We perform a comprehensive analysis of your SMTP TLS certificate chain because each issue has different consequences:
  - **Self-signed certificates** cause other mail servers to distrust your TLS entirely. Many providers will fall back to plaintext, which defeats the purpose of having TLS at all. Some providers (like Microsoft) may reject your mail outright.
  - **Missing intermediate certificates** cause chain verification failures even with a valid leaf certificate. The client can't build a trust path to a root CA. This is one of the most common TLS misconfigurations.
  - **SHA-1 signatures** have been deprecated since 2017 because SHA-1 has known collision attacks. Certificates signed with SHA-1 are distrusted by modern software.
  - **RSA keys < 2048 bits** are considered cryptographically weak and can be factored with sufficient resources.
- **Function:** `CheckSelfSignedCert(host, timeout) CheckResult`
- **How:** STARTTLS on port 25, inspect full certificate chain for self-signed, missing intermediates, SHA-1 signatures, key size
- **Pass:** Certificate from trusted CA with proper chain
- **Fail:** Self-signed certificate
- **Warn:** Missing intermediates, SHA-1 signature, or weak key

#### Plaintext Auth
- **Why:** If your SMTP server advertises AUTH mechanisms on port 25 *before* STARTTLS has been negotiated, any client that connects and authenticates will send its username and password in plaintext. A well-behaved client should upgrade to TLS first, but not all clients do — and the server shouldn't even offer the temptation. This is a simple misconfiguration (Postfix: `smtpd_tls_auth_only=yes`) that has outsized security impact. We test by connecting to port 25, sending EHLO, and checking if AUTH appears in the capability list without first doing STARTTLS.
- **Function:** `CheckPlaintextAuth(host, timeout) CheckResult`
- **How:** EHLO on port 25 (without STARTTLS), check for AUTH in capabilities
- **Pass:** AUTH not advertised on plaintext connection
- **Fail:** AUTH available before TLS (credential theft risk)

#### Auth Mechanisms
- **Why:** The AUTH mechanisms your server advertises determine how passwords are transmitted during authentication. PLAIN and LOGIN send the password (base64-encoded, not encrypted) — which is fine over TLS but dangerous without it. CRAM-MD5 and SCRAM-SHA-256 use challenge-response, so the password never crosses the wire even without TLS. We enumerate what's available so you know your authentication security posture. If you only offer PLAIN/LOGIN without TLS, that's a critical issue.
- **Function:** `CheckAuthMechanisms(host, timeout) CheckResult`
- **How:** Connect port 587, STARTTLS, re-EHLO, parse AUTH line
- **Reports:** List of mechanisms (PLAIN, LOGIN, CRAM-MD5, SCRAM-SHA-256, etc.)
- **Pass:** Standard mechanisms available
- **Skip:** No AUTH advertised

#### User Enumeration — RCPT TO (Tier 4)
- **Why:** Spammers and attackers use RCPT TO responses to discover which email addresses exist on your server. If `RCPT TO:<real-user@domain>` returns 250 (OK) but `RCPT TO:<fake-user@domain>` returns 550 (not found), the attacker can probe every possible username and build a list of valid addresses. This list is then used for targeted spear-phishing or credential stuffing. Ideally, your server should return the same response for both valid and invalid recipients during the SMTP transaction, and defer the actual rejection to after DATA.
- **Function:** `CheckUserEnumRCPT(host, domain, timeout) CheckResult`
- **How:** MAIL FROM, then RCPT TO with random address vs `postmaster@domain`. Compare response codes.
- **Pass:** Uniform responses (both 250 or both same code)
- **Warn:** Different codes for valid/invalid users (enables enumeration)

#### User Enumeration — VRFY (Tier 4)
- **Why:** This is a more direct enumeration vector than RCPT TO. The VRFY command explicitly asks "does this user exist?" If your server gives different answers for real vs fake users, it's an oracle that attackers can query. The ideal response is 502 (command disabled) for all inputs. Even returning a consistent "252 cannot verify" is better than leaking which users exist.
- **Function:** `CheckUserEnumVRFY(host, domain, timeout) CheckResult`
- **How:** VRFY with random user vs `postmaster`. Compare response codes.
- **Pass:** Both return 502 (disabled) or uniform response
- **Fail:** Different responses leak user existence

#### Brute-Force Protection (Tier 4)
- **Why:** Without rate limiting on authentication attempts, an attacker with a list of valid users (from enumeration) can try thousands of passwords per minute. Common passwords like `password123`, `company2026`, or leaked credentials from other breaches will succeed against some accounts. This test sends 3 rapid AUTH PLAIN attempts with fake credentials. A well-configured server should respond with 421 (service not available, try again later) or drop the connection after detecting rapid failures. If all 3 attempts succeed without any throttling, your server has no brute-force protection and every account is at risk.
- **Function:** `CheckBruteForceProtection(host, timeout) CheckResult`
- **How:** 3 rapid AUTH PLAIN attempts with bad credentials. Handles both pre-TLS and post-STARTTLS AUTH.
- **Pass:** Server blocks/defers after failed attempts (421/454/451 or connection drop)
- **Warn:** All 3 attempts accepted without blocking

#### SMTP Smuggling (Tier 4)
- **Why:** CVE-2023-51766 (December 2023) showed that most mail servers interpreted line endings differently. SMTP uses `\r\n` (CRLF), but what happens when you send `\n` (bare LF) followed by a dot? Some servers treated this as end-of-message, while the downstream server continued reading — allowing an attacker to inject a completely separate email into the middle of a legitimate one. This bypasses SPF, DKIM, and DMARC entirely because the "smuggled" email inherits the authentication context of the outer email. We test by sending a message with a bare-LF dot sequence and checking if the server processes any commands after it.
- **Function:** `CheckSMTPSmuggling(host, timeout) CheckResult`
- **How:** Sends message with bare-LF dot sequence (`\n.\r\n`) then real end-of-data (`\r\n.\r\n`). Checks if smuggled MAIL FROM command was processed.
- **Pass:** Server correctly handles ambiguous line endings
- **Fail:** Server processed data after bare-LF dot (CVE-2023-51766 vulnerable)

#### Rate Limiting (Tier 4)
- **Why:** Without connection rate limiting, your SMTP server is vulnerable to denial-of-service. An attacker can open thousands of connections per second, exhausting your server's file descriptors and memory, preventing legitimate mail from being delivered. More subtly, rapid connections are also used for mass scanning and brute-force attacks. We test by making 5 rapid connections in sequence. A well-configured server should start rejecting with 421 or 450 after detecting the anomalous connection rate. This is distinct from brute-force protection (which checks auth failures) — rate limiting protects against connection flooding.
- **Function:** `CheckRateLimiting(host, timeout) CheckResult`
- **How:** 5 rapid TCP connections to port 25, check for 421/450 rejection
- **Pass:** Server throttles rapid connections
- **Warn:** All 5 connections accepted without throttling

### Header Analysis & Round-Trip (headers.go)

#### Round-Trip Test (Tier 3)
- **Why:** This is the most realistic test possible. Instead of checking individual components in isolation, it tests the complete email lifecycle: compose → authenticate → send → transit → receive → store → retrieve. This catches integration issues that component tests miss: your SMTP accepts the message but it never arrives in the IMAP inbox (queue stuck), or it arrives but takes 30 seconds (slow DNS lookups in Postfix), or it arrives but with failed authentication headers (DKIM not signing). The round-trip latency itself is a key metric — if it takes more than 5 seconds for email to arrive in the same server's IMAP, something is wrong.
- **Function:** `RoundTripTest(host, user, pass, domain, timeout) []CheckResult`
- **How:**
  1. Sends email via `AuthSMTPSend` on port 587
  2. Polls IMAP for the message (2s intervals, up to 30s)
  3. Fetches latest message, checks if it matches test subject
  4. Analyzes headers on the received message
- **Returns:** Multiple CheckResults — the round-trip result plus header analysis results

#### Header Analysis
- **Why:** Email headers are the forensic record of what happened during delivery. The `Authentication-Results` header tells you whether SPF, DKIM, and DMARC actually passed from the receiving server's perspective — this is the ground truth, not just whether you have the DNS records configured. The `Received` chain shows every server that handled the message, and if any of them leak private IP addresses (10.x, 192.168.x), that reveals your internal network topology to every recipient. The `DKIM-Signature` header confirms that your server is actually signing outgoing mail (having a DKIM DNS record means nothing if your MTA doesn't sign).
- **Function:** `AnalyzeHeaders(rawMessage, domain) []CheckResult`
- **Sub-checks:**
  - `analyzeAuthResults` — Parses `Authentication-Results` header for SPF/DKIM/DMARC pass/fail. **Why:** This is the definitive answer to "will my email pass authentication?" — it's what Gmail, Microsoft, and Yahoo actually see.
  - `analyzeReceivedChain` — Counts hops, detects private IPs (10.x, 172.16-31.x, 192.168.x, fc00:, fe80:). **Why:** Private IP leakage in Received headers is an information disclosure vulnerability and can also trigger spam filters that flag messages with internal-looking routing.
  - `analyzeDKIMSignature` — Checks for DKIM-Signature header, extracts d=, s=, a= fields. **Why:** If d= doesn't match your From domain, or if the signing algorithm is weak (rsa-sha1), your DKIM passes technically but provides weaker authentication guarantees.

### Blacklist Checks (blacklist.go)

#### IP Blacklists
- **Why:** DNS-based Blackhole Lists (DNSBLs) are the primary mechanism that mail servers use to reject spam at connection time. Before your email content is even examined, the receiving server looks up your IP in these lists. If you're listed on Spamhaus ZEN (the most widely used), your mail will be rejected by most of the internet. Listings happen for many reasons: your IP sent spam (either intentionally or because you were hacked), your IP is in a range marked as "dynamic" (residential ISP), or your IP has no reverse DNS. We check 12 major lists in parallel because different providers use different combinations of lists. Being clean on Spamhaus but listed on Barracuda can still cause significant delivery problems.
- **Function:** `CheckBlacklists(ctx, ip) CheckResult`
- **How:** Reverses IP, queries all 12 DNSBLs in parallel using goroutines + WaitGroup
- **Lists checked:**
  1. `zen.spamhaus.org` — Spamhaus ZEN (combines SBL, XBL, PBL)
  2. `b.barracudacentral.org` — Barracuda Central
  3. `bl.spamcop.net` — SpamCop
  4. `dnsbl.sorbs.net` — SORBS main
  5. `spam.dnsbl.sorbs.net` — SORBS spam
  6. `bl.mailspike.net` — MailSpike
  7. `dnsbl-1.uceprotect.net` — UCEProtect Level 1
  8. `psbl.surriel.com` — PSBL
  9. `all.s5h.net` — S5H
  10. `rbl.interserver.net` — InterServer
  11. `dyna.spamrats.com` — SpamRATS dynamic
  12. `noptr.spamrats.com` — SpamRATS no-PTR
- **Pass:** Clean on all lists
- **Fail:** Listed on any list (reports which ones)

#### Domain Blacklists
- **Why:** IP-based blacklists catch your server's IP, but domain-based blacklists (DBLs) check if your *domain name* appears in spam or malicious content. These lists are maintained by Spamhaus (DBL), SURBL, and URIBL. Being on a domain blacklist is often harder to resolve than an IP listing because it implies your domain's reputation is compromised, not just a single IP. Some receiving servers check domain blacklists in addition to IP-based ones, so you need both clean.
- **Function:** `CheckDomainBlacklists(ctx, domain) CheckResult`
- **How:** Parallel DNS lookups against 3 domain-based blacklists (dbl.spamhaus.org, multi.surbl.org, black.uribl.com). Filters for 127.x.x.x responses to avoid false positives from DNS anomalies.
- **Pass:** Clean on all domain blacklists
- **Fail:** Domain listed on any list

### DNS Deep Dive (dns.go — Phases 5-6)

#### DNSSEC
- **Why:** DNSSEC cryptographically signs DNS responses, preventing attackers from forging them. Without DNSSEC, an attacker performing DNS spoofing can redirect your MX records to their own server and intercept all your email. DNSSEC is also required for DANE to be effective — a DANE TLSA record is useless if an attacker can spoof the DNS response containing it. We check the AD (Authenticated Data) flag on DNS responses and look for DNSKEY records.
- **Function:** `CheckDNSSEC(ctx, domain, timeout) CheckResult`
- **How:** Uses `miekg/dns` with DO flag to query for domain, checks AD flag. Falls back to checking for DNSKEY records.
- **Pass:** AD flag set or DNSKEY records present
- **Skip:** No DNSSEC (optional but increasingly recommended)

#### CAA Records
- **Why:** CAA (Certificate Authority Authorization) records specify which certificate authorities are allowed to issue certificates for your domain. Without CAA, any CA on earth can issue a cert for your domain — if one of them has weak validation procedures, an attacker could get a fraudulent cert. CAA is cheap insurance: one DNS record that limits your attack surface. Since September 2017, all CAs are required to check CAA before issuing.
- **Function:** `CheckCAA(ctx, domain, timeout) CheckResult`
- **How:** Uses `miekg/dns` to query CAA records (type 257). Parses tag (issue/issuewild/iodef), value, and flags.
- **Pass:** CAA records present
- **Skip:** No CAA records (optional but recommended)

#### BIMI Record
- **Why:** BIMI (Brand Indicators for Message Identification) displays your company logo next to your emails in supporting mail clients (Gmail, Apple Mail). While purely cosmetic, it requires DMARC enforcement at `p=quarantine` or `p=reject`, so its presence signals strong email authentication. The `l=` field points to an SVG logo, and the optional `a=` field points to a VMC (Verified Mark Certificate) that cryptographically proves you own the logo.
- **Function:** `CheckBIMI(ctx, domain) CheckResult`
- **How:** TXT lookup at `default._bimi.<domain>`, parses `l=` (logo URL) and `a=` (VMC) fields.
- **Pass:** BIMI record found with logo URL
- **Skip:** No BIMI record (optional, requires DMARC enforcement)

#### Forward-Confirmed rDNS (FCrDNS)
- **Why:** Reverse DNS (PTR) maps your IP to a hostname, but that's only half the picture. FCrDNS verifies the circle: your IP → PTR hostname → forward lookup back to your IP. If the forward lookup doesn't match, it means the PTR hostname is lying — either misconfigured or deliberately deceptive. Mail servers like Gmail check FCrDNS as an anti-spoofing measure. Many anti-spam systems treat a broken FCrDNS as a significant negative signal.
- **Function:** `CheckFCrDNS(ctx, ip) CheckResult`
- **How:** Reverse lookup (PTR) on IP, then forward lookup (A/AAAA) on PTR hostname, verifies original IP appears in results.
- **Pass:** Forward lookup of PTR hostname resolves back to original IP
- **Fail:** Forward lookup doesn't match (broken FCrDNS)

#### IPv6 Readiness
- **Why:** IPv6 is increasingly important for email. Google, Microsoft, and other major providers support IPv6 for SMTP. Having AAAA records for your MX host shows you're accessible over IPv6, which can improve delivery paths and reduce dependency on IPv4 (where IP reputation is congested). We also check for IPv6 PTR records because, like IPv4, receiving servers expect reverse DNS for IPv6 addresses.
- **Function:** `CheckIPv6Readiness(ctx, mxHost) CheckResult`
- **How:** Queries AAAA records for MX host. If found, checks IPv6 PTR records.
- **Pass:** AAAA records and IPv6 PTR both present
- **Warn:** AAAA records present but no IPv6 PTR
- **Skip:** No AAAA records (IPv6 not configured)

#### Client Autoconfig
- **Why:** When a user types their email address into Thunderbird, Apple Mail, or Outlook, the client tries to automatically discover mail server settings (IMAP host, SMTP host, ports, encryption). Without autoconfig, users have to manually enter `imap.example.com:993`, `smtp.example.com:587`, etc. — which leads to support tickets and misconfigured clients. We check three discovery mechanisms: SRV records (RFC 6186), Mozilla autoconfig XML, and Microsoft Autodiscover.
- **Function:** `CheckAutoconfig(ctx, domain, timeout) CheckResult`
- **How:** Checks SRV records (`_imaps._tcp`, `_submission._tcp`, `_imap._tcp`, `_pop3s._tcp`), Mozilla autoconfig XML at `autoconfig.<domain>/mail/config-v1.1.xml`, and Microsoft Autodiscover XML.
- **Pass:** At least one autoconfig mechanism found
- **Skip:** No autoconfig (users must configure manually)

### SMTP Advanced (smtp.go — Phase 6)

#### SMTPS Port 465
- **Why:** Port 465 is "implicit TLS" for SMTP submission — the connection starts encrypted immediately, unlike port 587 which starts plaintext and upgrades via STARTTLS. RFC 8314 (2018) re-standardized port 465 after years of ambiguity. Some clients prefer it because there's no risk of a STARTTLS downgrade attack. If you support both 587 and 465, you give clients maximum compatibility.
- **Function:** `CheckSMTPS(host, timeout) CheckResult`
- **How:** Direct TLS connection to port 465, reads SMTP banner.
- **Pass:** Banner received on implicit TLS
- **Skip:** Port not available

#### SMTP Extensions
- **Why:** Modern SMTP extensions improve efficiency, support, and security. SIZE tells clients the maximum message size (so they don't waste bandwidth sending oversized messages). PIPELINING allows multiple commands in one round-trip (reduces latency). REQUIRETLS (RFC 8689) lets senders demand TLS for a message's entire delivery path. 8BITMIME and SMTPUTF8 support international email. We parse the EHLO response to report which extensions your server supports.
- **Function:** `CheckSMTPExtensions(host, timeout) CheckResult`
- **How:** Connects to port 25, sends EHLO, parses capability list for SIZE, PIPELINING, REQUIRETLS, 8BITMIME, CHUNKING, SMTPUTF8.
- **Pass:** Common extensions supported (SIZE + PIPELINING minimum)
- **Warn:** Missing common extensions

### IMAP Advanced (imap.go — Phase 4)

#### IMAP Capabilities
- **Why:** IMAP capabilities tell you what features your server supports beyond basic mail access. CONDSTORE and QRESYNC enable efficient synchronization for mobile clients. NAMESPACE helps clients discover folder hierarchy. COMPRESS=DEFLATE reduces bandwidth for slow connections. MOVE enables atomic message moves (without COPY+DELETE which risks duplicates). SPECIAL-USE identifies standard folders (Sent, Trash, Drafts) automatically. We report all capabilities so you can verify your server supports the features your users need.
- **Function:** `CheckIMAPCapabilities(host, user, pass, timeout) CheckResult`
- **How:** Connects to IMAPS, logs in, sends CAPABILITY command, parses response for known extensions.
- **Pass:** Multiple useful extensions found
- **Warn:** Very few capabilities (basic IMAP only)

#### IMAP IDLE Support
- **Why:** IMAP IDLE is the push notification mechanism for email. Without it, clients must poll the server every few minutes to check for new mail, which wastes battery (mobile), bandwidth, and server resources. With IDLE, the server notifies the client instantly when new mail arrives. Most modern mail clients rely on IDLE for real-time notifications. We don't just check the CAPABILITY list — we actually send the IDLE command and verify the server responds with a continuation prompt.
- **Function:** `CheckIMAPIDLE(host, user, pass, timeout) CheckResult`
- **How:** Connects, logs in, sends IDLE command, checks for `+` continuation response, sends DONE to cleanly exit.
- **Pass:** IDLE command accepted (server supports push notifications)
- **Fail:** IDLE not supported

#### Mailbox Quota
- **Why:** Running out of mailbox quota silently causes incoming mail to bounce. Users often don't notice until someone tells them "I got a bounce message when emailing you." We check quota using GETQUOTAROOT INBOX and report both usage and limit. Warning at 90%+ gives administrators time to act before bounces start.
- **Function:** `CheckIMAPQuota(host, user, pass, timeout) CheckResult`
- **How:** Connects, logs in, sends `GETQUOTAROOT INBOX`, parses STORAGE used/limit.
- **Pass:** Quota available with adequate space
- **Warn:** Usage above 90%
- **Skip:** Server doesn't support QUOTA extension

### Account Security (security.go — Phase 4)

#### Password Strength
- **Why:** The provided test password's strength indicates the minimum password policy your server enforces. We evaluate length (8 minimum, 12 adequate, 16+ good), character class diversity (uppercase, lowercase, digits, special characters), and check against common password patterns (password, 123456, qwerty, etc.). A weak test password suggests the server allows weak passwords in general, which combined with no brute-force protection is a complete compromise pathway.
- **Function:** `CheckPasswordStrength(pass) CheckResult`
- **How:** Evaluates the provided password for length, character classes, common patterns, and repeated characters.
- **Pass:** 16+ characters with 3+ character classes
- **Warn:** 12+ characters or 8+ with good diversity
- **Fail:** Less than 8 characters, or matches common password patterns

### Header Analysis Advanced (headers.go — Phases 3, 6)

#### SPF Alignment
- **Why:** DMARC requires either SPF or DKIM to "align" — meaning the authenticated domain matches the visible From header domain. SPF alignment specifically checks that the envelope sender (Return-Path) domain matches the From domain. Without alignment, SPF can pass but DMARC still fails because the SPF-authenticated domain doesn't match what the user sees. We check both strict alignment (exact domain match) and relaxed alignment (organizational domain match, e.g., `mail.example.com` aligns with `example.com`).
- **Function:** `analyzeSPFAlignment(rawMessage, domain) CheckResult`
- **How:** Extracts Return-Path and From headers, compares domains at strict and relaxed (organizational domain) level. Handles multi-part TLDs (co.uk, com.au, etc.).
- **Pass:** Domains align (strict or relaxed)
- **Warn:** No Return-Path header found (can't verify)
- **Fail:** Domains don't align

#### Spam Score Estimate
- **Why:** Even with perfect authentication, your email can land in spam if the content triggers spam filters. We check for common content-level spam indicators: missing standard headers (Date, Message-ID, MIME-Version), ALL CAPS subject lines, excessive punctuation (!!!), very short body, HTML-only messages (no text/plain alternative), and the presence of X-Spam-Status headers from upstream filters. This gives administrators a heads-up about content issues before sending to real recipients.
- **Function:** `analyzeSpamScore(rawMessage) CheckResult`
- **How:** Parses message for missing headers, content patterns, and upstream spam flags.
- **Pass:** Clean — no spam indicators found
- **Warn:** Minor indicators (missing optional headers, short body)
- **Fail:** X-Spam-Status: Yes or multiple red flags

#### ARC Chain
- **Why:** ARC (Authenticated Received Chain, RFC 8617) preserves authentication results across forwarding hops. When a mailing list or forwarding service relays your email, SPF breaks (the forwarder's IP isn't in your SPF), and DKIM may break (if the list modifies the body or subject). ARC captures the original authentication results at each hop, so the final recipient can trust the chain. Gmail and Microsoft both use ARC to avoid false DMARC failures on forwarded mail.
- **Function:** `analyzeARC(rawMessage) CheckResult`
- **How:** Checks for ARC-Seal, ARC-Message-Signature, ARC-Authentication-Results headers. Parses `cv=` (chain validation) field.
- **Pass:** ARC headers present with valid chain (cv=pass)
- **Warn:** ARC present but chain validation failed (cv=fail)
- **Skip:** No ARC headers (direct delivery, no forwarding)

---

## Scoring System

Each check has a weight in `ScoreWeights` (report.go). Scoring:

| Status | Points |
|--------|--------|
| PASS | 100% of weight |
| WARN | 50% of weight |
| FAIL | 0% |
| SKIP | 0% |
| ERROR | 0% |

**Score** = sum of points earned across all applicable checks
**MaxScore** = sum of weights for all checks that ran
**Percentage** = (Score * 100) / MaxScore

Current total possible points (all tiers, all checks): ~262 points

### Score Display

| Score | Color | Meaning |
|-------|-------|---------|
| >= 80% | Green | Good health |
| >= 50% | Yellow | Needs attention |
| < 50% | Red | Poor health (exit code 2) |

---

## Security Audit Mode

The `--security-audit` flag enables Tier 4 checks that simulate attacker techniques:

1. **User Enumeration (RCPT TO)** — Mimics how spammers discover valid addresses by comparing SMTP response codes for real vs fake users
2. **User Enumeration (VRFY)** — Tests the VRFY command's response differentiation for valid vs invalid users
3. **Brute-Force Protection** — Tests if the server blocks rapid failed login attempts (like fail2ban would)
4. **SMTP Smuggling** — Tests for CVE-2023-51766 style vulnerabilities where ambiguous line endings allow forged emails
5. **Rate Limiting** — Tests if the server throttles rapid connections (common DoS protection)

All tests are **safe and non-destructive**:
- No actual emails are delivered to external addresses
- No real credentials are tested (uses fake/random credentials)
- No persistent state changes on the server
- Connection-level tests only

---

## Output Formats

### Terminal (default)

Colored output grouped by category with lipgloss styling:
- `[PASS]` green, `[WARN]` yellow, `[FAIL]` red, `[SKIP]` gray, `[ERR]` orange
- Categories: DNS Records, SMTP/Submission, TLS Certificates, IMAP, POP3, Security, Reputation, Deliverability, Header Analysis, Skipped
- Remediation advice shown in cyan for FAIL/WARN checks (the `Fix` field)
- Duration shown in gray next to each check

### Verbose (`-v`)

Same as terminal but includes `Detail` field for every check — shows raw responses, full banners, certificate details, etc.

### JSON (`--json`)

Machine-readable output for CI/scripting. Structure:
```json
{
  "domain": "example.com",
  "mx_hosts": ["mail.example.com"],
  "checks": [
    {
      "name": "MX Records",
      "category": "dns",
      "status": "PASS",
      "summary": "1 MX record(s): mail.example.com (priority 10)",
      "duration_ms": 45000000
    }
  ],
  "score": 85,
  "max_score": 100,
  "timestamp": "2026-02-22T14:30:00Z"
}
```

### Markdown (`--markdown`)

Generates a structured Markdown report with:
- Domain name and timestamp
- MX hosts
- Score summary with grade (Good/Needs Attention/Poor)
- Summary counts table (pass/warn/fail/skip)
- Per-category check tables with status, name, summary
- Fix recommendations for failures (verbose mode shows all details)

Output to stdout or file:
```bash
./instantmailcheck example.com --markdown                     # stdout
./instantmailcheck example.com --markdown --output report.md  # file
```

### Check Filtering (`--checks`)

Filter checks by name or category pattern (case-insensitive):
```bash
./instantmailcheck example.com --checks dns        # Only DNS checks
./instantmailcheck example.com --checks security   # Only security checks
./instantmailcheck example.com --checks tls        # TLS-related checks
./instantmailcheck example.com --checks dkim       # DKIM checks only
```

The filter matches against both check names and category names. Score is recalculated after filtering.

### CI/CD Integration (`--threshold`)

Use `--threshold` to set a custom pass/fail score percentage:
```bash
# Strict: fail if below 80%
./instantmailcheck example.com --threshold 80
echo $?  # 0 if >= 80%, 2 if < 80%

# Combined with JSON for machine parsing
./instantmailcheck example.com --json --threshold 75
```

---

## Files & Code Map

### report.go — Data Models

**What it contains:**
- `Status` type (int enum: Pass=0, Warn=1, Fail=2, Skip=3, Error=4)
- `CheckResult` struct — every check returns this
- `Report` struct — the full output with domain, MX hosts, checks, score, timestamp
- `Options` struct — CLI configuration passed to `Run()`
- `ScoreWeights` map — point values keyed by check name
- `CalculateScore()` — iterates checks, applies weights
- `Percentage()` — score as 0-100

**Key pattern:** Check names in `ScoreWeights` must match `CheckResult.Name` exactly. Unknown names default to weight 1.

### runner.go — Orchestration

**What it contains:**
- `Run(opts Options) *Report` — the main orchestration function
- `dnsCtx(timeout)` — creates per-check DNS contexts
- `AuthSMTPSend()` — authenticated SMTP send on port 587
- `base64Encode()` — helper for AUTH PLAIN

**Execution order:**
1. DNS checks (MX, SPF, DKIM, DMARC, MTA-STS, TLS-RPT, DNSSEC, CAA, BIMI, Autoconfig)
2. If MX exists: resolve IPs, PTR, FCrDNS, IPv6, DANE
3. Connection checks (SMTP 25, 587, 465, IMAPS 993, POP3S 995, open relay, SMTP extensions)
4. Security checks (banner, VRFY, plaintext, TLS versions, self-signed, plaintext auth, mechanisms)
5. Blacklists — IP (12 DNSBLs) + Domain (3 DBLs)
6. Tier 2: SMTP send test (if --send-to)
7. Tier 3: Password strength, IMAP capabilities/IDLE/quota, IMAP login, POP3 login, auth SMTP send, round-trip + header analysis (if --user/--pass)
8. Tier 4: User enumeration, brute force, smuggling, rate limiting (if --security-audit)
9. Calculate score

### display.go — Output

**What it contains:**
- `DisplayReport(report, verbose)` — colored terminal output via lipgloss
- `DisplayJSON(report)` — JSON encoder to stdout
- `DisplayMarkdown(report, verbose) string` — Markdown report generation
- `FilterChecks(report, pattern)` — filter checks by name/category, recalculate score
- `statusIcon(s)` — colored `[PASS]`/`[FAIL]` etc.
- `categoryName(cat)` — human-readable category names
- `formatDuration(d)` — µs/ms/s formatting

**Category display order:**
```go
categories := []string{
    "dns", "smtp", "tls", "imap", "pop3",
    "security", "reputation", "roundtrip", "headers", "skip",
}
```

### dnsutil.go — Low-Level DNS

**What it contains:**
- `QueryDNS(domain, qtype, timeout)` — raw DNS query via miekg/dns
- `QueryDNSSEC(domain, timeout)` — DNS query with DO flag for DNSSEC validation
- Shared helper used by DANE, CAA, DNSSEC checks

### dns.go — DNS Checks

All DNS checks take a `context.Context` (with timeout) and return `CheckResult`.

**Helper functions:**
- `ResolveMXIPs(ctx, host)` — resolves IPs for MX host, IPv4 first
- `FirstIPv4(ips)` — extracts first IPv4 from mixed list
- `CommonDKIMSelectors` — 17 common DKIM selectors to brute-force

**Phase 5-6 additions:**
- `CheckDNSSEC(ctx, domain, timeout)` — AD flag + DNSKEY check
- `CheckCAA(ctx, domain, timeout)` — CAA type 257 records via miekg/dns
- `CheckBIMI(ctx, domain)` — `default._bimi.<domain>` TXT lookup
- `CheckFCrDNS(ctx, ip)` — forward-confirmed reverse DNS
- `CheckIPv6Readiness(ctx, mxHost)` — AAAA + IPv6 PTR
- `CheckAutoconfig(ctx, domain, timeout)` — SRV + Mozilla XML + Microsoft Autodiscover

### smtp.go — SMTP Protocol

All SMTP checks take `(host, timeout)` and use raw TCP + bufio.

**Helper functions:**
- `readSMTPResponse(reader)` — reads multi-line SMTP responses (handles `250-` continuations)
- `verifyCert(host, state)` — validates TLS certificate against system CA pool
- `truncateSMTP(s, max)` — truncates long SMTP response strings for display

**Phase 6 additions:**
- `CheckSMTPS(host, timeout)` — implicit TLS on port 465
- `CheckSMTPExtensions(host, timeout)` — EHLO capability parsing

### imap.go — IMAP Protocol

**Phase 4 additions:**
- `CheckIMAPCapabilities(host, user, pass, timeout)` — CAPABILITY command parsing
- `CheckIMAPIDLE(host, user, pass, timeout)` — functional IDLE test
- `CheckIMAPQuota(host, user, pass, timeout)` — GETQUOTAROOT INBOX
- `parseIMAPCapabilities(lines)` — helper to extract capability tokens

### security.go — Security Probes

Contains both always-run Tier 1 security checks and Tier 4 audit checks.

**Pattern:** `versionPattern` regex matches 30+ mail server software names followed by version numbers.

**Phase 4 addition:**
- `CheckPasswordStrength(pass)` — length, character classes, common patterns

### blacklist.go — Blacklist Checks

**Phase 5 addition:**
- `DomainBLs` — list of 3 domain-based blacklists
- `CheckDomainBlacklists(ctx, domain)` — parallel domain-based blacklist lookups

### headers.go — Header Analysis

Contains both the round-trip test orchestration and email header parsing.

**Helper functions:**
- `extractHeader(raw, name)` — extracts first occurrence of a header (handles continuation lines)
- `extractAllHeaders(raw, name)` — extracts all occurrences
- `extractSubfield(headerValue, key)` — parses `key=value` from header content
- `sendTestEmail()` — wrapper around AuthSMTPSend
- `pollIMAPForMessage()` — polls IMAP every 2s for up to 30s
- `fetchLatestIMAPMessage()` — IMAP connect, LOGIN, SELECT INBOX, FETCH latest
- `extractDomainFromAddress(addr)` — extracts domain from email address
- `organizationalDomain(domain)` — handles multi-part TLDs (co.uk, com.au)

**Phase 3/6 additions:**
- `analyzeSPFAlignment(rawMessage, domain)` — envelope vs From domain comparison
- `analyzeSpamScore(rawMessage)` — content-level spam indicator check
- `analyzeARC(rawMessage)` — ARC chain validation

### main.go — CLI Entry Point

**Flags:**
- `--dkim-selector` — specific DKIM selector
- `--send-to` — target email for send tests
- `--user`, `--pass` — credentials for authenticated checks
- `-v` — verbose output
- `--json` — JSON output
- `--markdown` — Markdown output
- `--security-audit` — enable Tier 4 checks
- `--timeout` — per-check timeout (default 10s)
- `--threshold` — minimum score % to pass (default 50)
- `--checks` — filter by name/category pattern
- `--output` — write report to file
- `--version` — show version (set via ldflags at build time)

---

## Adding New Checks

### Step-by-step

1. **Write the check function** in the appropriate file (or create a new `.go` file):
```go
func CheckNewThing(host string, timeout time.Duration) CheckResult {
    start := time.Now()
    result := CheckResult{
        Name:     "New Thing",        // Must match ScoreWeights key
        Category: "security",         // dns/smtp/tls/imap/pop3/security/reputation/headers/roundtrip
    }
    // ... check logic ...
    result.Status = StatusPass // or Warn/Fail/Skip/Error
    result.Summary = "Check passed"
    result.Fix = "How to fix if it fails"  // Optional but recommended
    result.Duration = time.Since(start)
    return result
}
```

2. **Add score weight** in `report.go`:
```go
var ScoreWeights = map[string]int{
    // ...existing...
    "New Thing": 5,  // Must match CheckResult.Name exactly
}
```

3. **Wire into runner** in `runner.go`:
```go
// Add in the appropriate tier section
report.Checks = append(report.Checks, CheckNewThing(primaryMX, opts.Timeout))
```

4. **Add category** if new (in `display.go`):
```go
categories := []string{"dns", "smtp", "tls", "imap", "pop3", "security", "reputation", "new_cat", "skip"}
// And in categoryName():
case "new_cat":
    return "New Category"
```

5. **Add to skip list** if the check depends on MX records (in `runner.go`, the `skipNames` array).

---

## Roadmap

### Completed

**Phase 1 — DNS Enhancements (miekg/dns):**
- [x] Proper DANE/TLSA lookup using miekg/dns (TLSA record type 52)
- [x] MTA-STS: Fetch HTTPS policy, validate mode/mx/max_age, match MX patterns
- [x] TLS-RPT: Parse rua= field, validate mailto:/https: reporting URI

**Phase 2 — Security & TLS Enhancements:**
- [x] TLS cipher audit: probe TLS 1.0 AND 1.1, detect weak ciphers (RC4, DES, 3DES, export-grade)
- [x] Certificate chain deep analysis: self-signed, missing intermediates, SHA-1 signatures, key sizes
- [x] DKIM key strength: parse RSA public key from DNS, check bit length (< 1024 fail, < 2048 warn)

**Phase 3 — Deliverability Enhancements:**
- [x] SPF alignment check (envelope sender vs From header, strict + relaxed with multi-part TLD support)
- [x] Spam score estimation (missing headers, ALL CAPS subject, short body, X-Spam-Status, HTML-only)

**Phase 4 — IMAP & Account Enhancements:**
- [x] IMAP capabilities audit (IDLE, CONDSTORE, QRESYNC, NAMESPACE, COMPRESS, MOVE, SPECIAL-USE, QUOTA)
- [x] IMAP IDLE functional test (sends IDLE command, verifies continuation response, cleanly exits)
- [x] Mailbox quota check (GETQUOTAROOT INBOX, warns at 90%+)
- [x] Password strength evaluation (length, character classes, common patterns, repeated characters)

**Phase 5 — DNS Deep Dive:**
- [x] DNSSEC validation (AD flag + DNSKEY records via miekg/dns)
- [x] CAA record checking (type 257 via miekg/dns, parses issue/issuewild/iodef)
- [x] BIMI record checking (default._bimi TXT, parses l= logo and a= VMC)
- [x] Forward-Confirmed rDNS (PTR → forward lookup → verify IP match)
- [x] IPv6 mail readiness (AAAA records for MX host + IPv6 PTR)
- [x] Domain blacklists (Spamhaus DBL, SURBL, URIBL — parallel lookups, 127.x filter)

**Phase 6 — Advanced Protocol Tests:**
- [x] SMTPS port 465 (implicit TLS, RFC 8314)
- [x] SMTP extensions parsing (SIZE, PIPELINING, REQUIRETLS, 8BITMIME, CHUNKING, SMTPUTF8)
- [x] ARC header analysis (ARC-Seal, ARC-Message-Signature, ARC-Authentication-Results, cv= parsing)
- [x] Client autoconfig (SRV records, Mozilla autoconfig XML, Microsoft Autodiscover)

**Phase 7 — Output & UX:**
- [x] Markdown report export (`--markdown`, `--output` flags)
- [x] CI/CD integration (`--threshold` for custom pass/fail, `--checks` for filtering)
- [x] Check filtering (by name or category pattern, score recalculation)
- [x] Build system: Taskfile with ldflags version injection, cross-compilation (darwin/linux, arm64/amd64)

### Future Improvements

These features are not yet implemented but would add value:

- **Comparison mode** — Multiple domain arguments → side-by-side lipgloss table comparing scores
- **HTML report export** — `--html` flag with inline CSS for sharing via browser
- **Historical tracking** — `--save <file>` to append JSON-lines, `--trend <file>` for ASCII score trend chart
- **Windows cross-compilation** — Add `build:instantmailcheck:windows-amd64` target
- **Parallel check execution** — Run independent checks concurrently (currently sequential within tiers)
- **Custom DNSBL lists** — Allow users to specify additional blacklists via config file
- **SMTP TLS reporting** — Generate TLS-RPT compatible JSON reports from check results
