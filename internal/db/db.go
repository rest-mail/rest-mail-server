package db

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
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

// autoMigrateLockKey serialises AutoMigrate across processes sharing a
// database: replicas starting at once, or test packages run in parallel,
// would otherwise race creating the same tables ("relation already exists"):
// "automigr".
const autoMigrateLockKey int64 = 0x6175746f6d696772

// AutoMigrate runs GORM's auto-migration for all models. It is ADDITIVE and
// idempotent: it adds tables/columns/indexes, safe to re-run on every
// startup. A server boot never issues destructive DML (issue #196).
//
// It holds a session advisory lock on one pinned connection rather than
// running in a transaction: the best-effort index statements below may fail,
// and a failure would abort a transaction.
func AutoMigrate(db *gorm.DB) error {
	return db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SELECT pg_advisory_lock(?)", autoMigrateLockKey).Error; err != nil {
			return fmt.Errorf("auto-migration lock failed: %w", err)
		}
		defer conn.Exec("SELECT pg_advisory_unlock(?)", autoMigrateLockKey)
		return autoMigrate(conn)
	})
}

func autoMigrate(db *gorm.DB) error {
	slog.Info("running database auto-migration")

	err := db.AutoMigrate(
		&models.Domain{},
		&models.DKIMRotationKey{},
		&models.Mailbox{},
		&models.Alias{},
		&models.Message{},
		// Mail the user asked the server to send later; replayed through the
		// ordinary send path when due.
		&models.ScheduledSend{},
		&models.QuotaUsage{},
		&models.Certificate{},
		// The API's ACME account key per CA directory, reused by renewals.
		&models.ACMEAccount{},
		&models.OutboundQueue{},
		// Addresses a domain no longer sends to after a hard bounce (#141).
		&models.Suppression{},
		&models.DNSBLListingEvent{},
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
		// Outbound transport per sending domain: direct MX or an SMTP relay (#143)
		&models.DomainRelay{},
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
		// The outbound SMTP sessions REST Mail reports on in TLS-RPT (#726)
		&models.TLSRPTSession{},
	)
	if err != nil {
		return fmt.Errorf("auto-migration failed: %w", err)
	}
	// Connect disables implicit foreign-key migration. Rotation keys must not
	// outlive their domain, so create this additive constraint explicitly.
	if !db.Migrator().HasConstraint(&models.DKIMRotationKey{}, "Domain") {
		if err := db.Migrator().CreateConstraint(&models.DKIMRotationKey{}, "Domain"); err != nil {
			return fmt.Errorf("DKIM rotation domain constraint: %w", err)
		}
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
	// One row per mailbox, sender and handle: a Sieve vacation action is tracked apart from
	// the settings' reply (handle '') and from another action (#748). The row's time is the
	// latest reply, which the interval runs from.
	db.Exec(`DROP INDEX IF EXISTS idx_vacation_responses_unique`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_vacation_responses_handle ON vacation_responses(mailbox_id, sender, handle)`)

	// Messages stored before downgraded_size existed get it measured once.
	if err := BackfillDowngradedSize(db); err != nil {
		slog.Warn("failed to backfill downgraded message sizes", "error", err)
	}

	slog.Info("database migration completed")
	return nil
}

// downgradedSizeBatch is how many messages BackfillDowngradedSize reads at a time.
const downgradedSizeBatch = 200

// BackfillDowngradedSize measures downgraded_size for messages stored with a
// raw source before the column existed. A message whose headers are ASCII gets
// its raw_size. Until a row is measured the gateways report raw_size, as before,
// so a backfill that stops part way leaves nothing worse than it was. Rows are
// read in id order a batch at a time; a measured row is never read again.
func BackfillDowngradedSize(db *gorm.DB) error {
	var lastID uint
	measured := 0
	for {
		var rows []models.Message
		err := db.Select("id", "raw_size", "raw_message", "raw_octets").
			Where("id > ? AND raw_size > 0 AND downgraded_size = 0", lastID).
			Order("id").Limit(downgradedSizeBatch).Find(&rows).Error
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			size := rmail.DowngradedSize(string(row.Source()))
			if err := db.Model(&models.Message{}).Where("id = ?", row.ID).
				UpdateColumn("downgraded_size", size).Error; err != nil {
				return err
			}
			lastID = row.ID
			measured++
		}
	}
	if measured > 0 {
		slog.Info("backfilled downgraded message sizes", "messages", measured)
	}
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
