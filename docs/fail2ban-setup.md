# Fail2Ban Setup and Tuning

RestMail provides a two-layer IP ban system: an in-memory rate limiter built into each gateway process, and a persistent database-backed ban table managed via the admin API.

## Architecture

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│ SMTP Gateway │     │ IMAP Gateway │     │ POP3 Gateway │
│              │     │              │     │              │
│ connlimiter  │     │ connlimiter  │     │ connlimiter  │
│ (in-memory)  │     │ (in-memory)  │     │ (in-memory)  │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │                    │                    │
       └────────────┬───────┴────────────────────┘
                    │
              ┌─────▼──────┐
              │  PostgreSQL │
              │  bans table │
              └─────┬──────┘
                    │
              ┌─────▼──────┐
              │  Admin API  │
              │  /api/v1/   │
              │  admin/bans │
              └────────────┘
```

## Layer 1: In-Memory Rate Limiting (connlimiter)

Each gateway process runs an in-memory connection limiter that provides immediate protection without database queries for non-banned IPs.

### Default Configuration

| Parameter         | Default | Description                                   |
|-------------------|---------|-----------------------------------------------|
| `MaxPerIP`        | 20      | Max simultaneous connections per IP            |
| `MaxGlobal`       | 1000    | Max total connections across all IPs           |
| `AuthMaxFails`    | 5       | Auth failures before temporary in-memory ban   |
| `AuthBanWindow`   | 10m     | Window for counting authentication failures    |
| `AuthBanDuration` | 30m     | Duration of in-memory ban after threshold hit  |

### Behavior

1. **Connection limit**: New connections from an IP are rejected if either the per-IP or global limit is reached.
2. **Auth failure tracking**: Each failed LOGIN/AUTH attempt calls `RecordAuthFail(ip)`. Failures older than `AuthBanWindow` are pruned.
3. **Automatic ban**: When failures within the window reach `AuthMaxFails`, the IP is banned in-memory for `AuthBanDuration`.
4. **Reset on success**: A successful authentication clears the failure history for that IP via `ResetAuth(ip)`.

### Tuning

For high-traffic servers, adjust in the gateway main files:

```go
limiter := connlimiter.New(connlimiter.Config{
    MaxPerIP:        50,            // increase for shared NAT
    MaxGlobal:       5000,          // increase for large deployments
    AuthMaxFails:    3,             // stricter for brute-force protection
    AuthBanWindow:   5 * time.Minute,
    AuthBanDuration: 1 * time.Hour, // longer bans for repeat offenders
})
```

## Layer 2: Persistent Database Bans

The `bans` table stores permanent or time-limited bans that survive process restarts and are shared across all gateway instances.

### Database Schema

```sql
CREATE TABLE bans (
    id         SERIAL PRIMARY KEY,
    ip         VARCHAR(45) NOT NULL UNIQUE,  -- IPv4 or IPv6
    reason     TEXT,
    protocol   VARCHAR(10) NOT NULL DEFAULT 'all',  -- smtp, imap, pop3, all
    created_by VARCHAR(255),                         -- admin email or "auto"
    expires_at TIMESTAMP WITH TIME ZONE,             -- NULL = permanent
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);
```

### How It Works

Each gateway process calls `bancheck.Wire(limiter, database, "smtp")` at startup. This sets a `BanChecker` function on the limiter that queries the `bans` table on every new connection:

```go
func Wire(limiter *connlimiter.Limiter, database *gorm.DB, protocol string) {
    limiter.SetBanChecker(func(ip, proto string) bool {
        var count int64
        database.Model(&models.Ban{}).
            Where("ip = ? AND (protocol = ? OR protocol = 'all') AND (expires_at IS NULL OR expires_at > ?)",
                ip, proto, time.Now()).
            Count(&count)
        return count > 0
    }, protocol)
}
```

The check is called on every `Accept(ip)` and `IsBanned(ip)` call.

## Admin API Endpoints

### List Bans

```
GET /api/v1/admin/bans?protocol=smtp&active=true&limit=50&offset=0
Authorization: Bearer <admin-jwt>
```

Query parameters:
- `protocol` - Filter by protocol (smtp, imap, pop3)
- `ip` - Filter by specific IP
- `active` - `true` (default) shows only non-expired bans, `false` shows all
- `limit`, `offset` - Pagination

### Create Ban

```
POST /api/v1/admin/bans
Authorization: Bearer <admin-jwt>
Content-Type: application/json

