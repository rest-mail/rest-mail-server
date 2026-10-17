package repositories

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// openCredentialTestDB connects to the unit-test Postgres and migrates the
// tables app passwords touch. It skips (never fails) when no database is
// reachable.
func openCredentialTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: envIntOr("DB_PORT", 5432),
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("credential repo test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Mailbox{}, &models.Credential{}); err != nil {
		t.Skipf("credential repo test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// seedCredentialMailbox makes a mailbox of the test's own, removed afterwards
// with its credentials.
func seedCredentialMailbox(t *testing.T, gdb *gorm.DB) *models.Mailbox {
	t.Helper()
	local := "cred-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	mb := &models.Mailbox{DomainID: 1, LocalPart: local, Address: local + "@example.test", Password: "x", Active: true}
	if err := gdb.Create(mb).Error; err != nil {
		t.Fatalf("seed mailbox: %v", err)
	}
	t.Cleanup(func() {
		gdb.Where("owner_kind = ? AND owner_id = ?", models.CredentialOwnerMailbox, mb.ID).Delete(&models.Credential{})
		gdb.Delete(&models.Mailbox{}, mb.ID)
	})
	return mb
}

func refusalReason(t *testing.T, err error) *auth.AppPasswordRefusal {
	t.Helper()
	var refusal *auth.AppPasswordRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want an *auth.AppPasswordRefusal", err)
	}
	return refusal
}

func TestCredential_CreateStoresOnlyTheHash(t *testing.T) {
	gdb := openCredentialTestDB(t)
	repo := NewCredentialRepository(gdb)
	mb := seedCredentialMailbox(t, gdb)

	cred, password, err := repo.CreateAppPassword(mb.ID, "iPhone Mail", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	publicID, secret, ok := auth.ParseAppPassword(password)
	if !ok || publicID != cred.PublicID {
		t.Fatalf("app password %q does not parse to its public id %q", password, cred.PublicID)
	}

	var row models.Credential
	if err := gdb.First(&row, cred.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.SecretHash != auth.HashAppPasswordSecret(secret) {
		t.Error("the stored hash is not the secret's SHA-256")
	}
	if row.Kind != models.CredentialKindAppPassword || row.OwnerKind != models.CredentialOwnerMailbox ||
		row.OwnerID != mb.ID || row.CreatedByID != mb.ID || row.Name != "iPhone Mail" {
		t.Errorf("row = %+v", row)
	}
	if row.Scope.MailboxID != mb.ID || strings.Join(row.Scope.Capabilities, ",") != "mail:read,mail:write,mail:send" {
		t.Errorf("scope = %+v, want its own mailbox with read, write and send", row.Scope)
	}
	// The secret is nowhere in the row.
	var raw map[string]any
	if err := gdb.Raw("SELECT * FROM credentials WHERE id = ?", cred.ID).Scan(&raw).Error; err != nil {
		t.Fatalf("raw row: %v", err)
	}
	for col, v := range raw {
		if s, ok := v.(string); ok && strings.Contains(s, secret) {
			t.Errorf("column %s holds the secret", col)
		}
	}
}

func TestCredential_VerifyAppPassword(t *testing.T) {
	gdb := openCredentialTestDB(t)
	repo := NewCredentialRepository(gdb)
	mb := seedCredentialMailbox(t, gdb)
	other := seedCredentialMailbox(t, gdb)
	now := time.Now()

	cred, password, err := repo.CreateAppPassword(mb.ID, "Thunderbird", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	gotCred, gotMailbox, err := repo.VerifyAppPassword(mb.Address, password, now)
	if err != nil {
		t.Fatalf("VerifyAppPassword: %v", err)
	}
	if gotCred.ID != cred.ID || gotMailbox.ID != mb.ID {
		t.Errorf("verified credential %d of mailbox %d, want %d of %d", gotCred.ID, gotMailbox.ID, cred.ID, mb.ID)
	}

	publicID, secret, _ := auth.ParseAppPassword(password)
	wrongSecret := auth.AppPasswordPrefix + publicID + "_" + strings.Repeat("A", len(secret))
	unknownID := auth.AppPasswordPrefix + strings.Repeat("Z", len(publicID)) + "_" + secret

	for _, tc := range []struct {
		name, address, password, reason string
		credential                      bool // the refusal names the credential
	}{
		{"its mailbox's own password", mb.Address, "x", auth.AppPasswordMalformed, false},
		{"wrong secret", mb.Address, wrongSecret, auth.AppPasswordWrongSecret, false},
		{"unknown public id", mb.Address, unknownID, auth.AppPasswordUnknown, false},
		{"another mailbox's address", other.Address, password, auth.AppPasswordWrongAddress, true},
		{"an address nobody has", "nobody@example.test", password, auth.AppPasswordWrongAddress, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := repo.VerifyAppPassword(tc.address, tc.password, now)
			refusal := refusalReason(t, err)
			if refusal.Reason != tc.reason {
				t.Errorf("reason %q, want %q", refusal.Reason, tc.reason)
			}
			if (refusal.CredentialID != nil) != tc.credential {
				t.Errorf("credential id %v, want set=%v", refusal.CredentialID, tc.credential)
			}
			if tc.credential && *refusal.CredentialID != cred.ID {
				t.Errorf("credential id %d, want %d", *refusal.CredentialID, cred.ID)
			}
		})
	}
}

func TestCredential_RevocationAndExpiry(t *testing.T) {
	gdb := openCredentialTestDB(t)
	repo := NewCredentialRepository(gdb)
	mb := seedCredentialMailbox(t, gdb)
	now := time.Now()

	revoked, revokedPassword, err := repo.CreateAppPassword(mb.ID, "old phone", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if usable, err := repo.Usable(revoked.ID, now); err != nil || !usable {
		t.Fatalf("Usable before revoking = %v, %v", usable, err)
	}
	if err := repo.Revoke(mb.ID, revoked.ID, now); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, _, err = repo.VerifyAppPassword(mb.Address, revokedPassword, now)
	if refusal := refusalReason(t, err); refusal.Reason != auth.AppPasswordRevoked {
		t.Errorf("revoked: reason %q", refusal.Reason)
	}
	if usable, err := repo.Usable(revoked.ID, now); err != nil || usable {
		t.Errorf("Usable after revoking = %v, %v", usable, err)
	}
	// Revoking again, or as another mailbox, finds nothing; the row stays.
	if err := repo.Revoke(mb.ID, revoked.ID, now); !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("second Revoke = %v", err)
	}
	var count int64
	gdb.Model(&models.Credential{}).Where("id = ?", revoked.ID).Count(&count)
	if count != 1 {
		t.Errorf("a revoked credential's row is gone")
	}

	expiry := now.Add(time.Hour)
	expiring, expiringPassword, err := repo.CreateAppPassword(mb.ID, "n8n invoices", &expiry)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if _, _, err := repo.VerifyAppPassword(mb.Address, expiringPassword, now); err != nil {
		t.Errorf("before its expiry: %v", err)
	}
	later := expiry.Add(time.Second)
	_, _, err = repo.VerifyAppPassword(mb.Address, expiringPassword, later)
	if refusal := refusalReason(t, err); refusal.Reason != auth.AppPasswordExpired {
		t.Errorf("expired: reason %q", refusal.Reason)
	}
	if usable, _ := repo.Usable(expiring.ID, later); usable {
		t.Error("Usable after expiry")
	}

	// A disabled mailbox's app passwords stop working too.
	active, activePassword, err := repo.CreateAppPassword(mb.ID, "laptop", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	gdb.Model(&models.Mailbox{}).Where("id = ?", mb.ID).Update("active", false)
	_, _, err = repo.VerifyAppPassword(mb.Address, activePassword, now)
	if refusal := refusalReason(t, err); refusal.Reason != auth.AppPasswordMailboxInactive {
		t.Errorf("inactive mailbox: reason %q", refusal.Reason)
	}
	if usable, _ := repo.Usable(active.ID, now); usable {
		t.Error("Usable for an inactive mailbox")
	}

	// Listing leaves out the revoked one, and keeps the expired one.
	list, err := repo.ListForMailbox(mb.ID)
	if err != nil {
		t.Fatalf("ListForMailbox: %v", err)
	}
	var names []string
	for _, c := range list {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "laptop,n8n invoices" {
		t.Errorf("listed %q, want laptop,n8n invoices", got)
	}
}

func TestCredential_RevokeOnlyYourOwn(t *testing.T) {
	gdb := openCredentialTestDB(t)
	repo := NewCredentialRepository(gdb)
	mb := seedCredentialMailbox(t, gdb)
	other := seedCredentialMailbox(t, gdb)

	cred, password, err := repo.CreateAppPassword(mb.ID, "iPhone Mail", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if err := repo.Revoke(other.ID, cred.ID, time.Now()); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("revoking another mailbox's credential = %v", err)
	}
	if _, _, err := repo.VerifyAppPassword(mb.Address, password, time.Now()); err != nil {
		t.Errorf("still usable after another mailbox tried to revoke it: %v", err)
	}
}

func TestCredential_LastUsedAtMostOnceAMinute(t *testing.T) {
	gdb := openCredentialTestDB(t)
	repo := NewCredentialRepository(gdb)
	mb := seedCredentialMailbox(t, gdb)
	cred, _, err := repo.CreateAppPassword(mb.ID, "iPhone Mail", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	lastUsed := func() time.Time {
		t.Helper()
		var row models.Credential
		if err := gdb.First(&row, cred.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if row.LastUsedAt == nil {
			return time.Time{}
		}
		return *row.LastUsedAt
	}

	first := time.Now().Truncate(time.Second)
	if err := repo.TouchLastUsed(cred.ID, first); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	if got := lastUsed(); !got.Equal(first) {
		t.Fatalf("last used %v, want %v", got, first)
	}
	// Within the minute: not written again.
	if err := repo.TouchLastUsed(cred.ID, first.Add(59*time.Second)); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	if got := lastUsed(); !got.Equal(first) {
		t.Errorf("rewritten within the minute: %v", got)
	}
	// A minute on: written.
	next := first.Add(LastUsedEvery)
	if err := repo.TouchLastUsed(cred.ID, next); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	if got := lastUsed(); !got.Equal(next) {
		t.Errorf("last used %v after a minute, want %v", got, next)
	}
}
