package main

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
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
	if os.Getenv("RESTMAIL_DNS_SUBPROCESS") == "1" {
		os.Exit(runOperator([]string{"dns-records", "--mail-host", "mail.signed.example", "--ip", "203.0.113.5"}, os.Stdout, os.Stderr))
	}
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
	// Insert out of order to retain the command's alphabetical ordering check.
	if err := gdb.Create(&models.Domain{Name: "unsigned.example"}).Error; err != nil {
		t.Fatal("seed unsigned domain failed")
	}
	if err := gdb.Create(&models.Domain{Name: "signed.example", DKIMSelector: "mail", DKIMPrivateKey: stored}).Error; err != nil {
		t.Fatal("seed encrypted domain failed")
	}
	// An existing schema with missing RBAC seeds must remain unseeded.
	if err := gdb.Exec("TRUNCATE roles, capabilities, role_capabilities").Error; err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []any{
		&models.RefreshToken{Jti: "dns-observation-session", AccountID: 7, SessionID: "dns-observation-session", Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)},
		&models.OneTimeToken{Purpose: models.OneTimeTokenResetAdmin, TokenHash: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Hour)},
	} {
		if err := gdb.Create(fixture).Error; err != nil {
			t.Fatal("seed observation sentinel failed")
		}
	}
	before := operatorDatabaseSnapshot(t, gdb)
	setOperatorDatabaseReadOnly(t, gdb)

	var out, errOut string
	for _, debug := range []string{"", "1"} {
		wrongKey := "a-different-master-key-for-refusal"
		for _, configuredKey := range []string{wrongKey, masterKey} {
			t.Setenv("RESTMAIL_OPERATOR_DEBUG", debug)
			cmd := exec.Command(os.Args[0], "-test.run=^TestDNSRecords_DerivesDKIMFromTheStoredKey$")
			cmd.Env = append(os.Environ(), "RESTMAIL_DNS_SUBPROCESS=1", "MASTER_KEY="+configuredKey)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			out, errOut = stdout.String(), stderr.String()
			for _, secret := range []string{privPEM, stored, masterKey, wrongKey, "-----BEGIN RSA PRIVATE KEY-----", "-----BEGIN PRIVATE KEY-----"} {
				if strings.Contains(out+errOut, secret) {
					t.Fatal("operator output contains confidential key material")
				}
			}
			if err != nil {
				t.Fatalf("read-only operator subprocess failed: %v", err)
			}
			if !json.Valid([]byte(out)) {
				t.Fatal("stdout is not just the JSON result")
			}
			if debug == "" && errOut != "" {
				t.Fatal("quiet operator wrote to stderr")
			}
			if debug != "" && !strings.Contains(errOut, "SELECT") {
				t.Fatal("debug SQL logging was lost")
			}
			if configuredKey == wrongKey {
				if !strings.Contains(out, `"problem":"DKIM key unreadable:`) || strings.Contains(out, "mail._domainkey.signed.example") {
					t.Fatal("unreadable encrypted key did not retain its problem without publishing DKIM")
				}
			}
		}
	}
	if after := operatorDatabaseSnapshot(t, gdb); before != after {
		t.Fatal("dns-records changed schema or rows, including RBAC seeds")
	}
	var got struct {
		Domains []struct {
			Domain  string `json:"domain"`
			Problem string `json:"problem"`
			Records []struct{ Type, Name, Value string }
		} `json:"domains"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Domains) != 2 {
		t.Fatal("output does not contain the expected two domains")
	}
	want, err := dkim.RecordValue(pubPEM)
	if err != nil {
		t.Fatal("fixture public key is invalid")
	}
	signed := got.Domains[0]
	found := map[string]string{}
	for _, r := range signed.Records {
		found[r.Type+" "+r.Name] = r.Value
	}
	// Decode the actual DNS p= value and compare its public DER to the fixture.
	_, publicValue, ok := strings.Cut(found["TXT mail._domainkey.signed.example"], "p=")
	if !ok {
		t.Fatal("DKIM record has no public key")
	}
	publicValue = strings.TrimSpace(publicValue)
	der, err := base64.StdEncoding.DecodeString(publicValue)
	if err != nil {
		t.Fatal("DNS public key is not base64 DER")
	}
	if _, err := x509.ParsePKIXPublicKey(der); err != nil {
		t.Fatal("DNS public key is not PKIX DER")
	}
	block, _ := pem.Decode([]byte(pubPEM))
	if block == nil || !bytes.Equal(der, block.Bytes) {
		t.Fatal("DNS public DER does not match encrypted fixture")
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
