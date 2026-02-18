# PROXY Protocol Support

The RESTMAIL SMTP, IMAP, and POP3 gateways support PROXY protocol v1 (text) and
v2 (binary) for preserving real client IP addresses when running behind a
reverse proxy such as HAProxy or nginx.

Without PROXY protocol, the gateways see the proxy's IP address as the client
address. This breaks per-IP rate limiting, fail2ban-style banning, and
authentication logging. PROXY protocol solves this by letting the proxy prepend
a small header to each TCP connection that carries the original client IP and
port.

## How It Works

The gateways use the [`github.com/pires/go-proxyproto`](https://github.com/pires/go-proxyproto)
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

## Configuration

PROXY protocol is configured via a single environment variable on each gateway
container:

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

Trust the Docker bridge network (useful for the default RESTMAIL compose setup):

```
PROXY_PROTOCOL_TRUSTED_CIDRS=172.20.0.0/16
```

## HAProxy Configuration

HAProxy supports PROXY protocol natively in TCP mode. Below is a complete
example that proxies SMTP (ports 25, 587, 465), IMAP (ports 143, 993), and
POP3 (ports 110, 995) to the RESTMAIL gateways.

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
    server smtp1 172.20.0.13:25 send-proxy-v2

# ── SMTP Submission (port 587) ───────────────────────────────────────
frontend ft_smtp_submission
    bind *:587
    default_backend bk_smtp_submission

backend bk_smtp_submission
    server smtp1 172.20.0.13:587 send-proxy-v2

# ── SMTP Submission TLS (port 465) ───────────────────────────────────
frontend ft_smtp_submission_tls
    bind *:465
    default_backend bk_smtp_submission_tls

backend bk_smtp_submission_tls
    server smtp1 172.20.0.13:465 send-proxy-v2

# ── IMAP (port 143) ─────────────────────────────────────────────────
frontend ft_imap
    bind *:143
    default_backend bk_imap

backend bk_imap
    server imap1 172.20.0.15:143 send-proxy-v2

# ── IMAPS (port 993) ────────────────────────────────────────────────
frontend ft_imaps
    bind *:993
    default_backend bk_imaps

backend bk_imaps
    server imap1 172.20.0.15:993 send-proxy-v2

# ── POP3 (port 110) ─────────────────────────────────────────────────
frontend ft_pop3
    bind *:110
    default_backend bk_pop3

backend bk_pop3
    server pop3_1 172.20.0.16:110 send-proxy-v2

# ── POP3S (port 995) ────────────────────────────────────────────────
frontend ft_pop3s
    bind *:995
    default_backend bk_pop3s

backend bk_pop3s
    server pop3_1 172.20.0.16:995 send-proxy-v2
```

Key points:

- Use `mode tcp` -- HAProxy must **not** inspect or modify the mail protocol
  traffic.
- Use `send-proxy-v2` on each `server` line to send a binary PROXY v2 header.
  You can substitute `send-proxy` for PROXY v1 (text) if needed.
- If HAProxy terminates TLS itself, bind with `ssl crt /path/to/cert.pem` on
  the frontend and remove the implicit-TLS backend ports (465, 993, 995). The
  gateway would then receive plain TCP with a PROXY header.

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
        server 172.20.0.13:25;
    }
    server {
        listen 25;
        proxy_pass smtp_backend;
        proxy_protocol on;
    }

    # ── SMTP Submission (port 587) ──────────────────────────────────
    upstream smtp_submission_backend {
        server 172.20.0.13:587;
    }
    server {
        listen 587;
        proxy_pass smtp_submission_backend;
        proxy_protocol on;
    }

    # ── SMTP Submission TLS (port 465) ──────────────────────────────
    upstream smtp_submission_tls_backend {
        server 172.20.0.13:465;
    }
    server {
        listen 465;
        proxy_pass smtp_submission_tls_backend;
        proxy_protocol on;
    }

    # ── IMAP (port 143) ────────────────────────────────────────────
    upstream imap_backend {
        server 172.20.0.15:143;
    }
    server {
        listen 143;
        proxy_pass imap_backend;
        proxy_protocol on;
    }

    # ── IMAPS (port 993) ───────────────────────────────────────────
    upstream imaps_backend {
        server 172.20.0.15:993;
    }
    server {
        listen 993;
        proxy_pass imaps_backend;
        proxy_protocol on;
    }

    # ── POP3 (port 110) ────────────────────────────────────────────
    upstream pop3_backend {
        server 172.20.0.16:110;
    }
    server {
        listen 110;
        proxy_pass pop3_backend;
        proxy_protocol on;
    }

    # ── POP3S (port 995) ───────────────────────────────────────────
    upstream pop3s_backend {
        server 172.20.0.16:995;
    }
    server {
        listen 995;
        proxy_pass pop3s_backend;
        proxy_protocol on;
    }
}
```

Key points:

- `proxy_protocol on` makes nginx send a PROXY v1 (text) header to the
  upstream.
- The `stream` block is separate from the `http` block. Place it at the top
  level of `nginx.conf`, not inside an `http` block.
- nginx stream does not support PROXY v2. If you require binary v2 headers, use
  HAProxy instead.

## Docker Compose Integration

To add an HAProxy container in front of the RESTMAIL gateways, add it to your
`docker-compose.yml` and set the trusted CIDRs on each gateway to match the
proxy's IP address.

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
        ipv4_address: 172.20.0.30
    depends_on:
      - smtp-gateway
      - imap-gateway
      - pop3-gateway

  # ── SMTP Gateway (no longer exposes ports to host) ─────────────────
  smtp-gateway:
    # ... existing build/image config ...
    environment:
      # ... existing env vars ...
      PROXY_PROTOCOL_TRUSTED_CIDRS: "172.20.0.30/32"
    # Remove host port mappings since HAProxy handles them:
    # ports:
    #   - "25:25"
    #   - "587:587"
    #   - "465:465"
    networks:
      mailnet:
        ipv4_address: 172.20.0.13

  # ── IMAP Gateway ──────────────────────────────────────────────────
  imap-gateway:
    # ... existing build/image config ...
    environment:
      # ... existing env vars ...
      PROXY_PROTOCOL_TRUSTED_CIDRS: "172.20.0.30/32"
    networks:
      mailnet:
        ipv4_address: 172.20.0.15

  # ── POP3 Gateway ──────────────────────────────────────────────────
  pop3-gateway:
    # ... existing build/image config ...
    environment:
      # ... existing env vars ...
      PROXY_PROTOCOL_TRUSTED_CIDRS: "172.20.0.30/32"
    networks:
      mailnet:
        ipv4_address: 172.20.0.16
```

The important change is that `PROXY_PROTOCOL_TRUSTED_CIDRS` is set to the
HAProxy container's static IP (`172.20.0.30/32`). Only PROXY headers arriving
from that address will be honored.

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
{"level":"INFO","msg":"PROXY protocol configured","trusted_cidrs":["172.20.0.30/32"]}
{"level":"INFO","msg":"smtp: PROXY protocol enabled","trusted_cidrs":["172.20.0.30/32"]}
```

Look for these lines in `docker logs smtp-gateway`.

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
} | nc 172.20.0.13 25
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
| Protocols supported | PROXY protocol v1 (text), v2 (binary) |
| Library | `github.com/pires/go-proxyproto` |
| Source file | `internal/gateway/smtp/proxyproto.go` |
| Test file | `internal/gateway/proxyproto_test.go` |
| Default gateway IPs | SMTP: `172.20.0.13`, IMAP: `172.20.0.15`, POP3: `172.20.0.16` |
| SMTP ports | 25 (inbound), 587 (submission), 465 (submission TLS) |
| IMAP ports | 143 (plain/STARTTLS), 993 (implicit TLS) |
| POP3 ports | 110 (plain/STARTTLS), 995 (implicit TLS) |
