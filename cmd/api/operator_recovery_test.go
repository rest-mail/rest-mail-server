package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

const deployedPassword = "the-deployments-own-password"

// bootstrapAdmin makes the admin a fresh install has, signed in, with an
// authenticator, as an operator would find it.
// adminAddress is the fresh install's bootstrap account.
const adminAddress = "admin@example.com"

func bootstrapAdmin(t *testing.T, gdb *gorm.DB) models.Mailbox {
	t.Helper()
	migrate(t)
	if _, err := bootstrap.EnsureFirstAdmin(gdb, adminAddress, deployedPassword, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	var admin models.Mailbox
	if err := gdb.Where("address = ?", adminAddress).First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.NewTwoFactorRepository(gdb).Enroll(admin.ID, "x", nil); err != nil {
		t.Fatal(err)
	}
	gdb.Model(&models.Credential{}).Where("account_id = ? AND kind = ?", admin.ID, models.CredentialKindTOTP).Update("confirmed_at", time.Now())
	gdb.Create(&models.RefreshToken{Jti: "admin-session", AccountID: admin.ID, SessionID: "admin-session", Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)})
	return admin
}

// passwordOf is the hash of the account's usable password.
func passwordOf(t *testing.T, gdb *gorm.DB, accountID uint) string {
	t.Helper()
	var hashes []string
	gdb.Model(&models.Credential{}).Where("account_id = ? AND kind = ? AND revoked_at IS NULL", accountID, models.CredentialKindPassword).Pluck("secret_hash", &hashes)
	if len(hashes) != 1 {
		t.Fatalf("account %d has %d usable passwords, want 1", accountID, len(hashes))
	}
	return hashes[0]
}

// migrate brings the scratch database to the schema the server would.
func migrate(t *testing.T) {
	t.Helper()
	var errOut strings.Builder
	database, _, status := openDatabase(&errOut)
	if status != exitOK {
		t.Fatalf("migrate: %s", errOut.String())
	}
	closeDatabase(database)
}

func resetWith(args []string, password string) (int, string, string) {
	var out, errOut strings.Builder
	status := resetPassword(args, strings.NewReader(password+"\n"), &out, &errOut)
	return status, out.String(), errOut.String()
}

