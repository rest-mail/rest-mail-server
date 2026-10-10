package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/instance"
	"github.com/restmail/restmail/internal/seed"
	"gorm.io/gorm"
)

// defaultQuotaBytes is the per-domain default quota (1 GiB) applied to
// additional served-domain rows, matching the seed fixture's primary domain.
const defaultQuotaBytes = 1073741824

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(logHandler))

	domain := os.Getenv("SEED_DOMAIN")
	if domain == "" {
		domain = "example.test"
	}
	slog.Info("seeding database with test data", "domain", domain)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	database, err := db.WaitForDB(cfg, 30*time.Second)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	// Seeding is additive-only (allowDestructive=false); it never issues
	// destructive DML (issue #196).
	if err := db.AutoMigrate(database, false); err != nil {
		slog.Error("failed to migrate", "error", err)
		os.Exit(1)
	}

	if err := seedFixture(database, domain); err != nil {
		slog.Error("seeding failed", "error", err)
		os.Exit(1)
	}

	// Additional served domains (manifest `domains:` list, rendered to
	// RESTMAIL_SEED_SERVED_DOMAINS and passed here as SEED_SERVED_DOMAINS). Each
	// gets a DB domain row with its declared server_type; mailboxes/aliases for
	// these stay DB-driven (admin API). Empty/unset → nothing extra is seeded,
	// so single-domain instances are unchanged.
	if err := seedServedDomains(database, os.Getenv("SEED_SERVED_DOMAINS")); err != nil {
		slog.Error("served-domain seeding failed", "error", err)
		os.Exit(1)
	}

	if err := seedRBAC(database); err != nil {
		slog.Error("RBAC seeding failed", "error", err)
		os.Exit(1)
	}

	slog.Info("seeding completed successfully")
}

func seedFixture(database *gorm.DB, domain string) error {
	defaultPassword, err := auth.HashPassword("password123")
	if err != nil {
		return err
	}
	fx := seed.BuildFixture(domain, defaultPassword)

	// Domain (mail1/mail2 are seeded via SQL init scripts in their own databases)
	result := database.Where("name = ?", fx.Domain.Name).FirstOrCreate(&fx.Domain)
	if result.Error != nil {
		return result.Error
	}
	slog.Info("domain", "name", fx.Domain.Name, "id", fx.Domain.ID, "created", result.RowsAffected > 0)

	// Mailboxes
	for i := range fx.Mailboxes {
		fx.Mailboxes[i].DomainID = fx.Domain.ID
		result := database.Where("address = ?", fx.Mailboxes[i].Address).FirstOrCreate(&fx.Mailboxes[i])
		if result.Error != nil {
			return result.Error
		}
		slog.Info("mailbox", "address", fx.Mailboxes[i].Address, "id", fx.Mailboxes[i].ID, "created", result.RowsAffected > 0)
		database.Where("mailbox_id = ?", fx.Mailboxes[i].ID).FirstOrCreate(&models.QuotaUsage{MailboxID: fx.Mailboxes[i].ID})
	}

	// Aliases
	for i := range fx.Aliases {
		fx.Aliases[i].DomainID = fx.Domain.ID
		result := database.Where("source_address = ? AND destination_address = ?", fx.Aliases[i].SourceAddress, fx.Aliases[i].DestinationAddress).FirstOrCreate(&fx.Aliases[i])
		if result.Error != nil {
			return result.Error
		}
		slog.Info("alias", "source", fx.Aliases[i].SourceAddress, "dest", fx.Aliases[i].DestinationAddress, "created", result.RowsAffected > 0)
	}

	// Webmail accounts
	for _, addr := range fx.WebmailAddresses {
		var mailbox models.Mailbox
		database.Where("address = ?", addr).First(&mailbox)

		var account models.WebmailAccount
		result := database.Where("primary_mailbox_id = ?", mailbox.ID).FirstOrCreate(&account, models.WebmailAccount{PrimaryMailboxID: mailbox.ID})
		if result.Error != nil {
			return result.Error
		}
		slog.Info("webmail_account", "address", addr, "id", account.ID, "created", result.RowsAffected > 0)
	}

	return nil
}

// seedServedDomains creates a DB domain row for each ADDITIONAL served domain
// declared in the manifest `domains:` list (decoded from SEED_SERVED_DOMAINS).
// FirstOrCreate on the name makes it idempotent. Only the domain row (with its
// server_type) is created — mailboxes/aliases for additional domains remain
// DB-driven via the admin API, matching how the roadmap treats domain data.
func seedServedDomains(database *gorm.DB, spec string) error {
	entries, err := instance.ParseSeedServedDomains(spec)
	if err != nil {
		return err
	}
	for _, e := range entries {
		domain := models.Domain{Name: e.Name, ServerType: e.ServerType, Active: true, DefaultQuotaBytes: defaultQuotaBytes}
		result := database.Where("name = ?", e.Name).FirstOrCreate(&domain)
		if result.Error != nil {
			return result.Error
		}
		slog.Info("served domain", "name", e.Name, "server_type", e.ServerType, "id", domain.ID, "created", result.RowsAffected > 0)
	}
	return nil
}

func seedRBAC(database *gorm.DB) error {
	if err := bootstrap.EnsureRBAC(database); err != nil {
		return err
	}

	var superadminRole models.Role
	if err := database.Where("name = ?", "superadmin").First(&superadminRole).Error; err != nil {
		return err
	}

	// Create initial admin user (username: admin, password: admin123!@)
	adminPassword, err := auth.HashPassword("admin123!@")
	if err != nil {
		return err
	}

	// The attrs MUST go through Attrs(), not as FirstOrCreate conditions: GORM
	// merges a conditions-struct into the lookup query, and PasswordHash is a
	// freshly salted bcrypt hash on every run — the SELECT never matches the
	// stored row, so re-seeding an existing database tried to INSERT a second
	// "admin" and aborted the whole up chain on the unique username index.
	var adminUser models.AdminUser
	result := database.Where("username = ?", "admin").
		Attrs(models.AdminUser{
			Username:               "admin",
			Email:                  "admin@localhost",
			PasswordHash:           adminPassword,
			PasswordChangeRequired: true,
			Active:                 true,
		}).
		FirstOrCreate(&adminUser)
	if result.Error != nil {
		return result.Error
	}
	slog.Info("admin user", "username", "admin", "created", result.RowsAffected > 0)

	// Assign superadmin role to admin user
	var ur models.UserRole
	result = database.Where("user_id = ? AND role_id = ?", adminUser.ID, superadminRole.ID).
		FirstOrCreate(&ur, models.UserRole{
			UserID: adminUser.ID,
			RoleID: superadminRole.ID,
		})
	if result.Error != nil {
		return result.Error
	}
	slog.Info("user role assigned", "user", "admin", "role", "superadmin", "created", result.RowsAffected > 0)

	return nil
}
