package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(logHandler))

	slog.Info("seeding database with test data")

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

	if err := db.AutoMigrate(database); err != nil {
		slog.Error("failed to migrate", "error", err)
		os.Exit(1)
	}

	if err := seed(database); err != nil {
		slog.Error("seeding failed", "error", err)
		os.Exit(1)
	}

	slog.Info("seeding completed successfully")
}

func seed(database *gorm.DB) error {
	// Create domains
	domains := []models.Domain{
		{Name: "mail1.test", ServerType: "traditional", Active: true, DefaultQuotaBytes: 1073741824},
		{Name: "mail2.test", ServerType: "traditional", Active: true, DefaultQuotaBytes: 1073741824},
		{Name: "mail3.test", ServerType: "restmail", Active: true, DefaultQuotaBytes: 1073741824},
	}

	for i := range domains {
		result := database.Where("name = ?", domains[i].Name).FirstOrCreate(&domains[i])
		if result.Error != nil {
			return result.Error
		}
		slog.Info("domain", "name", domains[i].Name, "id", domains[i].ID, "created", result.RowsAffected > 0)
	}

	// Default password for all test accounts
	defaultPassword, err := auth.HashPassword("password123")
	if err != nil {
		return err
	}

	// Create mailboxes
	mailboxes := []models.Mailbox{
		// mail1.test users
		{DomainID: domains[0].ID, LocalPart: "alice", Address: "alice@mail1.test", Password: defaultPassword, DisplayName: "Alice Smith", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[0].ID, LocalPart: "bob", Address: "bob@mail1.test", Password: defaultPassword, DisplayName: "Bob Jones", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[0].ID, LocalPart: "postmaster", Address: "postmaster@mail1.test", Password: defaultPassword, DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
		// mail2.test users
		{DomainID: domains[1].ID, LocalPart: "charlie", Address: "charlie@mail2.test", Password: defaultPassword, DisplayName: "Charlie Brown", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[1].ID, LocalPart: "diana", Address: "diana@mail2.test", Password: defaultPassword, DisplayName: "Diana Prince", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[1].ID, LocalPart: "postmaster", Address: "postmaster@mail2.test", Password: defaultPassword, DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
		// mail3.test users (rest-mail)
		{DomainID: domains[2].ID, LocalPart: "eve", Address: "eve@mail3.test", Password: defaultPassword, DisplayName: "Eve Wilson", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[2].ID, LocalPart: "frank", Address: "frank@mail3.test", Password: defaultPassword, DisplayName: "Frank Miller", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[2].ID, LocalPart: "postmaster", Address: "postmaster@mail3.test", Password: defaultPassword, DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
	}

	for i := range mailboxes {
		result := database.Where("address = ?", mailboxes[i].Address).FirstOrCreate(&mailboxes[i])
		if result.Error != nil {
			return result.Error
		}
		slog.Info("mailbox", "address", mailboxes[i].Address, "id", mailboxes[i].ID, "created", result.RowsAffected > 0)

		// Create quota usage record
		database.Where("mailbox_id = ?", mailboxes[i].ID).FirstOrCreate(&models.QuotaUsage{MailboxID: mailboxes[i].ID})
	}

	// Create aliases
	aliases := []models.Alias{
		{DomainID: domains[0].ID, SourceAddress: "info@mail1.test", DestinationAddress: "alice@mail1.test", Active: true},
		{DomainID: domains[1].ID, SourceAddress: "info@mail2.test", DestinationAddress: "charlie@mail2.test", Active: true},
		{DomainID: domains[2].ID, SourceAddress: "info@mail3.test", DestinationAddress: "eve@mail3.test", Active: true},
		{DomainID: domains[2].ID, SourceAddress: "admin@mail3.test", DestinationAddress: "eve@mail3.test", Active: true},
	}

	for i := range aliases {
		result := database.Where("source_address = ? AND destination_address = ?", aliases[i].SourceAddress, aliases[i].DestinationAddress).FirstOrCreate(&aliases[i])
		if result.Error != nil {
			return result.Error
		}
		slog.Info("alias", "source", aliases[i].SourceAddress, "dest", aliases[i].DestinationAddress, "created", result.RowsAffected > 0)
	}

	// Create webmail accounts for mail3.test users
	for _, addr := range []string{"eve@mail3.test", "frank@mail3.test"} {
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
