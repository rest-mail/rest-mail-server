// Package bootstrap brings an empty database to the state a fresh install needs
// before anyone can sign in: the roles and capabilities, and — when the
// operator supplies a password — the first account, admin@<mail domain>,
// holding superadmin.
//
// Both run on every API start and are idempotent. The production image ships
// only restmail-api (cmd/seed is deliberately kept out of it, since it makes
// fixture accounts with known passwords), so without this a fresh install had
// no account, therefore nobody who could sign in.
package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/pipeline"
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
	// FirstAdminRearmed: the bootstrap account was still on its deploy-time
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
		{Name: "users:read", Description: "View roles and grants", Resource: "users", Action: "read"},
		{Name: "users:write", Description: "Grant and remove roles", Resource: "users", Action: "write"},
		{Name: "users:delete", Description: "Remove accounts' grants when deleting them", Resource: "users", Action: "delete"},
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
		{Name: "agents:create", Description: "Create owned accounts (agents and sub accounts) on a domain", Resource: "agents", Action: "create"},
		{Name: models.CapMailRead, Description: "Read a mailbox's mail", Resource: "mail", Action: "read"},
		{Name: models.CapMailWrite, Description: "Change a mailbox's mail: folders, flags, drafts, contacts, filters", Resource: "mail", Action: "write"},
		{Name: models.CapMailSend, Description: "Send as a mailbox", Resource: "mail", Action: "send"},
		{Name: models.CapMailboxManage, Description: "Manage a mailbox's settings and credentials", Resource: "mailbox", Action: "manage"},
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
		{Name: models.RoleOwner, Description: "Everything on a mailbox: read, write, send and manage", SystemRole: true},
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
		"agents:create",
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

	var ownerRole models.Role
	database.Where("name = ?", models.RoleOwner).First(&ownerRole)
	for _, capName := range models.OwnerCapabilities {
		var cap models.Capability
		database.Where("name = ?", capName).First(&cap)
		var rc models.RoleCapability
		result := database.Where("role_id = ? AND capability_id = ?", ownerRole.ID, cap.ID).
			FirstOrCreate(&rc, models.RoleCapability{RoleID: ownerRole.ID, CapabilityID: cap.ID})
		if result.Error != nil {
			return result.Error
		}
	}
	slog.Info("role capabilities assigned", "role", models.RoleOwner, "count", len(models.OwnerCapabilities))

	// This data migration also runs on existing installations. It initializes
	// the server object once, after the complete capability vocabulary exists.
	return grants.EnsureServerCeiling(database)
}

// EnsureFirstAdmin makes the first account of a fresh install: address (the
// deployment's admin@<mail domain>), with password, in setup (it must choose
// its own password and enroll an authenticator before it can do anything
// else), holding admin at "*", and superadmin at "*" eligible (#346). Its
// domain is created too, not yet live, when the server does not host it yet.
//
// It does nothing on a server that already has an admin (an account holding
// any grant at "*"), except re-arm the bootstrap account while it is still on
// a deploy-time password (rearmBootstrapAdmin). The password works for
// BootstrapPasswordLifetime.
//
// twoFactorAvailable is whether this server can enroll an authenticator. The
// account must, so without it the account could never finish signing in, and
// making it is refused.
func EnsureFirstAdmin(database *gorm.DB, address, password string, twoFactorAvailable bool, now time.Time) (FirstAdminOutcome, error) {
	return EnsureFirstAdminWith(database, address, BootstrapPassword(password), twoFactorAvailable, now)
}

// EnsureFirstAdminWith is EnsureFirstAdmin for a credential that may be the
// password or a bcrypt hash of it (BootstrapCredential). A different hash is a
// different password: it re-arms the account exactly as a different password
// does.
func EnsureFirstAdminWith(database *gorm.DB, address string, credential BootstrapCredential, twoFactorAvailable bool, now time.Time) (FirstAdminOutcome, error) {
	if credential.IsZero() {
		return FirstAdminUnchanged, nil
	}
	if err := credential.validate(); err != nil {
		return FirstAdminUnchanged, err
	}
	address = strings.ToLower(strings.TrimSpace(address))
	local, domain, ok := strings.Cut(address, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return FirstAdminUnchanged, fmt.Errorf("the bootstrap account's address %q is not an address (RESTMAIL_BOOTSTRAP_ADMIN_ADDRESS)", address)
	}

	exists, err := AdminExists(database)
	if err != nil {
		return FirstAdminUnchanged, err
	}
	if exists {
		return rearmBootstrapAdmin(database, address, credential, now)
	}
	if !twoFactorAvailable {
		return FirstAdminUnchanged, fmt.Errorf("the bootstrap account must enroll in 2FA, but TOTP_2FA_ENABLED is off: it could never finish signing in")
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
		d, err := ensureDomain(tx, domain)
		if err != nil {
			return err
		}
		var account models.Mailbox
		res := tx.Where("address = ?", address).
			Attrs(models.Mailbox{
				DomainID:               d.ID,
				LocalPart:              local,
				Address:                address,
				QuotaBytes:             d.DefaultQuotaBytes,
				Active:                 true,
				PasswordChangeRequired: true,
				TwoFactorRequired:      true,
				BootstrapExpiresAt:     &expires,
			}).
			FirstOrCreate(&account)
		if res.Error != nil {
			return res.Error
		}
		created = res.RowsAffected > 0
		if created {
			if _, err := repositories.NewCredentialRepository(tx).SetPassword(account.ID, hash, nil, now); err != nil {
				return err
			}
		}
		// The admin role at "*" to work with day to day, and superadmin at "*"
		// to escalate to: eligible, switched on with a fresh code and a reason
		// only when needed (#346), so a stolen phone or session never carries
		// it. Only a new admin gets these: one that already holds either role
		// keeps it as it is.
		if _, err := grants.EnsureSystem(tx, account.ID, admin.ID, grants.All, models.GrantActive); err != nil {
			return err
		}
		_, err = grants.EnsureSystem(tx, account.ID, superadmin.ID, grants.All, models.GrantEligible)
		return err
	})
	if err != nil {
		return FirstAdminUnchanged, err
	}
	if !created {
		return FirstAdminUnchanged, nil
	}
	slog.Info("bootstrap account created", "address", address, "password_expires", expires.UTC().Format(time.RFC3339))
	return FirstAdminCreated, nil
}

