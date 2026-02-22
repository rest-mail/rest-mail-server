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

	slog.Info("seeding mail3.test database with test data")

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

	if err := seedRBAC(database); err != nil {
		slog.Error("RBAC seeding failed", "error", err)
		os.Exit(1)
	}

	slog.Info("seeding completed successfully")
}

func seed(database *gorm.DB) error {
	// Create mail3.test domain (mail1/mail2 are seeded via SQL init scripts in their own databases)
	domains := []models.Domain{
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

	// Create mail3.test mailboxes
	mailboxes := []models.Mailbox{
		{DomainID: domains[0].ID, LocalPart: "eve", Address: "eve@mail3.test", Password: defaultPassword, DisplayName: "Eve Wilson", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[0].ID, LocalPart: "frank", Address: "frank@mail3.test", Password: defaultPassword, DisplayName: "Frank Miller", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[0].ID, LocalPart: "postmaster", Address: "postmaster@mail3.test", Password: defaultPassword, DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
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

	// Create mail3.test aliases
	aliases := []models.Alias{
		{DomainID: domains[0].ID, SourceAddress: "info@mail3.test", DestinationAddress: "eve@mail3.test", Active: true},
		{DomainID: domains[0].ID, SourceAddress: "admin@mail3.test", DestinationAddress: "eve@mail3.test", Active: true},
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

func seedRBAC(database *gorm.DB) error {
	slog.Info("seeding RBAC system")

	// Create capabilities
	capabilities := []models.Capability{
		{Name: "*", Description: "All permissions (superadmin wildcard)", Resource: "*", Action: "*"},
		{Name: "domains:read", Description: "View domains", Resource: "domains", Action: "read"},
		{Name: "domains:write", Description: "Create/update domains", Resource: "domains", Action: "write"},
		{Name: "domains:delete", Description: "Delete domains", Resource: "domains", Action: "delete"},
		{Name: "mailboxes:read", Description: "View mailboxes", Resource: "mailboxes", Action: "read"},
		{Name: "mailboxes:write", Description: "Create/update mailboxes", Resource: "mailboxes", Action: "write"},
		{Name: "mailboxes:delete", Description: "Delete mailboxes", Resource: "mailboxes", Action: "delete"},
		{Name: "users:read", Description: "View admin users", Resource: "users", Action: "read"},
		{Name: "users:write", Description: "Create/update admin users", Resource: "users", Action: "write"},
		{Name: "users:delete", Description: "Delete admin users", Resource: "users", Action: "delete"},
		{Name: "pipelines:read", Description: "View pipelines", Resource: "pipelines", Action: "read"},
		{Name: "pipelines:write", Description: "Create/update pipelines", Resource: "pipelines", Action: "write"},
		{Name: "pipelines:delete", Description: "Delete pipelines", Resource: "pipelines", Action: "delete"},
		{Name: "messages:send_bulk", Description: "Send bulk messages", Resource: "messages", Action: "send_bulk"},
		{Name: "messages:read", Description: "Read messages", Resource: "messages", Action: "read"},
		{Name: "queue:read", Description: "View outbound queue", Resource: "queue", Action: "read"},
		{Name: "queue:manage", Description: "Manage outbound queue", Resource: "queue", Action: "manage"},
		{Name: "bans:read", Description: "View IP bans", Resource: "bans", Action: "read"},
		{Name: "bans:write", Description: "Create/update IP bans", Resource: "bans", Action: "write"},
		{Name: "bans:delete", Description: "Delete IP bans", Resource: "bans", Action: "delete"},
	}

	for i := range capabilities {
		result := database.Where("name = ?", capabilities[i].Name).FirstOrCreate(&capabilities[i])
		if result.Error != nil {
			return result.Error
		}
		slog.Info("capability", "name", capabilities[i].Name, "created", result.RowsAffected > 0)
	}

	// Create roles
	roles := []models.Role{
		{Name: "superadmin", Description: "Full system access", SystemRole: true},
		{Name: "admin", Description: "Standard administrator", SystemRole: true},
		{Name: "readonly", Description: "Read-only access", SystemRole: true},
	}

	for i := range roles {
		result := database.Where("name = ?", roles[i].Name).FirstOrCreate(&roles[i])
		if result.Error != nil {
			return result.Error
		}
		slog.Info("role", "name", roles[i].Name, "created", result.RowsAffected > 0)
	}

	// Assign capabilities to superadmin role (wildcard permission)
	var superadminRole models.Role
	database.Where("name = ?", "superadmin").First(&superadminRole)
	var wildcardCap models.Capability
	database.Where("name = ?", "*").First(&wildcardCap)

	var existingRC models.RoleCapability
	result := database.Where("role_id = ? AND capability_id = ?", superadminRole.ID, wildcardCap.ID).
		FirstOrCreate(&existingRC, models.RoleCapability{
			RoleID:       superadminRole.ID,
			CapabilityID: wildcardCap.ID,
		})
	if result.Error != nil {
		return result.Error
	}
	slog.Info("role capability", "role", "superadmin", "cap", "*", "created", result.RowsAffected > 0)

	// Assign capabilities to admin role
	var adminRole models.Role
	database.Where("name = ?", "admin").First(&adminRole)
	adminCaps := []string{
		"domains:read", "domains:write", "domains:delete",
		"mailboxes:read", "mailboxes:write", "mailboxes:delete",
		"pipelines:read", "pipelines:write", "pipelines:delete",
		"users:read", "messages:read",
		"queue:read", "queue:manage",
		"bans:read", "bans:write", "bans:delete",
	}
	for _, capName := range adminCaps {
		var cap models.Capability
		database.Where("name = ?", capName).First(&cap)
		var rc models.RoleCapability
		result := database.Where("role_id = ? AND capability_id = ?", adminRole.ID, cap.ID).
			FirstOrCreate(&rc, models.RoleCapability{
				RoleID:       adminRole.ID,
				CapabilityID: cap.ID,
			})
		if result.Error != nil {
			return result.Error
		}
	}
	slog.Info("role capabilities assigned", "role", "admin", "count", len(adminCaps))

	// Assign capabilities to readonly role
	var readonlyRole models.Role
	database.Where("name = ?", "readonly").First(&readonlyRole)
	readonlyCaps := []string{
		"domains:read", "mailboxes:read", "pipelines:read",
		"users:read", "messages:read", "queue:read", "bans:read",
	}
	for _, capName := range readonlyCaps {
		var cap models.Capability
		database.Where("name = ?", capName).First(&cap)
		var rc models.RoleCapability
		result := database.Where("role_id = ? AND capability_id = ?", readonlyRole.ID, cap.ID).
			FirstOrCreate(&rc, models.RoleCapability{
				RoleID:       readonlyRole.ID,
				CapabilityID: cap.ID,
			})
		if result.Error != nil {
			return result.Error
		}
	}
	slog.Info("role capabilities assigned", "role", "readonly", "count", len(readonlyCaps))

	// Create initial admin user (username: admin, password: admin123!@)
	adminPassword, err := auth.HashPassword("admin123!@")
	if err != nil {
		return err
	}

	var adminUser models.AdminUser
	result = database.Where("username = ?", "admin").FirstOrCreate(&adminUser, models.AdminUser{
		Username:               "admin",
		Email:                  "admin@localhost",
		PasswordHash:           adminPassword,
		PasswordChangeRequired: true,
		Active:                 true,
	})
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
