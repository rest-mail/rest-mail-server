package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// A server claimed with a token minted by the operator command, and handed over
// with another: the owner chooses their own password at each step, and a
// reset leaves nothing the previous holder knew working.

// newUnclaimedServer is a fresh install with no admin, inside one rolled-back
// transaction (see newFirstSignIn for why).
func newUnclaimedServer(t *testing.T) *firstSignIn {
	t.Helper()
	base := openLoginTestDB(t)
	if err := base.AutoMigrate(&models.AdminUser{}, &models.Role{}, &models.Capability{},
		&models.UserRole{}, &models.RoleCapability{}, &models.TwoFactor{},
		&models.TwoFactorRecoveryCode{}, &models.OneTimeToken{}); err != nil {
		t.Skipf("claim DB test skipped: migrate failed (%v)", err)
	}
	gdb := base.Begin()
	t.Cleanup(func() { gdb.Rollback() })
	tables := strings.Join([]string{
		models.UserRole{}.TableName(), models.RoleCapability{}.TableName(),
		models.AdminUser{}.TableName(), models.Role{}.TableName(), models.Capability{}.TableName(),
		models.OneTimeToken{}.TableName(),
	}, ", ")
	if err := gdb.Exec("TRUNCATE " + tables + " RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatalf("empty tables: %v", err)
	}
	if err := bootstrap.EnsureRBAC(gdb); err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService("claim-secret", 15*time.Minute, 7*24*time.Hour)
	return &firstSignIn{
		db:    gdb,
		jwt:   jwt,
		auth:  NewAuthHandler(gdb, jwt, firstSignInKey),
		twofa: NewTwoFactorHandler(gdb, firstSignInKey, true),
	}
}

func post(handler http.HandlerFunc, body map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func claimErrorCode(rr *httptest.ResponseRecorder) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	return env.Error.Code
}

