# Instant Mail Check — Design Document

**Date:** 2026-02-22
**Status:** Draft
**Binary:** `cmd/instantmailcheck/main.go`
**Package:** `internal/mailcheck/`

## Overview

A standalone CLI tool that performs comprehensive mail server diagnostics from a
single command. Designed as a free downloadable utility to promote rest-mail.

Zero dependencies on the rest-mail API, database, or any running services — it
is a pure client-side probe tool.

## Usage

```bash
# Tier 1: Public audit (no credentials needed)
instantmailcheck example.com

# Tier 1 + DKIM selector check
instantmailcheck example.com --dkim-selector default

# Tier 2: Send a test email to verify delivery
instantmailcheck example.com --send-to test@example.com

# Tier 3: Full authenticated round-trip
instantmailcheck example.com \
  --user test@example.com \
  --pass secret \
  --send-to test@example.com

# JSON output for scripting/CI
instantmailcheck example.com --json

# Verbose mode
instantmailcheck example.com -v
```

## Philosophy

This is an **admin's own-server audit tool**, not a remote scanner. The admin
running this tool owns the server and can provide credentials. This means we can
test things that no external tool can: authenticated logins, email round-trips,
header analysis, user enumeration resistance, brute-force protection, and more.

The goal is to **fingerprint issues, find problems, offer solutions, explain
security holes, and test for things that attackers use to exploit mail servers**.

## Three Tiers

### Tier 1 — Public Probe (domain only)

DNS checks:
- MX records
- SPF record (parse + validate policy strength)
- DMARC record (parse + validate policy: none/quarantine/reject)
- DKIM record (try common selectors or user-specified)
- DANE/TLSA records
- MTA-STS (TXT + HTTPS policy fetch)
- TLS-RPT record
- Reverse DNS (PTR) for MX IPs
- SRV records (_submission, _imap, _imaps, _pop3, _pop3s)
- CAA records (Certificate Authority Authorization)

Connection checks:
- SMTP port 25: banner, EHLO capabilities, STARTTLS support
- SMTP port 587: banner, STARTTLS, requires AUTH check
- IMAPS port 993: TLS cert validity
- POP3S port 995: TLS cert validity
- TLS version + cipher for each connection
- Certificate chain, expiry, SANs for each TLS connection
- Open relay test (RCPT TO foreign address without auth — should reject)

Security checks (unauthenticated):
- SMTP banner information leakage (software version disclosure)
- SMTP VRFY/EXPN command support (user enumeration vectors)
- Insecure port exposure: port 110 (POP3 plaintext), port 143 (IMAP plaintext)
- TLS minimum version (fail if SSLv3/TLS 1.0/1.1 accepted)
- Weak cipher suite detection
- Certificate transparency (warn if self-signed)

Reputation checks:
- IP blacklist (DNSBL) queries: Spamhaus ZEN, Barracuda, SpamCop, etc.

### Tier 2 — Send Test (--send-to)

- Connect to MX on port 25, deliver a test message
- Check for rejection/bounce
- Report SMTP response codes

### Tier 3 — Authenticated Admin Audit (--user + --pass)

Login & protocol checks:
- SMTP AUTH on port 587 (PLAIN + LOGIN mechanisms)
- IMAP login on port 993 + list folders + mailbox stats
- POP3 login on port 995 + STAT

Deliverability round-trip:
- Authenticated send via 587 → IMAP fetch on 993
- Measure end-to-end latency (send → appears in INBOX)
- Verify message arrived intact

Header analysis (on received test message):
- Authentication-Results header (SPF/DKIM/DMARC alignment)
- Received chain analysis (hop count, internal IPs leaked?)
- DKIM signature present and valid?
- List-Unsubscribe header presence (deliverability signal)
- Message-ID format (properly formed?)

Security audit (authenticated):
- SMTP AUTH over plaintext (port 25 without STARTTLS) — should be rejected
- AUTH PLAIN vs AUTH LOGIN vs CRAM-MD5 mechanism availability
- Failed login response timing (constant-time? or leaks valid usernames)
- IMAP IDLE support (push notifications)
- IMAP namespace check
- Mailbox quota (IMAP GETQUOTAROOT if supported)

### Tier 4 — Exploit Simulation (--security-audit)

