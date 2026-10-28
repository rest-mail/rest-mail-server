package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/examplepairs"
)

// The recovery email pairs that need a registered method, a pending or spendable
// link, or a known password run the real router on a scratch database holding
// exactly that state, and check what the request changed as well as the answer
// (#1237). They skip, and prove nothing, when no database is reachable.

const recoveryExampleAddress = "backup@external.test"

func recoveryExamplePairs(t *testing.T) map[string]map[string]oapifly.ExamplePair {
	t.Helper()
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	return pairs
}

func newRecoveryExampleFixture(t *testing.T) *credentialFixture {
	t.Helper()
	return newCredentialFixtureWith(t, &config.Config{Environment: "test", RecoveryEmailBaseURL: "https://mail.example.test"})
}

// sendRecoveryExample sends the pair's own request body, signed in when session is set.
func sendRecoveryExample(f *credentialFixture, method, path, session string, pair oapifly.ExamplePair) *httptest.ResponseRecorder {
	return f.do(method, path, session, string(pair.Request.Body))
}

// recoveryMethod stores account's recovery address, verified or still pending.
func recoveryMethod(f *credentialFixture, account uint, verified bool) {
	f.t.Helper()
	method := models.RecoveryEmail{AccountID: account, Address: recoveryExampleAddress}
	if verified {
		now := time.Now()
		method.VerifiedAt = &now
	}
	if err := f.db.Create(&method).Error; err != nil {
		f.t.Fatalf("seed recovery email: %v", err)
	}
}

// recoveryToken stores an unspent link of purpose for account, as the pair's
// token, so the request can spend it.
func recoveryToken(f *credentialFixture, account uint, token, purpose string) {
	f.t.Helper()
	sum := sha256.Sum256([]byte(token))
	if err := f.db.Create(&models.OneTimeToken{Purpose: purpose, TokenHash: hex.EncodeToString(sum[:]), AccountID: &account, ExpiresAt: time.Now().Add(15 * time.Minute)}).Error; err != nil {
		f.t.Fatalf("seed recovery token: %v", err)
	}
}

func recoveryTokenToken(t *testing.T, pair oapifly.ExamplePair) string {
	t.Helper()
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(pair.Request.Body, &body); err != nil || body.Token == "" {
		t.Fatalf("pair has no token: %s", pair.Request.Body)
	}
	return body.Token
}

func unspentRecoveryTokens(f *credentialFixture, account uint, purpose string) int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Model(&models.OneTimeToken{}).Where("account_id = ? AND purpose = ? AND used_at IS NULL", account, purpose).Count(&n).Error; err != nil {
		f.t.Fatal(err)
	}
	return n
}

func queuedTo(f *credentialFixture, recipient string) []models.OutboundQueue {
	f.t.Helper()
	var rows []models.OutboundQueue
	if err := f.db.Where("recipient = ?", recipient).Find(&rows).Error; err != nil {
		f.t.Fatal(err)
	}
	return rows
}

// signInWith sets the fixture mailbox's password and signs in with it again, so
// the session the request carries postdates the password it checks.
func signInWith(f *credentialFixture, password string) string {
	f.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := repositories.NewCredentialRepository(f.db).SetPassword(f.mailboxID, hash, nil, time.Now()); err != nil {
		f.t.Fatalf("set the known password: %v", err)
	}
	rr := f.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, f.address, password))
	if rr.Code != http.StatusOK {
		f.t.Fatalf("sign in with the known password: %d %s", rr.Code, rr.Body.String())
	}
	var login struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	decodeInto(f.t, rr, &login)
	return "Bearer " + login.Data.AccessToken
}

