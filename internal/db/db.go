package db

import (
	"fmt"
	"log/slog"
	"os"
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

// backfillRawSizeSQL populates messages.raw_size for pre-existing rows:
// the exact octet count of the stored raw message when one exists
// (octet_length counts bytes, matching len(raw) at write time), else the
// legacy size_bytes value so gateways report an unchanged size for
// messages that never had a stored raw. Guarded on raw_size = 0 so the
// statement is idempotent across restarts and never overwrites a value a
// writer recorded.
const backfillRawSizeSQL = `UPDATE messages SET raw_size = CASE
	WHEN raw_message IS NOT NULL AND raw_message <> '' THEN octet_length(raw_message)
	ELSE size_bytes
END
WHERE raw_size = 0 AND (COALESCE(raw_message, '') <> '' OR size_bytes <> 0)`

// dedupeLinkedAccountsSQL collapses pre-existing duplicate linked_accounts rows,
// keeping the earliest (lowest id) link per mailbox, so the standalone unique
// index on mailbox_id (OSI-21) can be created on a database that predates the
// constraint. It DELETEs rows, so it is DESTRUCTIVE and one-time: it runs only on
// the explicit opt-in migrate path, never on a server boot (issue #196).
const dedupeLinkedAccountsSQL = `DELETE FROM linked_accounts a USING linked_accounts b WHERE a.mailbox_id = b.mailbox_id AND a.id > b.id`

// countLinkedAccountDuplicatesSQL counts the rows a dedupe would remove: any link
// for a mailbox that already has an earlier (lower-id) link. Used on the additive
// boot path to detect — WITHOUT deleting anything — whether a legacy database
// still holds duplicates that would block creating the unique index.
const countLinkedAccountDuplicatesSQL = `SELECT count(*) FROM linked_accounts a WHERE EXISTS (SELECT 1 FROM linked_accounts b WHERE b.mailbox_id = a.mailbox_id AND b.id < a.id)`

// pendingDestructiveMigrations returns the destructive, one-time DML a migration
// run would issue for the given opt-in. On the additive/boot path (allowDestructive
// == false) it is ALWAYS empty: booting the API, seeding, or any default run never
// issues destructive DML, so a rolling deploy can never have whichever code
// version boots first silently rewrite data (issue #196). The destructive steps
// run only when an operator explicitly opts in via the migrate tool.
func pendingDestructiveMigrations(allowDestructive bool) []string {
	if !allowDestructive {
		return nil
	}
	return []string{dedupeLinkedAccountsSQL}
}

// migrateLinkedAccountsDedupe applies (or, on the boot path, guards against) the
// one-time destructive collapse of duplicate linked_accounts rows. When
// allowDestructive is true it runs the dedupe; when false (every server boot) it
// deletes nothing and, if a legacy database still holds duplicates that would make
// the unique-index creation fail opaquely, it refuses with an actionable error so
// the operator runs the migrate tool rather than the API silently rewriting data.
func migrateLinkedAccountsDedupe(db *gorm.DB, allowDestructive bool) error {
	// Fresh database: the table does not exist yet, so there is nothing to collapse
	// and nothing to guard — a no-op for the common install case.
	if !db.Migrator().HasTable(&models.LinkedAccount{}) {
		return nil
	}

	for _, stmt := range pendingDestructiveMigrations(allowDestructive) {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("destructive migration failed: %w", err)
		}
	}
	if allowDestructive {
		return nil
	}

	// Additive/boot path: never delete. If duplicates remain, the unique index
	// below would fail with an opaque error — surface an actionable one and refuse.
	var dupes int64
	if err := db.Raw(countLinkedAccountDuplicatesSQL).Scan(&dupes).Error; err != nil {
		// Could not even introspect (e.g. an unrelated quirk on a very old schema):
		// do not block boot on the check itself — proceed additive-only.
		slog.Warn("could not check linked_accounts for duplicates; proceeding additive-only", "error", err)
		return nil
	}
	if dupes > 0 {
		return fmt.Errorf("linked_accounts holds %d duplicate row(s) that block the unique index; a server boot never modifies data — run the migrate tool with DB_ALLOW_DESTRUCTIVE_MIGRATIONS=true to collapse duplicates", dupes)
	}
	return nil
}