These are safe, non-destructive tests that simulate what attackers try:
- SMTP user enumeration via RCPT TO (try invalid user, check response differs)
- SMTP user enumeration via VRFY command
- SMTP pipelining abuse (send commands before response)
- AUTH brute-force rate limiting (3 rapid failed logins — should get blocked)
- Oversized MAIL FROM / RCPT TO (buffer overflow attempt)
- SMTP smuggling test (ambiguous line endings)
- Null sender handling (MAIL FROM:<>)
- Internationalized email address handling (EAI/SMTPUTF8)
- Maximum message size (EHLO SIZE parameter)
- Connection rate limiting (rapid reconnects — should get throttled)

## Architecture

```
cmd/instantmailcheck/
  main.go              — CLI entry point, flag parsing, orchestration

internal/mailcheck/
  report.go            — Report model (CheckResult, Report, scoring, fix suggestions)
  dns.go               — All DNS checks (MX, SPF, DKIM, DMARC, DANE, MTA-STS, PTR, SRV, CAA)
  tls.go               — TLS connection probing + cert inspection + version/cipher checks
  smtp.go              — SMTP banner, EHLO, STARTTLS, open relay, send
  imap.go              — IMAP login, list, fetch, IDLE, quota, namespace
  pop3.go              — POP3 login, stat
  blacklist.go         — DNSBL lookups
  security.go          — Security audit checks (VRFY, EXPN, plaintext ports, banner leak,
                          user enumeration, brute-force rate limit, smuggling, etc.)
  headers.go           — Header analysis (Authentication-Results, Received chain, DKIM sig)
  runner.go            — Orchestrates all checks, builds Report
  display.go           — Terminal output (colored, using lipgloss, with fix suggestions)
```

## Report Model

```go
type CheckResult struct {
    Name        string        // "SPF Record"
    Category    string        // "dns", "tls", "smtp", "imap", "pop3", "reputation"
    Status      Status        // Pass, Warn, Fail, Skip, Error
    Summary     string        // "v=spf1 ip4:1.2.3.4 -all"
    Detail      string        // longer explanation if needed
    Duration    time.Duration // how long the check took
}

type Status int
const (
    StatusPass Status = iota
    StatusWarn
    StatusFail
    StatusSkip
    StatusError
)

type Report struct {
    Domain    string
    MXHosts   []string
    Checks    []CheckResult
    Score     int    // 0-100
    Timestamp time.Time
}
```

## Output Modes

1. **Terminal (default):** Colored output with lipgloss, grouped by category,
   pass/warn/fail indicators, overall score at the bottom.
2. **JSON (--json):** Machine-readable for CI/scripting.
3. **Verbose (-v):** Show full detail for every check.

## Scoring

Simple weighted scoring:
- DNS checks: 40 points (MX 5, SPF 10, DKIM 10, DMARC 10, PTR 5)
- TLS checks: 25 points (cert valid 10, modern TLS 5, STARTTLS 5, submission TLS 5)
- Security: 20 points (no open relay 10, blacklist clean 10)
- Bonus: 15 points (DANE 5, MTA-STS 5, TLS-RPT 5)

## Dependencies

- Go stdlib: `net`, `crypto/tls`, `net/smtp`, `encoding/json`, `flag`
- `github.com/charmbracelet/lipgloss` (already in go.mod) for terminal styling
- No external DNS library needed — `net.Resolver` handles all lookups
- IMAP: minimal client using `net` + `crypto/tls` (raw protocol, no library)
- POP3: minimal client using `net` + `crypto/tls` (raw protocol, no library)

## Build & Distribution

```bash
# Build standalone binary
go build -o instantmailcheck ./cmd/instantmailcheck

# Cross-compile for distribution
GOOS=linux GOARCH=amd64 go build -o instantmailcheck-linux-amd64 ./cmd/instantmailcheck
GOOS=darwin GOARCH=arm64 go build -o instantmailcheck-darwin-arm64 ./cmd/instantmailcheck
GOOS=windows GOARCH=amd64 go build -o instantmailcheck-windows-amd64.exe ./cmd/instantmailcheck
```

## Marketing Footer

Every run prints:
```
─────────────────────────────────────────
  Instant Mail Check by rest-mail
  https://restmail.io
  Tired of mail server pain? Try rest-mail.
─────────────────────────────────────────
```
