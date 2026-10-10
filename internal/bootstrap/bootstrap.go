// Package bootstrap brings an empty database to the state a fresh install needs
// before anyone can sign in: the RBAC roles and capabilities, and — when the
// operator supplies a password — a first superadmin.
//
// Both run on every API start and are idempotent. The production image ships
// only restmail-api (cmd/seed is deliberately kept out of it, since it makes
// fixture accounts with known passwords), so without this a fresh install had
// no admin, therefore no domain or mailbox, therefore nobody who could sign in.
package bootstrap

import (
	"fmt"
	"log/slog"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// MinAdminPasswordLength is the shortest bootstrap password accepted. The
// password is generated and stored by whoever deploys, so there is no reason
// for it to be guessable; refusing a short one catches a placeholder left in.
const MinAdminPasswordLength = 16

// EnsureRBAC creates the built-in capabilities and roles and wires them
// together. Rows that already exist are left as they are.
func EnsureRBAC(database *gorm.DB) error {
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
		{Name: "observability:read", Description: "View pipeline analytics funnel and per-message traces", Resource: "observability", Action: "read"},
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
		"observability:read",
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
		"observability:read",
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

	return nil
}

// EnsureFirstAdmin creates a superadmin named username with password, but only
// while there are no admin users at all. It reports whether it created one.
//
// The account starts in setup: its first sign-in can do nothing but set a new
// password and enroll a TOTP authenticator. The deploy-time password sits in the
// deploy tool's state, so until both are done the account is not the operator's
// alone. twoFactorAvailable says whether this server lets accounts enroll in
// 2FA; without it the setup could never finish, so no admin is created.
//
// An empty password means the operator did not ask for a bootstrap admin, and
// nothing happens. Once any admin exists this is a no-op forever, so changing
// or removing the password later cannot reset an account or add a second one.
func EnsureFirstAdmin(database *gorm.DB, username, password string, twoFactorAvailable bool) (bool, error) {
	if password == "" {
		return false, nil
	}
	if len(password) < MinAdminPasswordLength {
		return false, fmt.Errorf("bootstrap admin password is %d bytes; it must be at least %d", len(password), MinAdminPasswordLength)
	}
	if username == "" {
		username = "admin"
	}

	var count int64
	if err := database.Model(&models.AdminUser{}).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	if !twoFactorAvailable {
		return false, fmt.Errorf("a bootstrap admin must enroll in 2FA, but TOTP_2FA_ENABLED is off: it could never finish signing in")
	}

	var superadmin models.Role
	if err := database.Where("name = ?", "superadmin").First(&superadmin).Error; err != nil {
		return false, fmt.Errorf("superadmin role missing (run EnsureRBAC first): %w", err)
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, err
	}

	created := false
	err = database.Transaction(func(tx *gorm.DB) error {
		// Keyed on username, so two replicas starting together cannot both insert.
		var user models.AdminUser
		res := tx.Where("username = ?", username).
			Attrs(models.AdminUser{
				Username:               username,
				PasswordHash:           hash,
				Active:                 true,
				PasswordChangeRequired: true,
				TwoFactorRequired:      true,
			}).
			FirstOrCreate(&user)
		if res.Error != nil {
			return res.Error
		}
		created = res.RowsAffected > 0
		var ur models.UserRole
		return tx.Where("user_id = ? AND role_id = ?", user.ID, superadmin.ID).
			FirstOrCreate(&ur, models.UserRole{UserID: user.ID, RoleID: superadmin.ID}).Error
	})
	if err != nil {
		return false, err
	}
	if created {
		slog.Info("bootstrap admin created", "username", username)
	}
	return created, nil
}
