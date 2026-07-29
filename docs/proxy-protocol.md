# PROXY Protocol Support

The RESTMAIL **SMTP gateway** supports PROXY protocol v1 (text) and v2 (binary)
for preserving real client IP addresses when running behind a reverse proxy such
as HAProxy or nginx.

Without PROXY protocol, the gateway sees the proxy's IP address as the client
address. This breaks per-IP rate limiting, fail2ban-style banning, and
authentication logging. PROXY protocol solves this by letting the proxy prepend
a small header to each TCP connection that carries the original client IP and
port.

> **Scope: SMTP only.** PROXY protocol is implemented for the SMTP gateway
> (`internal/gateway/smtp`). The IMAP and POP3 gateways are thin adapters over
> the external `rest-mail/imap` and `rest-mail/pop3` server libraries and do
> **not** read PROXY headers. If you place a proxy in front of IMAP/POP3,
> forward plain TCP (no `send-proxy` / `proxy_protocol`) — those gateways will
> log the proxy's address as the client IP.

## How It Works

The SMTP gateway uses the [`github.com/pires/go-proxyproto`](https://github.com/pires/go-proxyproto)
library to wrap each TCP listener. The behavior is controlled by a trusted CIDR
policy:

- **Connection from a trusted CIDR**: The PROXY header is parsed. The
  connection's `RemoteAddr` is rewritten to the real client IP from the header.
- **Connection from an untrusted CIDR**: Any PROXY header is silently ignored.
  The connection's `RemoteAddr` remains the direct peer address. The connection
  works normally without interruption.
- **No PROXY header at all**: Connections proceed normally regardless of trust
  status.

This means you can safely enable PROXY protocol support even in mixed
environments where some connections arrive through a proxy and others connect
directly.

The PROXY header is read lazily on each connection's own goroutine (at the first
read/write), not synchronously in the accept loop. Reading the header blocks
until the proxy sends it (or the go-proxyproto header timeout expires), so
resolving the client IP off the accept path keeps one slow or stalled proxy
handshake from delaying acceptance of every other connection. Peer-IP resolution
and the connection-limiter admission both happen at this lazy point.

## Configuration

PROXY protocol is configured via a single environment variable on the SMTP
gateway container:

```
PROXY_PROTOCOL_TRUSTED_CIDRS=<cidr1>,<cidr2>,...
```

The value is a comma-separated list of CIDR ranges. Whitespace around each entry
is trimmed. When this variable is unset or empty, PROXY protocol support is
disabled entirely (no listener wrapping occurs).

### Examples

Trust a single proxy host:

```
PROXY_PROTOCOL_TRUSTED_CIDRS=10.0.0.5/32
```

Trust an entire subnet (e.g., your load balancer tier):

```
PROXY_PROTOCOL_TRUSTED_CIDRS=10.0.1.0/24
```

Trust multiple ranges including IPv6:

```
PROXY_PROTOCOL_TRUSTED_CIDRS=10.0.1.0/24,172.16.0.0/12,fd00::/8
```

Trust the whole testbed network (useful for the default RESTMAIL `mailnet` setup):

```
PROXY_PROTOCOL_TRUSTED_CIDRS=10.99.0.0/16
```

## HAProxy Configuration

HAProxy supports PROXY protocol natively in TCP mode. Below is an example that
proxies SMTP (ports 25, 587, 465) to the SMTP gateway with PROXY headers. IMAP
and POP3 do not read PROXY headers (see Scope above); if you also front them,
forward those ports as plain TCP without `send-proxy`.

```haproxy
global
    log stdout format raw local0
    maxconn 4096

defaults
    log     global
    mode    tcp
    option  tcplog
    timeout connect 10s
    timeout client  300s
    timeout server  300s

# ── SMTP (port 25) ───────────────────────────────────────────────────
frontend ft_smtp
    bind *:25
    default_backend bk_smtp

backend bk_smtp
    server smtp1 10.99.0.13:25 send-proxy-v2

# ── SMTP Submission (port 587) ───────────────────────────────────────
frontend ft_smtp_submission
    bind *:587
    default_backend bk_smtp_submission

backend bk_smtp_submission
    server smtp1 10.99.0.13:587 send-proxy-v2

# ── SMTP Submission TLS (port 465) ───────────────────────────────────
frontend ft_smtp_submission_tls
    bind *:465
    default_backend bk_smtp_submission_tls

backend bk_smtp_submission_tls
    server smtp1 10.99.0.13:465 send-proxy-v2

# ── IMAP / POP3: plain TCP passthrough, NO PROXY header ──────────────
# The IMAP/POP3 gateways do not parse PROXY headers; sending one would be
# interpreted as a protocol command and break the session. Forward them
# without send-proxy (client IP will appear as this proxy's address).
frontend ft_imaps
    bind *:993
    default_backend bk_imaps

backend bk_imaps
    server imap1 10.99.0.15:993       # no send-proxy
```

Key points:

- Use `mode tcp` -- HAProxy must **not** inspect or modify the mail protocol
  traffic.
- Use `send-proxy-v2` on each SMTP `server` line to send a binary PROXY v2
  header. You can substitute `send-proxy` for PROXY v1 (text) if needed.
- Do **not** add `send-proxy`/`send-proxy-v2` to IMAP/POP3 backends — those
  gateways do not read PROXY headers.
- If HAProxy terminates TLS itself, bind with `ssl crt /path/to/cert.pem` on
  the frontend and remove the implicit-TLS backend port (465). The gateway would
  then receive plain TCP with a PROXY header.

## nginx Stream Configuration

nginx can proxy TCP connections and inject PROXY protocol headers using its
`stream` module. This requires nginx compiled with `--with-stream` (included in
the official nginx Docker image).

```nginx
stream {
    log_format proxy '$remote_addr [$time_local] '
                     '$protocol $status $bytes_sent $bytes_received '
                     '$session_time "$upstream_addr"';
    access_log /var/log/nginx/stream.log proxy;

    # ── SMTP (port 25) ──────────────────────────────────────────────
    upstream smtp_backend {
        server 10.99.0.13:25;
    }
    server {
        listen 25;
        proxy_pass smtp_backend;
        proxy_protocol on;
    }

    # ── SMTP Submission (port 587) ──────────────────────────────────
    upstream smtp_submission_backend {
        server 10.99.0.13:587;
    }
    server {
        listen 587;
        proxy_pass smtp_submission_backend;
        proxy_protocol on;
    }

    # ── SMTP Submission TLS (port 465) ──────────────────────────────
    upstream smtp_submission_tls_backend {
        server 10.99.0.13:465;
    }
    server {
        listen 465;
        proxy_pass smtp_submission_tls_backend;
        proxy_protocol on;
    }

    # ── IMAP / POP3: plain TCP passthrough, NO proxy_protocol ──────
    # These gateways do not parse PROXY headers. Omit `proxy_protocol on`
    # so nginx forwards the raw stream (client IP will appear as nginx's).
    upstream imaps_backend {
        server 10.99.0.15:993;
    }
    server {
        listen 993;
        proxy_pass imaps_backend;
    }

    upstream pop3s_backend {
        server 10.99.0.16:995;
    }
    server {
        listen 995;
        proxy_pass pop3s_backend;
    }
}
```

Key points:

- `proxy_protocol on` makes nginx send a PROXY v1 (text) header to the
  upstream. Use it only on the SMTP `server` blocks.
- Do **not** set `proxy_protocol on` for IMAP/POP3 — those gateways do not
  read PROXY headers.
- The `stream` block is separate from the `http` block. Place it at the top
  level of `nginx.conf`, not inside an `http` block.
- nginx stream does not support PROXY v2. If you require binary v2 headers, use
  HAProxy instead.

## Compose / Container Integration

RESTMAIL's dev stack runs each gateway as its own container image on the shared
`mailnet` network (managed via the Taskfile, not a single compose file), but the
pattern below applies to any orchestrator. To put an HAProxy container in front
of the gateways, set the trusted CIDRs on the **SMTP** gateway to match the
proxy's IP address. `PROXY_PROTOCOL_TRUSTED_CIDRS` is only read by the SMTP
gateway; the IMAP/POP3 gateways ignore it.

```yaml
services:
  # ── Load Balancer ──────────────────────────────────────────────────
  haproxy:
    image: haproxy:2.9-alpine
    container_name: haproxy
    restart: unless-stopped
    ports:
      - "25:25"
      - "587:587"
      - "465:465"
      - "143:143"
      - "993:993"
      - "110:110"
      - "995:995"
    volumes:
      - ./haproxy.cfg:/usr/local/etc/haproxy/haproxy.cfg:ro
    networks:
      mailnet:
        ipv4_address: 10.99.0.30
    depends_on:
      - smtp-gateway
      - imap-gateway
      - pop3-gateway

  # ── SMTP Gateway (no longer exposes ports to host) ─────────────────
  smtp-gateway:
    # ... existing build/image config ...
    environment:
      # ... existing env vars ...
      PROXY_PROTOCOL_TRUSTED_CIDRS: "10.99.0.30/32"
    # Remove host port mappings since HAProxy handles them:
    # ports:
    #   - "25:25"
    #   - "587:587"
    #   - "465:465"
    networks:
      mailnet:
        ipv4_address: 10.99.0.13

  # ── IMAP Gateway (no PROXY_PROTOCOL_TRUSTED_CIDRS — not supported) ─
  imap-gateway:
    # ... existing build/image config ...
    networks:
      mailnet:
        ipv4_address: 10.99.0.15

  # ── POP3 Gateway (no PROXY_PROTOCOL_TRUSTED_CIDRS — not supported) ─
  pop3-gateway:
    # ... existing build/image config ...
    networks:
      mailnet:
        ipv4_address: 10.99.0.16
```

