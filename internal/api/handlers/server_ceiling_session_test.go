package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ceilingSessionDB answers the queries mayReach makes for alice's own mailbox
// (id 7), with a server ceiling holding only ceiling and no grants of alice's
// own.
type ceilingSessionDB struct {
	serviceProbeTestDB
	ceiling []string
}

func (d *ceilingSessionDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *ceilingSessionDB) Driver() driver.Driver                        { return d }
func (d *ceilingSessionDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *ceilingSessionDB) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if rows, ok := grantRowsWithin(q, d.ceiling); ok {
		return rows, nil
	}
	if strings.Contains(q, `FROM "mailboxes"`) {
		return &deliveryReadRows{columns: []string{"id", "address", "active"}, values: []driver.Value{int64(7), "alice@example.test", true}}, nil
	}
	return nil, errors.New("unexpected SQL: " + q)
}

// TestMayReachBoundsAppPasswordByServerCeiling: an app password reads, writes
// and sends on its own mailbox only while the server's ceiling allows it,
// like the account's password does. Before, an app password skipped the
// grant check, so it kept every mail capability the ceiling had removed.
func TestMayReachBoundsAppPasswordByServerCeiling(t *testing.T) {
	mail := []string{models.CapMailRead, models.CapMailWrite, models.CapMailSend}
	for _, kind := range []string{models.CredentialKindAppPassword, models.CredentialKindPassword} {
		claims := &auth.Claims{AccountID: 7, Address: "alice@example.test", TokenType: "access", CredentialID: 1, CredentialKind: kind}
		for _, removed := range mail {
			var ceiling []string
			for _, c := range models.OwnerCapabilities {
				if c != removed {
					ceiling = append(ceiling, c)
				}
			}
			pool := sql.OpenDB(&ceilingSessionDB{ceiling: ceiling})
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			for _, capability := range mail {
				_, err := mayReach(db, claims, 7, capability)
				if err != nil && !errors.Is(err, errNoAccess) {
					t.Fatalf("%s %s: %v", kind, capability, err)
				}
				if allowed, want := err == nil, capability != removed; allowed != want {
					t.Errorf("%s: %s allowed = %v with %s removed from the server ceiling, want %v", kind, capability, allowed, removed, want)
				}
			}
		}
	}
}

// TestMayReachAppliesServerCeilingToOpenSessions: the protocol gateways call
// the API with the credentials a mail client signed in with, on every
// operation. A session signed in while the server allowed mail:read keeps
// reading while it stays allowed, is refused on its next call once the
// server's ceiling removes mail:read, whether it signed in with an app
// password or the account's password, and reads again once it is restored.
// The global superadmin is exempt throughout.
func TestMayReachAppliesServerCeilingToOpenSessions(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(accountModels...); err != nil {
		t.Fatal(err)
	}
	ensureDefaultServerCeiling(t, db)

	domain := models.Domain{Name: "ceiling-session.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	account := func(local string) models.Mailbox {
		t.Helper()
		a := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + domain.Name, Active: true}
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		return a
	}
	alice, root := account("alice"), account("root")

	wildcard := models.Capability{Name: grants.Wildcard}
	if err := db.Where("name = ?", wildcard.Name).FirstOrCreate(&wildcard).Error; err != nil {
		t.Fatal(err)
	}
	superadmin := models.Role{Name: "ceiling-session-superadmin", SystemRole: true}
	if err := db.Create(&superadmin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.RoleCapability{RoleID: superadmin.ID, CapabilityID: wildcard.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: root.ID,
		RoleID: superadmin.ID, Scope: grants.All, Mode: models.GrantActive,
		GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	var server models.Role
	if err := db.Where("name = ?", models.RoleServer).First(&server).Error; err != nil {
		t.Fatal(err)
	}
	var read models.Capability
	if err := db.Where("name = ?", models.CapMailRead).First(&read).Error; err != nil {
		t.Fatal(err)
	}

	claimsFor := func(a models.Mailbox, kind string) *auth.Claims {
		return &auth.Claims{AccountID: a.ID, Address: a.Address, TokenType: "access", CredentialID: 1, CredentialKind: kind}
	}
	sessions := []struct {
		name   string
		claims *auth.Claims
		target models.Mailbox
		exempt bool
	}{
		{"app password", claimsFor(alice, models.CredentialKindAppPassword), alice, false},
		{"password", claimsFor(alice, models.CredentialKindPassword), alice, false},
		{"superadmin app password", claimsFor(root, models.CredentialKindAppPassword), root, true},
		{"superadmin password", claimsFor(root, models.CredentialKindPassword), root, true},
	}
	reaches := func(t *testing.T, claims *auth.Claims, target models.Mailbox) bool {
		t.Helper()
		_, err := mayReach(db, claims, target.ID, models.CapMailRead)
		if err != nil && !errors.Is(err, errNoAccess) {
			t.Fatalf("mayReach: %v", err)
		}
		return err == nil
	}

	for _, s := range sessions {
		for range 2 {
			if !reaches(t, s.claims, s.target) {
				t.Fatalf("%s: refused while the server allows mail:read", s.name)
			}
		}
	}

	if err := db.Where("role_id = ? AND capability_id = ?", server.ID, read.ID).
		Delete(&models.RoleCapability{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if got := reaches(t, s.claims, s.target); got != s.exempt {
			t.Errorf("%s: reaches = %v after the server lost mail:read, want %v", s.name, got, s.exempt)
		}
	}

	if err := db.Create(&models.RoleCapability{RoleID: server.ID, CapabilityID: read.ID}).Error; err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if !reaches(t, s.claims, s.target) {
			t.Errorf("%s: refused after mail:read was restored", s.name)
		}
	}
}