func TestResetPassword_PutsTheAdminBackInSetup(t *testing.T) {
	gdb := scratchDatabase(t)
	before := bootstrapAdmin(t, gdb)

	status, out, errOut := resetWith([]string{"--address", adminAddress}, "a-freshly-generated-password")
	if status != exitOK {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}
	var result struct {
		Address       string   `json:"address"`
		SetupRequired []string `json:"setup_required"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Address != adminAddress || len(result.SetupRequired) != 2 {
		t.Fatalf("output = %q", out)
	}

	var after models.Mailbox
	gdb.First(&after, before.ID)
	if auth.CheckPassword("a-freshly-generated-password", passwordOf(t, gdb, before.ID)) != nil {
		t.Error("the new password does not sign in")
	}
	if !after.PasswordChangeRequired || !after.TwoFactorRequired {
		t.Error("the account is not back in setup")
	}
	if after.CredentialEpoch <= before.CredentialEpoch {
		t.Error("the epoch was not bumped, so access tokens survive")
	}
	var live int64
	gdb.Model(&models.RefreshToken{}).Where("account_id = ? AND status = ?", before.ID, models.RefreshTokenActive).Count(&live)
	if live != 0 {
		t.Error("a refresh token survived, so the old session can re-mint")
	}
	var factors int64
	gdb.Model(&models.Credential{}).Where("account_id = ? AND kind = ? AND revoked_at IS NULL", before.ID, models.CredentialKindTOTP).Count(&factors)
	if factors != 0 {
		t.Error("the old authenticator survived")
	}

	// A restart with the deployment's password still in the Secret must not
	// take back the password someone was just handed.
	if outcome, err := bootstrap.EnsureFirstAdmin(gdb, adminAddress, deployedPassword, true, time.Now()); err != nil || outcome != bootstrap.FirstAdminUnchanged {
		t.Fatalf("restart: outcome %v, err %v", outcome, err)
	}
	if auth.CheckPassword("a-freshly-generated-password", passwordOf(t, gdb, before.ID)) != nil {
		t.Error("a restart replaced the reset password with the deployment's")
	}
}

func TestResetPassword_Refusals(t *testing.T) {
	gdb := scratchDatabase(t)
	bootstrapAdmin(t, gdb)

	if status, _, errOut := resetWith([]string{"--address", "nobody@example.com"}, "a-freshly-generated-password"); status != exitNoSuchAccount || errorCodeIn(t, errOut) != "no_such_account" {
		t.Errorf("nobody: status %d, %s", status, errOut)
	}
	if status, _, errOut := resetWith([]string{"--address", adminAddress}, "short"); status != exitUsage || errorCodeIn(t, errOut) != "password_too_short" {
		t.Errorf("short: status %d, %s", status, errOut)
	}
	if status, _, errOut := resetWith(nil, "a-freshly-generated-password"); status != exitUsage {
		t.Errorf("no address: status %d, %s", status, errOut)
	}
}

func TestRevokeSessions_EndsEveryRefreshTokenAndEpoch(t *testing.T) {
	gdb := scratchDatabase(t)
	admin := bootstrapAdmin(t, gdb)
	gdb.Create(&models.RefreshToken{Jti: "mailbox-session", AccountID: 7, SessionID: "mailbox-session", Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)})
	gdb.Create(&models.RefreshToken{Jti: "spent", AccountID: 7, SessionID: "mailbox-session", Status: models.RefreshTokenRotated, ExpiresAt: time.Now().Add(time.Hour)})

	status, out, errOut := run("revoke-sessions")
	if status != exitOK {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}
	var revoked bootstrap.SessionsRevoked
	if err := json.Unmarshal([]byte(out), &revoked); err != nil {
		t.Fatalf("output = %q", out)
	}
	if revoked.RefreshTokens != 2 || revoked.Accounts != 1 {
		t.Errorf("revoked = %+v, want 2 refresh tokens and 1 account", revoked)
	}
	var live int64
	gdb.Model(&models.RefreshToken{}).Where("status = ?", models.RefreshTokenActive).Count(&live)
	if live != 0 {
		t.Errorf("%d refresh tokens still active", live)
	}
	var after models.Mailbox
	gdb.First(&after, admin.ID)
	if after.CredentialEpoch != admin.CredentialEpoch+1 {
		t.Errorf("epoch %d, want %d", after.CredentialEpoch, admin.CredentialEpoch+1)
	}
}

func TestDNSRecords_DerivesDKIMFromTheStoredKey(t *testing.T) {
	gdb := scratchDatabase(t)
	masterKey := "a-master-key-long-enough-for-config"
	t.Setenv("MASTER_KEY", masterKey)
	migrate(t)
	privPEM, pubPEM, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := models.EncryptDKIMPrivateKey(privPEM, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	gdb.Create(&models.Domain{Name: "signed.example", DKIMSelector: "mail", DKIMPrivateKey: stored})
	gdb.Create(&models.Domain{Name: "unsigned.example"})

	status, out, errOut := run("dns-records", "--mail-host", "mail.signed.example", "--ip", "203.0.113.5")
	if status != exitOK {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}
	var got struct {
		Domains []struct {
			Domain  string `json:"domain"`
			Problem string `json:"problem"`
			Records []struct{ Type, Name, Value string }
		} `json:"domains"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Domains) != 2 {
		t.Fatalf("output = %q", out)
	}
	want, _ := dkim.RecordValue(pubPEM)
	signed := got.Domains[0]
	found := map[string]string{}
	for _, r := range signed.Records {
		found[r.Type+" "+r.Name] = r.Value
	}
	if found["TXT mail._domainkey.signed.example"] != want {
		t.Errorf("DKIM record = %q, want the key's public half", found["TXT mail._domainkey.signed.example"])
	}
	if found["MX signed.example"] != "mail.signed.example" || found["A mail.signed.example"] != "203.0.113.5" || found["PTR 203.0.113.5"] != "mail.signed.example" {
		t.Errorf("records = %v", found)
	}
	if signed.Problem != "" {
		t.Errorf("signed domain reports a problem: %s", signed.Problem)
	}
	if got.Domains[1].Problem == "" {
		t.Error("a domain with no DKIM key says nothing about it")
	}

	if status, _, _ := run("dns-records"); status != exitUsage {
		t.Errorf("no --mail-host: status %d", status)
	}
}
