# Reference Mail Server

A standard [docker-mailserver](https://github.com/docker-mailserver/docker-mailserver) stack for `ref.test`. Use this as a comparison baseline when verifying that RESTMAIL gateways are indistinguishable from a traditional Postfix + Dovecot deployment.

## Architecture

| Component | Container | IP | Description |
|-----------|-----------|-----|-------------|
| dnsmasq | ref-dnsmasq | 172.21.0.2 | Authoritative DNS for ref.test |
| docker-mailserver | ref-mailserver | 172.21.0.10 | Postfix + Dovecot (all-in-one) |

## Ports (localhost)

| Service | Port | Notes |
|---------|------|-------|
| SMTP (MTA) | 12025 | MX delivery, no auth |
| Submission | 12587 | Port 587 + STARTTLS + AUTH |
| IMAP | 12143 | STARTTLS |
| IMAPS | 12993 | TLS on connect |
| POP3 | 12110 | STARTTLS |
| POP3S | 12995 | TLS on connect |

## Quick Start

```bash
cd reference
docker compose up -d
./setup.sh          # creates alice@ref.test and bob@ref.test
```

## Default Accounts

| Address | Password |
|---------|----------|
| alice@ref.test | password |
| bob@ref.test | password |

## Key Configuration

- **Domain**: `ref.test`
- **SSL**: Self-signed (generated automatically by docker-mailserver on first start)
- **Spam/virus scanning**: disabled (for simplicity)
- **Fail2ban**: disabled (for simplicity)

## What To Verify

### 1. Protocol indistinguishability

Connect to the RESTMAIL SMTP gateway on port 25 and to the reference server on port 12025. Run the same SMTP dialogue and compare:

- `EHLO` capability list (PIPELINING, STARTTLS, SIZE, AUTH mechanisms)
- TLS cipher negotiation behaviour
- Rejection messages (5xx codes, reason text)
- Bounce/DSN format

### 2. Cross-stack delivery

Deliver mail from `ref.test` to `mail3.test` (RESTMAIL domain) and back:

```bash
# Send from reference to RESTMAIL (requires both stacks on the same Docker network)
swaks --to alice@mail3.test --from bob@ref.test \
      --server 172.20.0.13 --port 25
```

### 3. Client auto-discovery

Configure Thunderbird with `alice@ref.test`. Confirm that autoconfig resolves correctly. Then do the same with `alice@mail3.test` and compare the experience.

## Notes

- The reference stack runs on the `172.21.0.0/24` network, separate from the main RESTMAIL `172.20.0.0/16` network.
- To test cross-stack delivery, connect both compose networks with a Docker network alias or use `--network` flags.
- docker-mailserver stores accounts in `/tmp/docker-mailserver/` inside the container (persisted via the `ref-mail-config` volume).
