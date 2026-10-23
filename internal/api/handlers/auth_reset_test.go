package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
)

// A server is installed with a bootstrap password, set up by whoever installed
// it, and handed over with a reset token: the owner chooses their own password,
// and a reset leaves nothing the previous holder knew working.

const ownerPassword = "a passphrase only the owner chose"

// newInstalledServer is a fresh install — its bootstrap admin, not signed in
// yet — with the one-time token table to hand it over with.
func newInstalledServer(t *testing.T) *firstSignIn {
	t.Helper()
	if err := openLoginTestDB(t).AutoMigrate(&models.OneTimeToken{}, &models.RefreshToken{}); err != nil {
		t.Skipf("reset DB test skipped: migrate failed (%v)", err)
	}
	f := newFirstSignIn(t)
	if err := f.db.Exec("TRUNCATE " + models.OneTimeToken{}.TableName() + " RESTART IDENTITY").Error; err != nil {
		t.Fatalf("empty one-time tokens: %v", err)
	}
	return f
}

// signInFirstTime walks the first sign-in as whoever installed the server
// would: the bootstrap password, then their own, then an authenticator. It
// returns the authenticator's secret and a session that reaches the admin API.
func (f *firstSignIn) signInFirstTime(t *testing.T, password string) (string, nativeSession) {
	t.Helper()
	_, boot := f.login(bootstrapPassword, "")
	if rr := f.changePassword(boot.AccessToken, bootstrapPassword, password); rr.Code != http.StatusOK {
		t.Fatalf("change password = %d %s", rr.Code, rr.Body)
	}
	rr, s := f.login(password, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("login with the chosen password = %d %s", rr.Code, rr.Body)
	}
	secret := f.enrollTOTP(t, s.AccessToken)
	rr, done := f.login(password, totpNow(t, secret))
	if rr.Code != http.StatusOK || f.adminAPI(done.AccessToken).Code != http.StatusTeapot {
		t.Fatalf("first sign-in did not reach the admin API: login = %d %s", rr.Code, rr.Body)
	}
	return secret, done
}

func post(handler http.HandlerFunc, body map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func errorCodeOf(rr *httptest.ResponseRecorder) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	return env.Error.Code
}

func mintReset(t *testing.T, f *firstSignIn) string {
	t.Helper()
	token, _, err := bootstrap.MintResetToken(f.db, f.name, 0)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// An agent set the server up and holds the admin's password, authenticator and
// sessions. It hands the owner a reset link; redeeming it leaves none of that
// working.
func TestReset_HandsTheServerOverAndLocksOutWhoeverSetItUp(t *testing.T) {
	f := newInstalledServer(t)
	const agentPassword = "the agent's own password"
	agentSecret, agent := f.signInFirstTime(t, agentPassword)

	rr := post(f.auth.Reset, map[string]string{"token": mintReset(t, f), "password": ownerPassword, "client": "native"})
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
	if rr := f.call(agent.AccessToken, "/api/v1/auth/2fa/enroll", twoFactorRoutes(f.twofa), nil); rr.Code != http.StatusUnauthorized {
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

func TestReset_RefusesBadTokensAndKeepsTheGoodOneForARetry(t *testing.T) {
	f := newInstalledServer(t)
	f.signInFirstTime(t, chosenPassword)
	token := mintReset(t, f)

	cases := []struct {
		name string
		body map[string]string
		want int
		code string
	}{
		{"unknown token", map[string]string{"token": "made-up", "password": ownerPassword}, 401, "invalid_token"},
		{"short password", map[string]string{"token": token, "password": "short"}, 422, "validation_failed"},
	}
	for _, c := range cases {
		if rr := post(f.auth.Reset, c.body); rr.Code != c.want || errorCodeOf(rr) != c.code {
			t.Errorf("%s: = %d %s, want %d %s", c.name, rr.Code, rr.Body, c.want, c.code)
		}
	}
	// None of that spent the token.
	if rr := post(f.auth.Reset, map[string]string{"token": token, "password": ownerPassword}); rr.Code != http.StatusOK {
		t.Fatalf("reset after refused attempts = %d %s", rr.Code, rr.Body)
	}
	// Spent now.
	rr := post(f.auth.Reset, map[string]string{"token": token, "password": "yet another passphrase"})
	if rr.Code != http.StatusUnauthorized || errorCodeOf(rr) != "invalid_token" {
		t.Errorf("reused token = %d %s, want 401 invalid_token", rr.Code, rr.Body)
	}
}
