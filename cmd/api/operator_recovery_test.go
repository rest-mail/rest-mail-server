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
	"gorm.io/gorm"
)

const deployedPassword = "the-deployments-own-password"

// bootstrapAdmin makes the admin a fresh install has, signed in, with an
// authenticator, as an operator would find it.
func bootstrapAdmin(t *testing.T, gdb *gorm.DB) models.AdminUser {
	t.Helper()
	migrate(t)
	if _, err := bootstrap.EnsureFirstAdmin(gdb, "admin", deployedPassword, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	var admin models.AdminUser
	if err := gdb.Where("username = ?", "admin").First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	gdb.Create(&models.TwoFactor{UserType: models.TwoFactorUserTypeAdmin, SubjectID: admin.ID, EncryptedSecret: "x", Confirmed: true})
	gdb.Create(&models.RefreshToken{Jti: "admin-session", UserType: "admin", SubjectID: admin.ID, Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)})
	return admin
}

// migrate brings the scratch database to the schema the server would.
func migrate(t *testing.T) {
	t.Helper()
	var errOut strings.Builder
	if _, _, status := openDatabase(&errOut); status != exitOK {
		t.Fatalf("migrate: %s", errOut.String())
	}
}

func resetWith(args []string, password string) (int, string, string) {
	var out, errOut strings.Builder
	status := resetPassword(args, strings.NewReader(password+"\n"), &out, &errOut)
	return status, out.String(), errOut.String()
}

func TestResetPassword_PutsTheAdminBackInSetup(t *testing.T) {
	gdb := scratchDatabase(t)
	before := bootstrapAdmin(t, gdb)

	status, out, errOut := resetWith([]string{"--username", "admin"}, "a-freshly-generated-password")
	if status != exitOK {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}
	var result struct {
		Username      string   `json:"username"`
		SetupRequired []string `json:"setup_required"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Username != "admin" || len(result.SetupRequired) != 2 {
		t.Fatalf("output = %q", out)
	}

	var after models.AdminUser
	gdb.First(&after, before.ID)
	if auth.CheckPassword("a-freshly-generated-password", after.PasswordHash) != nil {
		t.Error("the new password does not sign in")
	}
	if !after.PasswordChangeRequired || !after.TwoFactorRequired {
		t.Error("the account is not back in setup")
	}
	if after.CredentialEpoch <= before.CredentialEpoch {
		t.Error("the epoch was not bumped, so access tokens survive")
	}
	var live int64
	gdb.Model(&models.RefreshToken{}).Where("subject_id = ? AND status = ?", before.ID, models.RefreshTokenActive).Count(&live)
	if live != 0 {
		t.Error("a refresh token survived, so the old session can re-mint")
	}
	var factors int64
	gdb.Model(&models.TwoFactor{}).Where("subject_id = ?", before.ID).Count(&factors)
	if factors != 0 {
		t.Error("the old authenticator survived")
	}

	// A restart with the deployment's password still in the Secret must not
	// take back the password someone was just handed.
	if outcome, err := bootstrap.EnsureFirstAdmin(gdb, "admin", deployedPassword, true, time.Now()); err != nil || outcome != bootstrap.FirstAdminUnchanged {
		t.Fatalf("restart: outcome %v, err %v", outcome, err)
	}
	gdb.First(&after, before.ID)
	if auth.CheckPassword("a-freshly-generated-password", after.PasswordHash) != nil {
		t.Error("a restart replaced the reset password with the deployment's")
	}
}

func TestResetPassword_Refusals(t *testing.T) {
	gdb := scratchDatabase(t)
	bootstrapAdmin(t, gdb)

	if status, _, errOut := resetWith([]string{"--username", "nobody"}, "a-freshly-generated-password"); status != exitNoSuchAdmin || errorCodeIn(t, errOut) != "no_such_admin" {
		t.Errorf("nobody: status %d, %s", status, errOut)
	}
	if status, _, errOut := resetWith([]string{"--username", "admin"}, "short"); status != exitUsage || errorCodeIn(t, errOut) != "password_too_short" {
		t.Errorf("short: status %d, %s", status, errOut)
	}
	if status, _, errOut := resetWith(nil, "a-freshly-generated-password"); status != exitUsage {
		t.Errorf("no username: status %d, %s", status, errOut)
	}
}

func TestRevokeSessions_EndsEveryRefreshTokenAndAdminEpoch(t *testing.T) {
	gdb := scratchDatabase(t)
	admin := bootstrapAdmin(t, gdb)
	gdb.Create(&models.RefreshToken{Jti: "mailbox-session", UserType: "mailbox", SubjectID: 7, Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)})
	gdb.Create(&models.RefreshToken{Jti: "spent", UserType: "mailbox", SubjectID: 7, Status: models.RefreshTokenRotated, ExpiresAt: time.Now().Add(time.Hour)})

	status, out, errOut := run("revoke-sessions")
	if status != exitOK {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}
	var revoked bootstrap.SessionsRevoked
	if err := json.Unmarshal([]byte(out), &revoked); err != nil {
		t.Fatalf("output = %q", out)
	}
	if revoked.RefreshTokens != 2 || revoked.Admins != 1 {
		t.Errorf("revoked = %+v, want 2 refresh tokens (admin's and mailbox's) and 1 admin", revoked)
	}
	var live int64
	gdb.Model(&models.RefreshToken{}).Where("status = ?", models.RefreshTokenActive).Count(&live)
	if live != 0 {
		t.Errorf("%d refresh tokens still active", live)
	}
	var after models.AdminUser
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
