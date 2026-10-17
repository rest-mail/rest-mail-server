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
	mb := &models.Mailbox{DomainID: 1, LocalPart: local, Address: local + "@example.test", Active: true}
	if err := gdb.Create(mb).Error; err != nil {
		t.Fatalf("seed mailbox: %v", err)
	}
	t.Cleanup(func() {
		gdb.Where("account_id = ?", mb.ID).Delete(&models.Credential{})
		gdb.Delete(&models.Mailbox{}, mb.ID)
	})
	return mb
}

func refusalReason(t *testing.T, err error) *auth.CredentialRefusal {
	t.Helper()
	var refusal *auth.CredentialRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want an *auth.CredentialRefusal", err)
	}
	return refusal
}

func TestCredential_CreateStoresOnlyTheHash(t *testing.T) {
	gdb := openCredentialTestDB(t)
	repo := NewCredentialRepository(gdb)
	mb := seedCredentialMailbox(t, gdb)

	cred, password, err := repo.CreateAppPassword(mb.ID, mb.ID, "iPhone Mail", nil)
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
	if row.Kind != models.CredentialKindAppPassword || row.AccountID != mb.ID ||
		row.CreatedByID == nil || *row.CreatedByID != mb.ID || row.Name != "iPhone Mail" {
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

	cred, password, err := repo.CreateAppPassword(mb.ID, mb.ID, "Thunderbird", nil)
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
		{"not an app password", mb.Address, "x", auth.CredentialWrongSecret, false},
		{"wrong secret", mb.Address, wrongSecret, auth.CredentialWrongSecret, false},
		{"unknown public id", mb.Address, unknownID, auth.CredentialUnknown, false},
		{"another mailbox's address", other.Address, password, auth.CredentialWrongAddress, true},
		{"an address nobody has", "nobody@example.test", password, auth.CredentialWrongAddress, true},
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

	revoked, revokedPassword, err := repo.CreateAppPassword(mb.ID, mb.ID, "old phone", nil)
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
	if refusal := refusalReason(t, err); refusal.Reason != auth.CredentialRevoked {
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
	expiring, expiringPassword, err := repo.CreateAppPassword(mb.ID, mb.ID, "n8n invoices", &expiry)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if _, _, err := repo.VerifyAppPassword(mb.Address, expiringPassword, now); err != nil {
		t.Errorf("before its expiry: %v", err)
	}
	later := expiry.Add(time.Second)
	_, _, err = repo.VerifyAppPassword(mb.Address, expiringPassword, later)
	if refusal := refusalReason(t, err); refusal.Reason != auth.CredentialExpired {
		t.Errorf("expired: reason %q", refusal.Reason)
	}
	if usable, _ := repo.Usable(expiring.ID, later); usable {
		t.Error("Usable after expiry")
	}

	// A disabled mailbox's app passwords stop working too.
	active, activePassword, err := repo.CreateAppPassword(mb.ID, mb.ID, "laptop", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	gdb.Model(&models.Mailbox{}).Where("id = ?", mb.ID).Update("active", false)
	_, _, err = repo.VerifyAppPassword(mb.Address, activePassword, now)
	if refusal := refusalReason(t, err); refusal.Reason != auth.CredentialAccountInactive {
		t.Errorf("inactive mailbox: reason %q", refusal.Reason)
	}
	if usable, _ := repo.Usable(active.ID, now); usable {
		t.Error("Usable for an inactive mailbox")
	}

	// Listing leaves out the revoked one, and keeps the expired one.
	list, err := repo.ListForAccount(mb.ID, models.CredentialKindAppPassword)
	if err != nil {
		t.Fatalf("ListForAccount: %v", err)
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

	cred, password, err := repo.CreateAppPassword(mb.ID, mb.ID, "iPhone Mail", nil)
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

// A new password for a mailbox revokes all its app passwords, expired ones
// too, and keeps their rows; one already revoked keeps when it was, and
// another mailbox's are untouched.
func TestCredential_RevokeAppPasswords(t *testing.T) {
	gdb := openCredentialTestDB(t)
	repo := NewCredentialRepository(gdb)
	mb := seedCredentialMailbox(t, gdb)
	other := seedCredentialMailbox(t, gdb)
	now := time.Now()

	phone, phonePassword, err := repo.CreateAppPassword(mb.ID, mb.ID, "iPhone Mail", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	past := now.Add(-time.Hour)
	expired, _, err := repo.CreateAppPassword(mb.ID, mb.ID, "n8n invoices", &past)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	earlier, _, err := repo.CreateAppPassword(mb.ID, mb.ID, "old laptop", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	earlierAt := now.Add(-24 * time.Hour)
	if err := repo.Revoke(mb.ID, earlier.ID, earlierAt); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	others, othersPassword, err := repo.CreateAppPassword(other.ID, other.ID, "Thunderbird", nil)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}

	revoked, err := repo.RevokeAppPasswords(mb.ID, now)
	if err != nil {
		t.Fatalf("RevokeAppPasswords: %v", err)
	}
	if len(revoked) != 2 || revoked[0].ID != phone.ID || revoked[1].ID != expired.ID ||
		revoked[0].RevokedAt == nil || revoked[0].PublicID != phone.PublicID || revoked[1].Name != "n8n invoices" {
		t.Fatalf("revoked %+v, want the phone's and the expired one", revoked)
	}
	_, _, err = repo.VerifyAppPassword(mb.Address, phonePassword, now)
	if refusal := refusalReason(t, err); refusal.Reason != auth.CredentialRevoked {
		t.Errorf("after a new password: reason %q", refusal.Reason)
	}
	// The SMTP gateway's check before each message.
	if usable, err := repo.Usable(phone.ID, now); err != nil || usable {
		t.Errorf("Usable after a new password = %v, %v", usable, err)
	}
	var rows []models.Credential
	gdb.Where("account_id = ?", mb.ID).Order("id").Find(&rows)
	if len(rows) != 3 {
		t.Fatalf("%d rows left, want all 3", len(rows))
	}
	for _, row := range rows {
		if row.RevokedAt == nil {
			t.Errorf("%q is not revoked", row.Name)
		}
		if row.ID == earlier.ID && row.RevokedAt.Sub(earlierAt).Abs() > time.Millisecond {
			t.Errorf("an earlier revocation moved from %v to %v", earlierAt, row.RevokedAt)
		}
	}

	if _, _, err := repo.VerifyAppPassword(other.Address, othersPassword, now); err != nil {
		t.Errorf("another mailbox's app password stopped working: %v", err)
	}
	if usable, err := repo.Usable(others.ID, now); err != nil || !usable {
		t.Errorf("Usable for another mailbox's = %v, %v", usable, err)
	}

	// Again: nothing left to revoke.
	if again, err := repo.RevokeAppPasswords(mb.ID, now); err != nil || len(again) != 0 {
		t.Errorf("revoking again = %+v, %v", again, err)
	}
}

func TestCredential_LastUsedAtMostOnceAMinute(t *testing.T) {
	gdb := openCredentialTestDB(t)
	repo := NewCredentialRepository(gdb)
	mb := seedCredentialMailbox(t, gdb)
	cred, _, err := repo.CreateAppPassword(mb.ID, mb.ID, "iPhone Mail", nil)
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

type countingMemo struct {
	remembered map[string]bool
	hits       int
}

func (m *countingMemo) key(c *models.Credential, secret string) string {
	return strconv.FormatUint(uint64(c.ID), 10) + "|" + c.SecretHash + "|" + secret
}

func (m *countingMemo) Remembered(c *models.Credential, secret string) bool {
	if m.remembered[m.key(c, secret)] {
		m.hits++
		return true
	}
	return false
}

func (m *countingMemo) Remember(c *models.Credential, secret string) {
	m.remembered[m.key(c, secret)] = true
}

// TestCredential_PasswordsAreCredentials: an account's password is a
// credential; setting a new one revokes the old, a password-less account
// matches nothing, and HTTP Basic takes a password only from an account that
// is active, set up and without 2FA.
func TestCredential_PasswordsAreCredentials(t *testing.T) {
	gdb := openCredentialTestDB(t)
	repo := NewCredentialRepository(gdb)
	mb := seedCredentialMailbox(t, gdb)
	now := time.Now()

	if ok, err := repo.HasPassword(mb.ID, now); err != nil || ok {
		t.Fatalf("a new account has a password: %v %v", ok, err)
	}
	if c, err := repo.MatchPassword(mb.ID, "anything", now); err != nil || c != nil {
		t.Fatalf("a password-less account matched: %v %v", c, err)
	}
	hash, err := auth.HashPassword("first password!")
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.SetPassword(mb.ID, hash, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != models.CredentialKindPassword || first.PublicID == "" || first.CreatedByID != nil {
		t.Errorf("password credential = %+v", first)
	}
	if c, _ := repo.MatchPassword(mb.ID, "first password!", now); c == nil || c.ID != first.ID {
		t.Fatalf("the password does not match its credential: %v", c)
	}
	hash2, _ := auth.HashPassword("second password!")
	if _, err := repo.SetPassword(mb.ID, hash2, &mb.ID, now); err != nil {
		t.Fatal(err)
	}
	if c, _ := repo.MatchPassword(mb.ID, "first password!", now); c != nil {
		t.Error("the old password still matches after a new one was set")
	}
	var rows []models.Credential
	gdb.Where("account_id = ? AND kind = ?", mb.ID, models.CredentialKindPassword).Order("id").Find(&rows)
	if len(rows) != 2 || rows[0].RevokedAt == nil || rows[1].RevokedAt != nil {
		t.Fatalf("password rows = %+v, want the old one revoked and kept", rows)
	}

	memo := &countingMemo{remembered: map[string]bool{}}
	cred, account, err := repo.VerifyBasic(mb.Address, "second password!", now, memo)
	if err != nil || cred.ID != rows[1].ID || account.ID != mb.ID {
		t.Fatalf("VerifyBasic(password) = %v %v %v", cred, account, err)
	}
	if _, _, err := repo.VerifyBasic(mb.Address, "second password!", now, memo); err != nil || memo.hits != 1 {
		t.Errorf("the second check was not remembered: hits %d, err %v", memo.hits, err)
	}
	if got := refusalReason(t, func() error { _, _, e := repo.VerifyBasic(mb.Address, "wrong", now, memo); return e }()); got.Reason != auth.CredentialWrongPassword || got.CredentialID != nil {
		t.Errorf("wrong password refused as %+v", got)
	}
	if got := refusalReason(t, func() error {
		_, _, e := repo.VerifyBasic("nobody@example.test", "second password!", now, nil)
		return e
	}()); got.Reason != auth.CredentialWrongPassword {
		t.Errorf("unknown address refused as %+v", got)
	}

	// An authenticator makes the password unusable over Basic: a mail client
	// needs an app password.
	totp := &models.Credential{AccountID: mb.ID, Kind: models.CredentialKindTOTP, Name: "Authenticator", PublicID: "t" + strconv.FormatInt(time.Now().UnixNano(), 36), Scope: accountScope(mb.ID)}
	if err := gdb.Create(totp).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.VerifyBasic(mb.Address, "second password!", now, nil); err != nil {
		t.Errorf("an unconfirmed authenticator already counts: %v", err)
	}
	gdb.Model(totp).Update("confirmed_at", now)
	if got := refusalReason(t, func() error { _, _, e := repo.VerifyBasic(mb.Address, "second password!", now, nil); return e }()); got.Reason != auth.CredentialNeedsAppPassword || got.CredentialID == nil {
		t.Errorf("password of a 2FA account refused as %+v", got)
	}
	gdb.Model(totp).Update("revoked_at", now)

	// An account in setup, or disabled, cannot use its password over Basic.
	gdb.Model(&models.Mailbox{}).Where("id = ?", mb.ID).Update("password_change_required", true)
	if got := refusalReason(t, func() error { _, _, e := repo.VerifyBasic(mb.Address, "second password!", now, nil); return e }()); got.Reason != auth.CredentialAccountInSetup {
		t.Errorf("password of an account in setup refused as %+v", got)
	}
	gdb.Model(&models.Mailbox{}).Where("id = ?", mb.ID).Updates(map[string]any{"password_change_required": false, "active": false})
	if got := refusalReason(t, func() error { _, _, e := repo.VerifyBasic(mb.Address, "second password!", now, nil); return e }()); got.Reason != auth.CredentialAccountInactive {
		t.Errorf("password of a disabled account refused as %+v", got)
	}
}