// AutoMigrate runs GORM's auto-migration for all models. It is ADDITIVE and
// idempotent by default: it adds tables/columns/indexes and runs guarded,
// WHERE-0 backfills — all safe to re-run on every startup. Destructive one-time
// upgrades (currently the linked_accounts dedupe) run ONLY when allowDestructive
// is true, i.e. from the dedicated migrate tool with an explicit opt-in, never
// from a server boot (issue #196).
func AutoMigrate(db *gorm.DB, allowDestructive bool) error {
	slog.Info("running database auto-migration", "allow_destructive", allowDestructive)

	// OSI-21: linked_accounts carries a standalone unique index on mailbox_id (a
	// mailbox may be linked to at most one webmail account). On a database that
	// predates the constraint, duplicate links must be collapsed first or the
	// unique-index creation below would fail — but that collapse is destructive, so
	// it is gated behind the explicit opt-in (see migrateLinkedAccountsDedupe).
	if err := migrateLinkedAccountsDedupe(db, allowDestructive); err != nil {
		return err
	}

	err := db.AutoMigrate(
		&models.Domain{},
		&models.Mailbox{},
		&models.Alias{},
		&models.WebmailAccount{},
		&models.LinkedAccount{},
		&models.Message{},
		&models.QuotaUsage{},
		&models.Certificate{},
		&models.DKIMKey{},
		&models.OutboundQueue{},
		// Pipeline models
		&models.Pipeline{},
		&models.CustomFilter{},
		&models.PipelineLog{},
		// Per-message observability trace (PR3): supersedes PipelineLog as the
		// write target; the composite (outcome, created_at) index is created via
		// its GORM tags above, alongside standalone indexes for correlation and
		// PR4 pruning.
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
		&models.ActivityLog{},
		// MTA-STS (RFC 8461) policies
		&models.MTASTSPolicy{},
		// TLS-RPT (RFC 8460) reports
		&models.TLSReport{},
		// Calendar event lifecycle tracking
		&models.CalendarEventVersion{},
		// RBAC models
		&models.AdminUser{},
		&models.Role{},
		&models.Capability{},
		&models.UserRole{},
		&models.RoleCapability{},
		// Refresh-token rotation/revocation ledger (OSI-10): one row per issued
		// refresh token, keyed by jti, tracking active/rotated/revoked state.
		&models.RefreshToken{},
		// Optional TOTP two-factor auth (OSI-19): one enrollment per account
		// (secret encrypted at rest) plus its hashed one-time recovery codes.
		&models.TwoFactor{},
		&models.TwoFactorRecoveryCode{},
		// DMARC aggregate (rua) reporting
		&models.DMARCAggregateRecord{},
	)
	if err != nil {
		return fmt.Errorf("auto-migration failed: %w", err)
	}

	// Backfill messages.raw_size for rows created before the column existed.
	// octet_length (not length) matches Go's len() over the stored text: it
	// counts bytes in the database encoding, which is exactly what the writer
	// would have recorded. Rows without a stored raw fall back to size_bytes so
	// protocol gateways keep reporting the same size they always did for them.
	// The raw_size = 0 guard makes the backfill idempotent and cheap on
	// subsequent startups.
	if err := db.Exec(backfillRawSizeSQL).Error; err != nil {
		slog.Warn("failed to backfill messages.raw_size", "error", err)
	}

	// messages.received_tls / messages.tls_version (inbound transport-security
	// monitoring) are added by AutoMigrate above and deliberately NOT backfilled:
	// the true transport security of a message that arrived before the column
	// existed is genuinely unknown, and received_tls is nullable so NULL encodes
	// exactly that. Defaulting old rows to false would misreport them as
	// plaintext inbound-MX arrivals and skew the dashboard aggregate, so they are
	// left NULL ("not applicable / unknown") — the honest, non-breaking default.

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

	// ── OSI-8: encrypt DKIM private keys at rest ──────────────────────────────
	// Any domains.dkim_private_key stored as legacy plaintext PEM is encrypted in
	// place with the MASTER_KEY-derived key and tagged with the dkim:v1: version
	// prefix, so the signer can tell an encrypted key (which MUST decrypt) apart
	// from a plaintext one and fail closed on an undecryptable key. Idempotent:
	// already-versioned rows are skipped and legacy bare-base64 ciphertext is only
	// normalized (prefixed, never re-encrypted), so a re-run never double-encrypts.
	// A no-op when MASTER_KEY is unset (encryption disabled) or the table is absent.
	// The key is read from the same MASTER_KEY env var config.Load uses, so every
	// entrypoint that calls AutoMigrate (api, migrate, seed) runs this transparently.
	if masterKey := os.Getenv("MASTER_KEY"); masterKey != "" && db.Migrator().HasTable(&models.Domain{}) {
		if n, err := encryptPlaintextDKIMKeys(db, masterKey); err != nil {
			slog.Warn("failed to encrypt DKIM keys at rest", "error", err)
		} else if n > 0 {
			slog.Info("encrypted plaintext DKIM keys at rest", "count", n)
		}
	}

	slog.Info("database migration completed")
	return nil
}

// encryptPlaintextDKIMKeys migrates any plaintext domains.dkim_private_key rows
// to the encrypted-at-rest form (OSI-8), returning how many rows were changed.
// Per-value plaintext/ciphertext detection and idempotency live in
// models.MigrateDKIMKeyAtRest; a row whose value cannot be interpreted under the
// current MASTER_KEY is logged and left untouched (fail-closed happens at sign
// time), so a single bad row never aborts startup migration.
func encryptPlaintextDKIMKeys(db *gorm.DB, masterKey string) (int, error) {
	var domains []models.Domain
	if err := db.Where("dkim_private_key <> ''").Find(&domains).Error; err != nil {
		return 0, err
	}
	migrated := 0
	for i := range domains {
		d := domains[i]
		upgraded, changed, err := models.MigrateDKIMKeyAtRest(d.DKIMPrivateKey, masterKey)
		if err != nil {
			slog.Warn("DKIM key at-rest migration skipped a row", "domain", d.Name, "error", err)
			continue
		}
		if !changed {
			continue
		}
		if err := db.Model(&models.Domain{}).Where("id = ?", d.ID).
			Update("dkim_private_key", upgraded).Error; err != nil {
			return migrated, err
		}
		migrated++
	}
	return migrated, nil
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