func mintSetup(t *testing.T, gdb *gorm.DB) string {
	t.Helper()
	token, _, err := bootstrap.MintSetupToken(gdb, 0)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestClaim_TheOwnerChoosesTheirPasswordAndStillOwesAnAuthenticator(t *testing.T) {
	f := newUnclaimedServer(t)
	token := mintSetup(t, f.db)

	rr := post(f.auth.Claim, map[string]string{
		"token": token, "username": "owner", "password": ownerPassword, "client": "native",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("claim = %d %s", rr.Code, rr.Body)
	}
	s := sessionFrom(rr)
	if want := []string{"two_factor"}; !reflect.DeepEqual(s.SetupRequired, want) {
		t.Errorf("setup_required = %v, want %v: no password to replace, but 2FA owed", s.SetupRequired, want)
	}
	if got := f.adminAPI(s.AccessToken); got.Code != http.StatusForbidden {
		t.Errorf("admin API straight after claiming = %d, want 403 until 2FA is enrolled", got.Code)
	}

	// The session finishes setup like any other, and the chosen password works.
	f.name = "owner"
	secret := f.enrollTOTP(t, s.AccessToken)
	rr, done := f.login(ownerPassword, totpNow(t, secret))
	if rr.Code != http.StatusOK || len(done.SetupRequired) != 0 {
		t.Fatalf("login after setup = %d, setup_required %v", rr.Code, done.SetupRequired)
	}
	if got := f.adminAPI(done.AccessToken); got.Code != http.StatusTeapot {
		t.Errorf("admin API after setup = %d, want it reached", got.Code)
	}
}

func TestClaim_RefusesBadTokensAndKeepsTheGoodOneForARetry(t *testing.T) {
	f := newUnclaimedServer(t)
	token := mintSetup(t, f.db)

	cases := []struct {
		name string
		body map[string]string
		want int
		code string
	}{
		{"unknown token", map[string]string{"token": "made-up", "username": "x", "password": ownerPassword}, 401, "invalid_token"},
		{"short password", map[string]string{"token": token, "username": "x", "password": "short"}, 422, "validation_failed"},
		{"no username", map[string]string{"token": token, "username": " ", "password": ownerPassword}, 422, "validation_failed"},
	}
	for _, c := range cases {
		rr := post(f.auth.Claim, c.body)
		if rr.Code != c.want {
			t.Errorf("%s: = %d %s, want %d", c.name, rr.Code, rr.Body, c.want)
		}
	}
	// None of that spent the token.
	if rr := post(f.auth.Claim, map[string]string{"token": token, "username": "owner", "password": ownerPassword}); rr.Code != http.StatusOK {
		t.Fatalf("claim after refused attempts = %d %s", rr.Code, rr.Body)
	}
	// Spent now.
	rr := post(f.auth.Claim, map[string]string{"token": token, "username": "late", "password": ownerPassword})
	if rr.Code != http.StatusUnauthorized || claimErrorCode(rr) != "invalid_token" {
		t.Errorf("reused token = %d %s, want 401 invalid_token", rr.Code, rr.Body)
	}
}

// An agent set the server up and holds the admin's password, authenticator and
// sessions. It hands the owner a reset link; redeeming it leaves none of that
// working.
func TestReset_HandsTheServerOverAndLocksOutWhoeverSetItUp(t *testing.T) {
	f := newUnclaimedServer(t)
	const agentPassword = "the agent's own password"
	rr := post(f.auth.Claim, map[string]string{
		"token": mintSetup(t, f.db), "username": "admin", "password": agentPassword, "client": "native",
	})
	f.name = "admin"
	agentSecret := f.enrollTOTP(t, sessionFrom(rr).AccessToken)
	_, agent := f.login(agentPassword, totpNow(t, agentSecret))
	if f.adminAPI(agent.AccessToken).Code != http.StatusTeapot {
		t.Fatal("the agent's session should work before the hand-over")
	}

	reset, _, err := bootstrap.MintResetToken(f.db, "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	rr = post(f.auth.Reset, map[string]string{"token": reset, "password": ownerPassword, "client": "native"})
	if rr.Code != http.StatusOK {
		t.Fatalf("reset = %d %s", rr.Code, rr.Body)
	}
	owner := sessionFrom(rr)
	if want := []string{"two_factor"}; !reflect.DeepEqual(owner.SetupRequired, want) {
		t.Errorf("setup_required = %v, want %v: the owner enrolls their own authenticator", owner.SetupRequired, want)
	}

	// Everything the agent knew is worthless — including the access token it
	// was holding, which has not expired. The reset wiped the authenticator; if
	// that token still worked, the agent could enroll its own before the owner
	// does and own the account again.
	if rr := f.call(agent.AccessToken, http.HandlerFunc(f.twofa.Enroll), nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("agent's unexpired access token enrolling an authenticator = %d %s, want 401", rr.Code, rr.Body)
	}
	if rr := f.adminAPI(agent.AccessToken); rr.Code != http.StatusUnauthorized {
		t.Errorf("agent's unexpired access token on the admin API = %d, want 401", rr.Code)
	}
	if rr, _ := f.login(agentPassword, totpNow(t, agentSecret)); rr.Code != http.StatusUnauthorized {
		t.Errorf("agent's password after hand-over = %d, want 401", rr.Code)
	}
	if rr, _ := f.refresh(agent.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Errorf("agent's session after hand-over = %d, want 401", rr.Code)
	}

	// The owner's account works once they enroll their own authenticator — and
	// the agent's codes are not it.
	ownerSecret := f.enrollTOTP(t, owner.AccessToken)
	if ownerSecret == agentSecret {
		t.Fatal("the owner was given the agent's authenticator secret")
	}
	if rr, _ := f.login(ownerPassword, totpNow(t, agentSecret)); rr.Code != http.StatusUnauthorized {
		t.Errorf("owner's password with the agent's code = %d, want 401", rr.Code)
	}
	rr, done := f.login(ownerPassword, totpNow(t, ownerSecret))
	if rr.Code != http.StatusOK || f.adminAPI(done.AccessToken).Code != http.StatusTeapot {
		t.Errorf("owner after hand-over = %d, cannot reach the admin API", rr.Code)
	}
}

func TestClaim_RefusedWhereTwoFactorIsOff(t *testing.T) {
	f := newUnclaimedServer(t)
	f.auth.SetTwoFactorAvailable(false)
	rr := post(f.auth.Claim, map[string]string{"token": mintSetup(t, f.db), "username": "owner", "password": ownerPassword})
	if rr.Code == http.StatusOK {
		t.Fatal("claimed a server whose first admin could never finish setting up")
	}
}

const ownerPassword = "a passphrase only the owner chose"