The important change is that `PROXY_PROTOCOL_TRUSTED_CIDRS` is set on the SMTP
gateway to the HAProxy container's static IP (`10.99.0.30/32`). Only PROXY
headers arriving from that address will be honored, and only the SMTP gateway
reads the variable at all.

## Security Considerations

**Restrict trusted CIDRs to your actual proxy IPs.** The PROXY protocol header
is unauthenticated -- any TCP client can send one. The trusted CIDR list is the
only mechanism that prevents IP spoofing. If you trust too broad a range, an
attacker connecting from within that range can forge their source address.

Recommendations:

1. **Use /32 (single-host) CIDRs** wherever possible. Trust only the specific
   IP addresses of your load balancers, not entire subnets.

2. **Never trust 0.0.0.0/0.** This would allow any client to spoof their IP
   address, defeating rate limiting, ban lists, and audit logs.

3. **Firewall the gateway ports.** In production, the gateway containers should
   not be directly reachable from the internet. Only the proxy should be able to
   reach them. Use Docker network isolation, security groups, or iptables rules
   to enforce this.

4. **Use PROXY v2 when possible.** The binary v2 format is unambiguous and
   slightly more efficient than the text-based v1. HAProxy supports v2 natively
   via `send-proxy-v2`.

5. **Audit the trust list.** If you change your proxy infrastructure (add/remove
   nodes, change IPs), update `PROXY_PROTOCOL_TRUSTED_CIDRS` on every gateway
   and restart.

## Verification and Testing

### Check that PROXY protocol is enabled

When a gateway starts with `PROXY_PROTOCOL_TRUSTED_CIDRS` set, it logs:

```
{"level":"INFO","msg":"PROXY protocol configured","trusted_cidrs":["10.99.0.30/32"]}
{"level":"INFO","msg":"smtp: PROXY protocol enabled","trusted_cidrs":["10.99.0.30/32"]}
```

Look for these lines in the SMTP gateway logs (`chore smtp-gateway:logs`). The
first line comes from the gateway's startup wiring, the second from the listener
wrapper as each SMTP listener is wrapped.

### Send a test PROXY v1 header manually

You can use `nc` (netcat) to send a raw PROXY v1 header followed by an SMTP
greeting from a trusted source:

```bash
# From a host whose IP is in the trusted CIDR list:
{
  printf "PROXY TCP4 203.0.113.50 198.51.100.1 44123 25\r\n"
  printf "EHLO test.example.com\r\n"
  sleep 2
  printf "QUIT\r\n"
} | nc 10.99.0.13 25
```

If PROXY protocol is working, the gateway will see the client as `203.0.113.50`
in its logs rather than the actual connecting IP.

### Send a PROXY v1 header from an untrusted source

Repeat the same test from an IP outside the trusted CIDR list. The gateway
should ignore the PROXY header and log the actual connecting IP. The SMTP
session may fail because the gateway will try to interpret the PROXY header text
as an SMTP command, which is the expected behavior for untrusted sources
attempting to inject PROXY headers.

### Verify with HAProxy stats

If using HAProxy, enable the stats frontend to confirm connections are flowing:

```haproxy
frontend stats
    bind *:8404
    mode http
    stats enable
    stats uri /stats
    stats refresh 10s
```

Then visit `http://<haproxy-host>:8404/stats` to see backend connection counts
and health status.

### Run the unit tests

The RESTMAIL codebase includes comprehensive tests for PROXY protocol handling:

```bash
go test ./internal/gateway/ -run TestProxy -v
go test ./internal/gateway/ -run TestWrapWithProxyProtocol -v
```

These tests cover:

- PROXY v1 TCP4 and TCP6 header parsing
- PROXY v2 binary header round-trip
- Trusted CIDR connections propagating the real client IP
- Untrusted CIDR connections ignoring the PROXY header
- Connections without any PROXY header working normally
- IPv6 client addresses in PROXY headers
- Valid and invalid CIDR parsing
- Empty CIDR list (all connections use IGNORE policy)

## Reference

| Item | Value |
|------|-------|
| Environment variable | `PROXY_PROTOCOL_TRUSTED_CIDRS` |
| Format | Comma-separated CIDR list |
| Gateways with PROXY support | SMTP only (IMAP/POP3 do not read PROXY headers) |
| Protocols supported | PROXY protocol v1 (text), v2 (binary) |
| Library | `github.com/pires/go-proxyproto` |
| Source file | `internal/gateway/smtp/proxyproto.go` |
| Test file | `internal/gateway/proxyproto_test.go` |
| Default gateway IPs | SMTP: `10.99.0.13`, IMAP: `10.99.0.15`, POP3: `10.99.0.16` |
| SMTP ports | 25 (inbound), 587 (submission), 465 (submission TLS) |
| IMAP ports (plain TCP behind a proxy) | 143 (plain/STARTTLS), 993 (implicit TLS) |
| POP3 ports (plain TCP behind a proxy) | 110 (plain/STARTTLS), 995 (implicit TLS) |
