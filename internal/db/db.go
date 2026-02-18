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

// AutoMigrate runs GORM's auto-migration for all models.
func AutoMigrate(db *gorm.DB) error {
	slog.Info("running database auto-migration")

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
	)
	if err != nil {
		return fmt.Errorf("auto-migration failed: %w", err)
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
