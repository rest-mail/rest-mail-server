package db

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Connect opens a GORM connection to PostgreSQL using the provided config.
func Connect(cfg *config.Config) (*gorm.DB, error) {
	logLevel := logger.Warn
	if cfg.Environment == "development" {
		logLevel = logger.Info
	}

	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logLevel),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.DBConnMaxLifetime)

	slog.Info("connected to database",
		"host", cfg.DBHost,
		"port", cfg.DBPort,
		"database", cfg.DBName,
	)

	return db, nil
}

// AutoMigrate runs GORM's auto-migration for all models. It is ADDITIVE and
// idempotent: it adds tables/columns/indexes, safe to re-run on every
// startup. A server boot never issues destructive DML (issue #196).
func AutoMigrate(db *gorm.DB) error {
	slog.Info("running database auto-migration")

	err := db.AutoMigrate(
		&models.Domain{},
		&models.Mailbox{},
		&models.Alias{},
		&models.Message{},
		&models.QuotaUsage{},
		&models.Certificate{},
		&models.OutboundQueue{},
		// Addresses a domain no longer sends to after a hard bounce (#141).
		&models.Suppression{},
		// Pipeline models
		&models.Pipeline{},
		&models.CustomFilter{},
		// Per-message observability trace: the composite (outcome, created_at)
		// index is created via its GORM tags, alongside standalone indexes for
		// correlation and pruning.
		&models.MessageTrace{},
		// Time-bucketed aggregate rollups (PR4): the rollup worker snapshots the
		// always-on pipeline counters into these long-lived rows. Separate from
		// message_traces (which are sampled + pruned) so aggregate history is
		// never affected by trace sampling or pruning.
		&models.PipelineRollup{},
		// Downsampling additive block (multi-resolution rollups): the coarse
		// (default daily) aggregate table the rollup worker condenses aged-out
		// fine rollups into. Separate table so the two resolutions never collide
		// on pipeline_rollups' (metric_name, labels, bucket_start) unique index.
		&models.PipelineRollupCoarse{},
		&models.Contact{},
		&models.DomainSenderRule{},
		&models.GreylistEntry{},
		&models.Quarantine{},
		&models.VacationResponse{},
		&models.Attachment{},
		&models.SieveScript{},
		&models.VacationConfig{},
		// RESTMAIL protocol upgrade cache
		&models.RESTMAILCapability{},
		// Admin features
		&models.Ban{},
		// MTA-STS (RFC 8461) policies
		&models.MTASTSPolicy{},
		// TLS-RPT (RFC 8460) reports
		&models.TLSReport{},
		// Calendar event lifecycle tracking
		&models.CalendarEventVersion{},
		// Roles, their capabilities, and the grants that give them to accounts
		// at a scope (#345).
		&models.Role{},
		&models.Capability{},
		&models.RoleCapability{},
		&models.Grant{},
		&models.GrantActivation{},
		// Refresh-token rotation/revocation ledger (OSI-10): one row per issued
		// refresh token, keyed by jti, tracking active/rotated/revoked state.
		&models.RefreshToken{},
		// Single-use account-reset tokens (internal/bootstrap).
		&models.OneTimeToken{},
		// Public owner recovery keys, independent of the deployment platform.
		&models.OwnerKey{},
		// Credentials: everything an account signs in with (passwords, TOTP
		// secrets encrypted at rest, recovery codes, app passwords), 0..n per
		// account, kept after revocation for the audit trail.
		&models.Credential{},
		// Phones registered for push (#363), each by the session that
		// registered it, gone when it ends.
		&models.PushDevice{},
		// DMARC aggregate (rua) reporting
		&models.DMARCAggregateRecord{},
	)
	if err != nil {
		return fmt.Errorf("auto-migration failed: %w", err)
	}

	// The audit log, and what makes it append-only. Unlike the indexes below, a
	// failure here stops the server: the table must never exist unguarded.
	if err := MigrateAuditLog(db); err != nil {
		return err
	}

	// Create composite unique index for mailboxes (domain_id, local_part)
	if !db.Migrator().HasIndex(&models.Mailbox{}, "idx_mailboxes_domain_localpart") {
		err = db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_mailboxes_domain_localpart ON mailboxes(domain_id, local_part)").Error
		if err != nil {
			slog.Warn("failed to create composite index", "error", err)
		}
	}

	// Create composite unique index for aliases (source, destination)
	if !db.Migrator().HasIndex(&models.Alias{}, "idx_aliases_source_dest") {
		err = db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_aliases_source_dest ON aliases(source_address, destination_address)").Error
		if err != nil {
			slog.Warn("failed to create composite index", "error", err)
		}
	}

	// Create full-text search index for messages
	err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_search ON messages USING gin(to_tsvector('english', coalesce(subject, '') || ' ' || coalesce(body_text, '')))`).Error
	if err != nil {
		slog.Warn("failed to create full-text search index", "error", err)
	}

	// Create partial index for unread messages
	err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_mailbox_unread ON messages(mailbox_id, folder) WHERE is_read = false`).Error
	if err != nil {
		slog.Warn("failed to create unread index", "error", err)
	}

	// Pipeline indexes
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_mailbox_email ON contacts(mailbox_id, email)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_contacts_trust_level ON contacts(mailbox_id, trust_level)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_domain_sender_rules_unique ON domain_sender_rules(domain_id, pattern, list_type)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_domain_sender_rules_lookup ON domain_sender_rules(domain_id, list_type, pattern)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_greylist_triple ON greylist_entries(sender, recipient, source_ip)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_quarantine_mailbox ON quarantine(mailbox_id, released, received_at DESC)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_quarantine_expiry ON quarantine(expires_at) WHERE released = false`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_attachments_checksum ON attachments(checksum)`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_vacation_responses_unique ON vacation_responses(mailbox_id, sender)`)

	slog.Info("database migration completed")
	return nil
}

// auditLogAppendOnlySQL makes audit_log append-only: a row trigger refuses
// UPDATE and DELETE, a statement trigger refuses TRUNCATE, and both raise the
// same error. CREATE OR REPLACE (Postgres 14 and later) keeps it idempotent.
var auditLogAppendOnlySQL = []string{
	`CREATE OR REPLACE FUNCTION audit_log_append_only() RETURNS trigger
	LANGUAGE plpgsql AS $$
	BEGIN
		RAISE EXCEPTION 'audit_log is append-only';
	END
	$$`,
	`CREATE OR REPLACE TRIGGER audit_log_no_update_or_delete
	BEFORE UPDATE OR DELETE ON audit_log
	FOR EACH ROW EXECUTE FUNCTION audit_log_append_only()`,
	`CREATE OR REPLACE TRIGGER audit_log_no_truncate
	BEFORE TRUNCATE ON audit_log
	FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only()`,
}

// auditLogMigrateLockKey serialises MigrateAuditLog across replicas starting
// at once, which would otherwise race replacing the same function and
// triggers: "auditmig".
const auditLogMigrateLockKey int64 = 0x61756469746d6967

// MigrateAuditLog creates the audit_log table (models.AuditEntry) and installs
// the triggers that make it append-only, in one transaction. Idempotent;
// AutoMigrate runs it on every start.
func MigrateAuditLog(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", auditLogMigrateLockKey).Error; err != nil {
			return fmt.Errorf("audit log migration failed: %w", err)
		}
		if err := tx.AutoMigrate(&models.AuditEntry{}); err != nil {
			return fmt.Errorf("audit log migration failed: %w", err)
		}
		for _, stmt := range auditLogAppendOnlySQL {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("audit log append-only guard failed: %w", err)
			}
		}
		return nil
	})
}

// WaitForDB retries connecting to the database until it succeeds or the timeout is reached.
func WaitForDB(cfg *config.Config, timeout time.Duration) (*gorm.DB, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		db, err := Connect(cfg)
		if err == nil {
			return db, nil
		}
		lastErr = err
		slog.Info("waiting for database...", "error", err)
		time.Sleep(2 * time.Second)
	}

	return nil, fmt.Errorf("database not ready after %v: %w", timeout, lastErr)
}