{
    "ip": "192.168.1.100",
    "reason": "Brute force attack on SMTP AUTH",
    "protocol": "smtp",
    "duration": "168h",
    "created_by": "admin@mail3.test"
}
```

- `protocol`: `smtp`, `imap`, `pop3`, or `all` (default: `all`)
- `duration`: Go duration string (e.g., `24h`, `168h`, `720h`). Omit for permanent ban.
- Upserts: if the IP is already banned, the existing ban is updated.

### Delete Ban by ID

```
DELETE /api/v1/admin/bans/{id}
Authorization: Bearer <admin-jwt>
```

### Unban by IP

```
DELETE /api/v1/admin/bans/ip/{ip}
Authorization: Bearer <admin-jwt>
```

## Production Deployment Recommendations

### 1. External fail2ban Integration

For production, pair the DB ban system with fail2ban watching gateway logs:

**`/etc/fail2ban/filter.d/restmail-smtp.conf`**:
```ini
[Definition]
failregex = ^.*"msg":"authentication failed".*"client_ip":"<HOST>".*$
ignoreregex =
```

**`/etc/fail2ban/jail.d/restmail.conf`**:
```ini
[restmail-smtp]
enabled  = true
filter   = restmail-smtp
logpath  = /var/log/restmail/smtp-gateway.log
maxretry = 5
findtime = 600
bantime  = 3600
action   = restmail-ban

[restmail-imap]
enabled  = true
filter   = restmail-imap
logpath  = /var/log/restmail/imap-gateway.log
maxretry = 10
findtime = 600
bantime  = 1800
action   = restmail-ban
```

**`/etc/fail2ban/action.d/restmail-ban.conf`**:
```ini
[Definition]
actionban = curl -s -X POST http://localhost:8080/api/v1/admin/bans \
    -H "Authorization: Bearer <ADMIN_TOKEN>" \
    -H "Content-Type: application/json" \
    -d '{"ip":"<ip>","reason":"fail2ban: <failures> failures in <findtime>s","protocol":"<name>","duration":"<bantime>s","created_by":"fail2ban"}'

actionunban = curl -s -X DELETE http://localhost:8080/api/v1/admin/bans/ip/<ip> \
    -H "Authorization: Bearer <ADMIN_TOKEN>"
```

### 2. Recommended Ban Durations

| Scenario                        | Duration | Protocol |
|---------------------------------|----------|----------|
| SMTP brute force (5 failures)   | 1h       | smtp     |
| SMTP brute force (repeat)       | 24h      | smtp     |
| IMAP brute force (10 failures)  | 30m      | imap     |
| POP3 brute force (10 failures)  | 30m      | pop3     |
| Known spam source               | 720h     | smtp     |
| Persistent abuser               | permanent| all      |

### 3. Monitoring

Check ban table growth and effectiveness:

```sql
-- Active bans by protocol
SELECT protocol, COUNT(*) FROM bans
WHERE expires_at IS NULL OR expires_at > NOW()
GROUP BY protocol;

-- Bans created in the last 24h
SELECT * FROM bans WHERE created_at > NOW() - INTERVAL '24 hours'
ORDER BY created_at DESC;

-- Expired bans (can be cleaned up)
SELECT COUNT(*) FROM bans WHERE expires_at < NOW();
```

### 4. Cleanup

Periodically remove expired bans to keep the table small:

```sql
DELETE FROM bans WHERE expires_at IS NOT NULL AND expires_at < NOW();
```

Or via the API, list with `active=false` and delete expired entries.

### 5. Allowlisting

To ensure certain IPs are never banned (monitoring systems, internal relays), do NOT add them to the ban table. Instead, configure gateway connection limits to exempt trusted CIDRs at the network/firewall level.

## Docker Compose Integration

When running in Docker, gateway containers need access to the PostgreSQL database for persistent bans. Ensure the containers share the same Docker network and that `DB_HOST` points to the database container:

```yaml
smtp-gateway:
  environment:
    - DB_HOST=postgres
    - DB_PORT=5432
    - DB_NAME=restmail
    - DB_USER=restmail
    - DB_PASS=restmail
```

Gateway logs are written to stdout in JSON format. Mount or forward these logs to a location where fail2ban can read them, or use a log aggregator that can trigger the ban API.
