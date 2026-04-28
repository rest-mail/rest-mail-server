# rest-mail — Backup & Restore Plan

_This is a separate plan for implementing backup and restore functionality. It is not part of the initial release but is documented here so we can come back to it._

## Overview

rest-mail stores data in three places. A complete backup must capture all three:

| Data Store | What It Contains | Volume |
|-----------|------------------|--------|
| **PostgreSQL** | Accounts, domains, messages (metadata + body), pipeline config, contacts, quarantine, queue, certs, DKIM keys | Primary — most data lives here |
| **Attachment storage** | Extracted attachments referenced by storage refs in the database | Can be large (depends on usage) |
| **Docker volumes** | Maildir (mail1/mail2 only), generated TLS certs, Let's Encrypt state | Small footprint |

## Backup Strategy

### 1. PostgreSQL Backup

#### Option A: pg_dump (Simple, Single-Instance)

Best for small-to-medium installations. Run `pg_dump` on a schedule inside the Postgres container:

```yaml
# docker-compose addition
services:
  backup:
    image: postgres:16
    volumes:
      - backup-data:/backups
    entrypoint: /bin/sh
    command: >
      -c "while true; do
        PGPASSWORD=$$DB_PASS pg_dump -h postgres -U restmail -Fc restmail > /backups/restmail-$$(date +%Y%m%d-%H%M%S).dump;
        find /backups -name '*.dump' -mtime +7 -delete;
        sleep 86400;
      done"
    depends_on:
      postgres:
        condition: service_healthy
```

- **Format:** Custom (`-Fc`) — compressed, supports selective restore
- **Frequency:** Daily (configurable)
- **Retention:** 7 days of daily backups (configurable)
- **Restore:** `pg_restore -h postgres -U restmail -d restmail restmail-20260217.dump`

#### Option B: WAL Archiving + Point-in-Time Recovery (Production)

For production with minimal data loss tolerance:

```
PostgreSQL ──► WAL segments ──► S3 / GCS / local storage
                                      │
                                      ▼
                              Restore to any point in time
```

Tools: **pgBackRest** or **WAL-G** (both support S3, GCS, Azure Blob, local).

- **RPO (Recovery Point Objective):** Near-zero (continuous WAL streaming)
- **RTO (Recovery Time Objective):** Minutes (restore base backup + replay WAL)
- **Base backups:** Weekly full backup
- **WAL archiving:** Continuous (every 16MB WAL segment, or every 60 seconds via `archive_timeout`)

```ini
# postgresql.conf additions for WAL archiving
wal_level = replica
archive_mode = on
archive_command = 'pgbackrest --stanza=restmail archive-push %p'
archive_timeout = 60
```

### 2. Attachment Storage Backup

Attachments are stored on a filesystem (Docker volume or S3-compatible storage in production).

#### Filesystem (Docker volume)

```bash
# Backup: tar the attachment volume
docker run --rm -v restmail_attachments:/data -v backup-data:/backups \
  alpine tar czf /backups/attachments-$(date +%Y%m%d).tar.gz -C /data .

# Restore:
docker run --rm -v restmail_attachments:/data -v backup-data:/backups \
  alpine tar xzf /backups/attachments-20260217.tar.gz -C /data
```

#### S3-Compatible Storage (Production)

If attachments are stored in S3 (or MinIO), enable **S3 versioning** and **lifecycle rules** for retention. No separate backup needed — S3 versioning is the backup.

### 3. Configuration Backup

Configuration is code (docker-compose.yml, Taskfile.yml, .env, etc.) and lives in Git. The only runtime state not in Git is:

| Item | Backup Method |
|------|---------------|
| `.env` (secrets) | Backed up to secrets manager (Vault, AWS Secrets Manager) |
| `MASTER_KEY` | Must be stored separately and securely — losing this means losing access to encrypted private keys |
| Let's Encrypt state | Included in Docker volume backup |
| TLS certs (dev CA) | Regenerable from scratch (`task certs:generate`) |

### 4. Backup Verification

Backups are worthless if they can't be restored. Automated verification:

```yaml
# Weekly: restore backup to a temporary environment and run health checks
verify-backup:
  schedule: "0 4 * * 0"  # Sunday 4 AM
  steps:
    - Create temporary Postgres container
    - Restore latest pg_dump
    - Run schema validation (all tables exist, row counts > 0)
    - Restore attachment volume
    - Verify attachment references resolve (sample 100 random refs)
    - Tear down temporary environment
    - Report result to monitoring (Prometheus / Grafana alert)
```

## Restore Procedures

### Full Disaster Recovery

Complete restore from scratch:

```
1. Deploy fresh docker-compose stack
2. Restore PostgreSQL:        pg_restore -h postgres -U restmail -d restmail latest.dump
3. Restore attachments:       tar xzf attachments-latest.tar.gz into volume
4. Set MASTER_KEY env var:    (from secrets manager)
5. Run migrations:            task db:migrate  (apply any schema changes since backup)
6. Verify:                    task test:e2e    (run smoke tests)
```

### Single-Table Recovery

Restore a specific table (e.g. accidentally deleted messages):

```bash
# Extract just the messages table from a custom-format dump
pg_restore -h postgres -U restmail -d restmail -t messages --data-only latest.dump
```

### Point-in-Time Recovery (WAL-based)

Restore to a specific timestamp (e.g. "right before the accidental DELETE"):

```bash
pgbackrest --stanza=restmail --type=time \
  --target="2026-02-17 14:30:00+00" restore
```

## Backup Schedule Summary

| Component | Method | Frequency | Retention | RPO |
|-----------|--------|-----------|-----------|-----|
| PostgreSQL (simple) | pg_dump | Daily | 7 days | 24 hours |
| PostgreSQL (production) | WAL archiving | Continuous | 30 days + weekly base | ~1 minute |
| Attachments (volume) | tar snapshot | Daily | 7 days | 24 hours |
| Attachments (S3) | S3 versioning | Continuous | 30 days | Near-zero |
| Configuration | Git | Every commit | Forever | N/A (code) |
| Secrets | Secrets manager | On change | Versioned | N/A |
| Backup verification | Restore + validate | Weekly | N/A | N/A |

## Monitoring & Alerting

| Alert | Condition | Severity |
|-------|-----------|----------|
| Backup missed | No new pg_dump in 25 hours | Critical |
| WAL archiving lag | WAL segments not archived for > 5 minutes | Warning |
| Backup verification failed | Weekly restore test failed | Critical |
| Backup storage full | < 20% free space on backup volume | Warning |
| MASTER_KEY not set | API starts without MASTER_KEY | Critical |

## TODO

- [ ] Implement pg_dump backup container in docker-compose.yml
- [ ] Create `task backup:db` and `task backup:attachments` commands
- [ ] Create `task restore:db` and `task restore:attachments` commands
- [ ] Implement backup verification script (restore to temp env, validate)
- [ ] Document WAL archiving setup with pgBackRest for production
- [ ] Add backup monitoring alerts to Grafana dashboard
- [ ] Document MASTER_KEY backup procedure (critical — losing this is catastrophic)
- [ ] Add backup schedule to CI/CD (scheduled GitHub Action or cron on server)

## Open Questions

- What backup storage target? Local volume, S3, GCS, Azure Blob?
- What RPO is acceptable? (determines pg_dump vs WAL archiving)
- Should backup encryption be implemented? (encrypt backups at rest with a separate key)
- Should we support cross-region backup replication?
