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
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// MinAdminPasswordLength is the shortest bootstrap password accepted. The
// password is generated and stored by whoever deploys, so there is no reason
// for it to be guessable; refusing a short one catches a placeholder left in.
const MinAdminPasswordLength = 16

// BootstrapPasswordLifetime is how long the bootstrap password works after it
// is first applied. It is the only way into a server nobody has signed in to,
// and it sits in a Secret, a stack, or a pipeline's settings; an install that
// is started and abandoned must not stay open to whoever finds it later.
const BootstrapPasswordLifetime = 72 * time.Hour

// FirstAdminOutcome is what EnsureFirstAdmin did.
type FirstAdminOutcome int

const (
	FirstAdminUnchanged FirstAdminOutcome = iota
	// FirstAdminCreated: there was no admin, and now there is one, in setup.
	FirstAdminCreated
	// FirstAdminRearmed: the bootstrap admin was still on its deploy-time
	// password and was given a new one, with a fresh BootstrapPasswordLifetime.
	FirstAdminRearmed
)

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
		{Name: "audit:read", Description: "Read the audit log and verify its chain", Resource: "audit", Action: "read"},
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
		"audit:read",
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

// EnsureFirstAdmin creates a superadmin named username with password while
// there are no admin users at all, and keeps that account's deploy-time
// password in step with the deployment until the account chooses its own.
//
// The account starts in setup: its first sign-in can do nothing but set a new
// password and enroll a TOTP authenticator. The deploy-time password sits in the
// deploy tool's state, so until both are done the account is not the operator's
// alone. twoFactorAvailable says whether this server lets accounts enroll in
// 2FA; without it the setup could never finish, so no admin is created.
//
// The deploy-time password works for BootstrapPasswordLifetime from now, and
// restarting does not extend it. Deploying with a DIFFERENT password, while the
// account still has not chosen its own, re-arms it: the new password replaces
// the old, every session the old one opened ends, and the clock starts again.
// That is the recovery for a password that leaked or ran out before anyone
// signed in, and it grants nothing: until the account chooses a password,
// whoever can change the Secret could already read the one in it.
//
// Once the account has chosen its own password this is a no-op forever, so
// changing or removing the deployment's password later cannot reset it, and
// no second admin is ever added. An empty password means the operator did not
// ask for a bootstrap admin, and nothing happens; RequireAdmin then says
// whether the server has any way in at all.
func EnsureFirstAdmin(database *gorm.DB, username, password string, twoFactorAvailable bool, now time.Time) (FirstAdminOutcome, error) {
	return EnsureFirstAdminWith(database, username, BootstrapPassword(password), twoFactorAvailable, now)
}

// EnsureFirstAdminWith is EnsureFirstAdmin for a credential that may be the
// password or a bcrypt hash of it (BootstrapCredential). A different hash is a
// different password: it re-arms the account exactly as a different password
// does.
func EnsureFirstAdminWith(database *gorm.DB, username string, credential BootstrapCredential, twoFactorAvailable bool, now time.Time) (FirstAdminOutcome, error) {
	if credential.IsZero() {
		return FirstAdminUnchanged, nil
	}
	if err := credential.validate(); err != nil {
		return FirstAdminUnchanged, err
	}
	if username == "" {
		username = "admin"
	}

	var count int64
	if err := database.Model(&models.AdminUser{}).Count(&count).Error; err != nil {
		return FirstAdminUnchanged, err
	}
	if count > 0 {
		return rearmBootstrapAdmin(database, username, credential, now)
	}
	if !twoFactorAvailable {
		return FirstAdminUnchanged, fmt.Errorf("a bootstrap admin must enroll in 2FA, but TOTP_2FA_ENABLED is off: it could never finish signing in")
	}

	var superadmin, admin models.Role
	if err := database.Where("name = ?", "superadmin").First(&superadmin).Error; err != nil {
		return FirstAdminUnchanged, fmt.Errorf("superadmin role missing (run EnsureRBAC first): %w", err)
	}
	if err := database.Where("name = ?", "admin").First(&admin).Error; err != nil {
		return FirstAdminUnchanged, fmt.Errorf("admin role missing (run EnsureRBAC first): %w", err)
	}

	hash, err := credential.stored()
	if err != nil {
		return FirstAdminUnchanged, err
	}
	expires := now.Add(BootstrapPasswordLifetime)

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
				BootstrapExpiresAt:     &expires,
			}).
			FirstOrCreate(&user)
		if res.Error != nil {
			return res.Error
		}
		created = res.RowsAffected > 0
		// The admin role at "*" to work with day to day, and superadmin at "*"
		// to escalate to: eligible, switched on with a fresh code and a reason
		// only when needed (#346), so a stolen phone or session never carries
		// it. Only a new admin gets these: one that already holds either role
		// keeps it as it is.
		if _, err := grants.EnsureSystem(tx, models.GrantHolderAdmin, user.ID, admin.ID, grants.All, models.GrantActive); err != nil {
			return err
		}
		_, err := grants.EnsureSystem(tx, models.GrantHolderAdmin, user.ID, superadmin.ID, grants.All, models.GrantEligible)
		return err
	})
	if err != nil {
		return FirstAdminUnchanged, err
	}
	if !created {
		return FirstAdminUnchanged, nil
	}
	slog.Info("bootstrap admin created", "username", username, "password_expires", expires.UTC().Format(time.RFC3339))
	return FirstAdminCreated, nil
}