func TestRecoveryEmailSetupExamples(t *testing.T) {
	pairs := recoveryExamplePairs(t)

	t.Run("registerRecoveryEmail/success", func(t *testing.T) {
		examplepairs.Proven(t, "registerRecoveryEmail", "success")
		pair := pairs["registerRecoveryEmail"]["success"]
		f := newRecoveryExampleFixture(t)
		var body struct {
			CurrentPassword string `json:"current_password"`
		}
		if err := json.Unmarshal(pair.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		session := signInWith(f, body.CurrentPassword) // known_password: the pair's password is the account's
		rr := sendRecoveryExample(f, http.MethodPost, "/api/v1/auth/recovery-email", session, pair)
		assertRequirementResponse(t, pair, rr)
		var method models.RecoveryEmail
		if err := f.db.First(&method, "account_id = ?", f.mailboxID).Error; err != nil {
			t.Fatalf("no recovery email registered: %v", err)
		}
		if method.Address != recoveryExampleAddress || method.VerifiedAt != nil {
			t.Fatalf("registered %+v, want %s pending", method, recoveryExampleAddress)
		}
		if n := unspentRecoveryTokens(f, f.mailboxID, models.OneTimeTokenVerifyRecoveryEmail); n != 1 {
			t.Fatalf("%d verification links issued, want 1", n)
		}
		if q := queuedTo(f, recoveryExampleAddress); len(q) != 1 || !strings.Contains(q[0].RawMessage, "mode=verify#") {
			t.Fatalf("verification link not queued to %s: %+v", recoveryExampleAddress, q)
		}
	})

	t.Run("registerRecoveryEmail/invalid_password", func(t *testing.T) {
		examplepairs.Proven(t, "registerRecoveryEmail", "invalid_password")
		pair := pairs["registerRecoveryEmail"]["invalid_password"]
		f := newRecoveryExampleFixture(t) // known_password: the account's is credentialTestPassword
		if strings.Contains(string(pair.Request.Body), credentialTestPassword) {
			t.Fatal("pair sends the account's password; it must send a different one")
		}
		rr := sendRecoveryExample(f, http.MethodPost, "/api/v1/auth/recovery-email", f.session, pair)
		assertRequirementResponse(t, pair, rr)
		var n int64
		if err := f.db.Model(&models.RecoveryEmail{}).Where("account_id = ?", f.mailboxID).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("refused registration stored %d methods (%v)", n, err)
		}
		if n := unspentRecoveryTokens(f, f.mailboxID, models.OneTimeTokenVerifyRecoveryEmail); n != 0 {
			t.Fatalf("refused registration issued %d links", n)
		}
		if q := queuedTo(f, recoveryExampleAddress); len(q) != 0 {
			t.Fatalf("refused registration queued mail: %+v", q)
		}
	})

	t.Run("verifyRecoveryEmail/success", func(t *testing.T) {
		examplepairs.Proven(t, "verifyRecoveryEmail", "success")
		pair := pairs["verifyRecoveryEmail"]["success"]
		f := newRecoveryExampleFixture(t)
		recoveryMethod(f, f.mailboxID, false) // pending_recovery_email
		recoveryToken(f, f.mailboxID, recoveryTokenToken(t, pair), models.OneTimeTokenVerifyRecoveryEmail)
		rr := sendRecoveryExample(f, http.MethodPost, "/api/v1/auth/recovery-email/verify", "", pair)
		assertRequirementResponse(t, pair, rr)
		var method models.RecoveryEmail
		if err := f.db.First(&method, "account_id = ?", f.mailboxID).Error; err != nil || method.VerifiedAt == nil {
			t.Fatalf("recovery email not verified: %+v (%v)", method, err)
		}
		if n := unspentRecoveryTokens(f, f.mailboxID, models.OneTimeTokenVerifyRecoveryEmail); n != 0 {
			t.Fatalf("verification link still unspent (%d)", n)
		}
	})

	t.Run("redeemRecoveryEmail/success", func(t *testing.T) {
		examplepairs.Proven(t, "redeemRecoveryEmail", "success")
		pair := pairs["redeemRecoveryEmail"]["success"]
		f := newRecoveryExampleFixture(t)
		recoveryMethod(f, f.mailboxID, true) // recovery_email_token: a verified method and its link
		recoveryToken(f, f.mailboxID, recoveryTokenToken(t, pair), models.OneTimeTokenRecoverEmail)
		var body struct {
			NewPassword string `json:"new_password"`
		}
		if err := json.Unmarshal(pair.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		rr := sendRecoveryExample(f, http.MethodPost, "/api/v1/auth/recover/email/redeem", "", pair)
		assertRequirementResponse(t, pair, rr)
		if n := unspentRecoveryTokens(f, f.mailboxID, models.OneTimeTokenRecoverEmail); n != 0 {
			t.Fatalf("recovery link still unspent (%d)", n)
		}
		creds := repositories.NewCredentialRepository(f.db)
		if cred, err := creds.MatchPassword(f.mailboxID, body.NewPassword, time.Now()); err != nil || cred == nil {
			t.Fatalf("new password not set: %v", err)
		}
		if cred, err := creds.MatchPassword(f.mailboxID, credentialTestPassword, time.Now()); err != nil || cred != nil {
			t.Fatalf("old password still accepted: %v", err)
		}
		var method models.RecoveryEmail
		if err := f.db.First(&method, "account_id = ?", f.mailboxID).Error; err != nil || method.FrozenUntil == nil || !method.FrozenUntil.After(time.Now()) {
			t.Fatalf("recovery email not frozen after recovery: %+v (%v)", method, err)
		}
		if q := queuedTo(f, f.address); len(q) != 1 {
			t.Fatalf("password change notice not queued to %s: %+v", f.address, q)
		}
	})

	t.Run("requestRecoveryEmail/success", func(t *testing.T) {
		examplepairs.Proven(t, "requestRecoveryEmail", "success")
		pair := pairs["requestRecoveryEmail"]["success"]
		f := newRecoveryExampleFixture(t)
		var body struct {
			Email string `json:"email"`
		}
		if err := json.Unmarshal(pair.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		local, domain, ok := strings.Cut(body.Email, "@")
		if !ok || domain != "example.test" {
			t.Fatalf("pair asks for %q, outside the fixture domain", body.Email)
		}
		var hosted models.Domain
		if err := f.db.First(&hosted, "name = ?", domain).Error; err != nil {
			t.Fatal(err)
		}
		account := f.seedMailbox(hosted.ID, local)
		recoveryMethod(f, account, true) // verified_recovery_email
		rr := sendRecoveryExample(f, http.MethodPost, "/api/v1/auth/recover/email", "", pair)
		assertRequirementResponse(t, pair, rr)
		// Unknown accounts get the same answer, so only the link proves this one was sent.
		if n := unspentRecoveryTokens(f, account, models.OneTimeTokenRecoverEmail); n != 1 {
			t.Fatalf("%d recovery links issued, want 1", n)
		}
		if q := queuedTo(f, recoveryExampleAddress); len(q) != 1 || !strings.Contains(q[0].RawMessage, "mode=recover#") {
			t.Fatalf("recovery link not queued to %s: %+v", recoveryExampleAddress, q)
		}
	})

	t.Run("getRecoveryEmail/verified", func(t *testing.T) {
		examplepairs.Proven(t, "getRecoveryEmail", "verified")
		pair := pairs["getRecoveryEmail"]["verified"]
		f := newRecoveryExampleFixture(t)
		recoveryMethod(f, f.mailboxID, true) // verified_recovery_email
		rr := f.do(http.MethodGet, "/api/v1/auth/recovery-email", f.session, "")
		assertRequirementResponse(t, pair, rr)
	})
}
