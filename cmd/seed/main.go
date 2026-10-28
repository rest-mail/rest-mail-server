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
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
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

	// Seeding is additive-only; it never issues destructive DML (issue #196).
	if err := db.AutoMigrate(database); err != nil {
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

	if err := seedRBAC(database, domain); err != nil {
		slog.Error("RBAC seeding failed", "error", err)
		os.Exit(1)
	}

	slog.Info("seeding completed successfully")
}

func seedFixture(database *gorm.DB, domain string) error {
	if err := bootstrap.EnsureRBAC(database); err != nil {
		return err
	}
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

	// Accounts, each with the fixture password as its password credential.
	creds := repositories.NewCredentialRepository(database)
	for i := range fx.Mailboxes {
		fx.Mailboxes[i].DomainID = fx.Domain.ID
		result := database.Where("address = ?", fx.Mailboxes[i].Address).FirstOrCreate(&fx.Mailboxes[i])
		if result.Error != nil {
			return result.Error
		}
		slog.Info("account", "address", fx.Mailboxes[i].Address, "id", fx.Mailboxes[i].ID, "created", result.RowsAffected > 0)
		database.Where("mailbox_id = ?", fx.Mailboxes[i].ID).FirstOrCreate(&models.QuotaUsage{MailboxID: fx.Mailboxes[i].ID})
		if result.RowsAffected > 0 {
			if err := grants.EnsureReceive(database, &fx.Mailboxes[i]); err != nil {
				return err
			}
			if _, err := creds.SetPassword(fx.Mailboxes[i].ID, fx.PasswordHash, nil, time.Now()); err != nil {
				return err
			}
		}
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

func seedRBAC(database *gorm.DB, domain string) error {
	if err := bootstrap.EnsureRBAC(database); err != nil {
		return err
	}

	var superadminRole models.Role
	if err := database.Where("name = ?", "superadmin").First(&superadminRole).Error; err != nil {
		return err
	}
	var fixtureDomain models.Domain
	if err := database.Where("name = ?", domain).First(&fixtureDomain).Error; err != nil {
		return err
	}

	// The fixture admin: the account admin@<domain>, password admin123!@,
	// holding superadmin. A test fixture, like the password123 accounts: dev
	// instances and the e2e suite sign in as it directly, and this tool never
	// ships in an image. The API enforces password_change_required (a session
	// can do nothing else until it is cleared), so it is off here.
	adminPassword, err := auth.HashPassword("admin123!@")
	if err != nil {
		return err
	}
	address := "admin@" + domain
	var admin models.Mailbox
	result := database.Where("address = ?", address).
		Attrs(models.Mailbox{
			DomainID:    fixtureDomain.ID,
			LocalPart:   "admin",
			Address:     address,
			DisplayName: "Admin",
			QuotaBytes:  fixtureDomain.DefaultQuotaBytes,
			Active:      true,
		}).
		FirstOrCreate(&admin)
	if result.Error != nil {
		return result.Error
	}
	if err := database.Model(&admin).Update("password_change_required", false).Error; err != nil {
		return err
	}
	if result.RowsAffected > 0 {
		database.Where("mailbox_id = ?", admin.ID).FirstOrCreate(&models.QuotaUsage{MailboxID: admin.ID})
		if err := grants.EnsureReceive(database, &admin); err != nil {
			return err
		}
		if _, err := repositories.NewCredentialRepository(database).SetPassword(admin.ID, adminPassword, nil, time.Now()); err != nil {
			return err
		}
	}
	slog.Info("admin account", "address", address, "created", result.RowsAffected > 0)

	// Superadmin at "*" for the fixture admin.
	var created bool
	err = database.Transaction(func(tx *gorm.DB) error {
		var err error
		created, err = grants.EnsureSystem(tx, admin.ID, superadminRole.ID, grants.All, models.GrantActive)
		return err
	})
	if err != nil {
		return err
	}
	slog.Info("grant", "account", address, "role", "superadmin", "scope", grants.All, "created", created)

	return nil
}