// ErrNoAdmin: the server has no admin, and was given no bootstrap password to
// make one with, so nobody could ever sign in.
var ErrNoAdmin = errors.New("there is no admin and no bootstrap admin password (RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD, or its hash in RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD_HASH): nobody could sign in")

// RequireAdmin answers ErrNoAdmin for a server nobody can sign in to. The first
// admin comes from the password the installer chose (EnsureFirstAdmin); there
// is no other way to make one over the network.
func RequireAdmin(database *gorm.DB) error {
	var count int64
	if err := database.Model(&models.AdminUser{}).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrNoAdmin
	}
	return nil
}

// rearmBootstrapAdmin gives the bootstrap admin a new deploy-time password,
// if it is still on one and the deployment now carries a different one.
//
// Only the account EnsureFirstAdmin made qualifies: the bootstrap username,
// still owing its password change, with an expiry stamped. One that has chosen
// its own password has its expiry cleared and is never touched again. The same
// password as before changes nothing, so a restart does not extend the expiry.
func rearmBootstrapAdmin(database *gorm.DB, username string, credential BootstrapCredential, now time.Time) (FirstAdminOutcome, error) {
	var user models.AdminUser
	err := database.
		Where("username = ? AND password_change_required = ? AND bootstrap_expires_at IS NOT NULL", username, true).
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return FirstAdminUnchanged, nil
	}
	if err != nil {
		return FirstAdminUnchanged, err
	}
	if credential.matches(user.PasswordHash) {
		return FirstAdminUnchanged, nil
	}

	hash, err := credential.stored()
	if err != nil {
		return FirstAdminUnchanged, err
	}
	expires := now.Add(BootstrapPasswordLifetime)
	rearmed := false
	err = database.Transaction(func(tx *gorm.DB) error {
		// Conditional on the account still owing its change, so a password
		// chosen between the read above and this write is never overwritten.
		res := tx.Model(&models.AdminUser{}).
			Where("id = ? AND password_change_required = ?", user.ID, true).
			Updates(map[string]interface{}{
				"password_hash":        hash,
				"bootstrap_expires_at": expires,
				"credential_epoch":     gorm.Expr("credential_epoch + 1"),
			})
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		rearmed = true
		// Whoever signed in with the old password is signed out. The epoch ends
		// their access tokens; a refresh re-mints at the current epoch rather
		// than checking it, so their refresh tokens must be revoked as well —
		// as a password change does.
		return repositories.NewRefreshTokenRepository(tx).RevokeAllForSubject("admin", user.ID)
	})
	if err != nil {
		return FirstAdminUnchanged, err
	}
	if !rearmed {
		return FirstAdminUnchanged, nil
	}
	slog.Info("bootstrap admin given the deployment's new password", "username", username, "password_expires", expires.UTC().Format(time.RFC3339))
	return FirstAdminRearmed, nil
}