// ensureDomain finds the domain name, or creates it as a fresh install's
// first domain: not live (it has no certificate yet), with the default
// inbound and outbound pipelines, as the admin API creates one.
func ensureDomain(tx *gorm.DB, name string) (*models.Domain, error) {
	var d models.Domain
	err := tx.Where("name = ?", name).First(&d).Error
	if err == nil {
		return &d, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	d = models.Domain{Name: name, ServerType: "restmail", Active: false, DefaultQuotaBytes: 1 << 30}
	if err := tx.Create(&d).Error; err != nil {
		return nil, err
	}
	for _, direction := range []string{"inbound", "outbound"} {
		if err := tx.Create(&models.Pipeline{
			DomainID:  d.ID,
			Direction: direction,
			Filters:   pipeline.DefaultPipelineJSON(direction),
			Active:    true,
		}).Error; err != nil {
			return nil, err
		}
	}
	return &d, nil
}

// AdminExists reports whether any account holds a grant at "*", active or
// eligible: whether the server has an admin.
func AdminExists(database *gorm.DB) (bool, error) {
	var count int64
	err := database.Model(&models.Grant{}).
		Where("holder_kind = ? AND scope = ?", models.GrantHolderAccount, grants.All).
		Count(&count).Error
	return count > 0, err
}

// ErrNoAdmin: the server has no admin, and was given no bootstrap password to
// make one with, so nobody could ever sign in.
var ErrNoAdmin = errors.New("there is no admin and no bootstrap admin password (RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD, or its hash in RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD_HASH): nobody could sign in")

// RequireAdmin answers ErrNoAdmin for a server nobody can administer. The
// first admin comes from the password the installer chose (EnsureFirstAdmin);
// there is no other way to make one over the network.
func RequireAdmin(database *gorm.DB) error {
	exists, err := AdminExists(database)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNoAdmin
	}
	return nil
}

// rearmBootstrapAdmin gives the bootstrap account a new deploy-time password,
// if it is still on one and the deployment now carries a different one.
//
// Only the account EnsureFirstAdmin made qualifies: the bootstrap address,
// still owing its password change, with an expiry stamped. One that has chosen
// its own password has its expiry cleared and is never touched again. The same
// password as before changes nothing, so a restart does not extend the expiry.
func rearmBootstrapAdmin(database *gorm.DB, address string, credential BootstrapCredential, now time.Time) (FirstAdminOutcome, error) {
	var account models.Mailbox
	err := database.
		Where("address = ? AND password_change_required = ? AND bootstrap_expires_at IS NOT NULL", address, true).
		First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return FirstAdminUnchanged, nil
	}
	if err != nil {
		return FirstAdminUnchanged, err
	}
	passwords, err := repositories.NewCredentialRepository(database).Passwords(account.ID, now)
	if err != nil {
		return FirstAdminUnchanged, err
	}
	for _, p := range passwords {
		if credential.matches(p.SecretHash) {
			return FirstAdminUnchanged, nil
		}
	}

	hash, err := credential.stored()
	if err != nil {
		return FirstAdminUnchanged, err
	}
	expires := now.Add(BootstrapPasswordLifetime)
	rearmed := false
	err = database.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Mailbox{}).
			Where("id = ? AND password_change_required = ?", account.ID, true).
			Updates(map[string]interface{}{
				"bootstrap_expires_at": expires,
				"credential_epoch":     gorm.Expr("credential_epoch + 1"),
			})
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		if _, err := repositories.NewCredentialRepository(tx).SetPassword(account.ID, hash, nil, now); err != nil {
			return err
		}
		rearmed = true
		return repositories.NewRefreshTokenRepository(tx).RevokeAllForAccount(account.ID)
	})
	if err != nil {
		return FirstAdminUnchanged, err
	}
	if !rearmed {
		return FirstAdminUnchanged, nil
	}
	slog.Info("bootstrap account given the deployment's new password", "address", address, "password_expires", expires.UTC().Format(time.RFC3339))
	return FirstAdminRearmed, nil
}
