package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// The bootstrap admin's password was chosen by a deploy tool and sits in its
// state. These tests walk the first sign-in: until the admin has replaced that
// password AND enrolled a TOTP authenticator, the session reaches nothing in the
// admin API — not directly, not by refreshing.

const (
	bootstrapPassword = "deploy-time-password-0123456789"
	chosenPassword    = "a passphrase only the operator knows"
	firstSignInKey    = "first-sign-in-master-key-0123456789abcdef"
)

// firstSignIn is one fresh install: a bootstrap admin, and the handlers and
// middleware the routes wire around them.
type firstSignIn struct {
	db    *gorm.DB
	jwt   *auth.JWTService
	auth  *AuthHandler
	twofa *TwoFactorHandler
	name  string
}

// newFirstSignIn gives a fresh install inside one transaction that is rolled
// back at the end. "No admin exists" is a fact about the whole database, and
// go test runs packages in parallel against the same Postgres: a plain TRUNCATE
// would wipe another package's admins mid-test. Inside a transaction the emptied
// tables are invisible to everyone else, and the TRUNCATE's lock makes any other
// test touching them wait for the rollback instead.
func newFirstSignIn(t *testing.T) *firstSignIn {
	t.Helper()
	base := openLoginTestDB(t)
	if err := base.AutoMigrate(&models.AdminUser{}, &models.Role{}, &models.Capability{},
		&models.UserRole{}, &models.RoleCapability{}, &models.TwoFactor{}, &models.TwoFactorRecoveryCode{}); err != nil {
		t.Skipf("first sign-in DB test skipped: migrate failed (%v)", err)
	}
	gdb := base.Begin()
	t.Cleanup(func() { gdb.Rollback() })
	tables := strings.Join([]string{
		models.UserRole{}.TableName(), models.RoleCapability{}.TableName(),
		models.AdminUser{}.TableName(), models.Role{}.TableName(), models.Capability{}.TableName(),
	}, ", ")
	if err := gdb.Exec("TRUNCATE " + tables + " RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatalf("empty RBAC tables: %v", err)
	}

	if err := bootstrap.EnsureRBAC(gdb); err != nil {
		t.Fatal(err)
	}
	name := "boot-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if created, err := bootstrap.EnsureFirstAdmin(gdb, name, bootstrapPassword, true); err != nil || !created {
		t.Fatalf("bootstrap admin: created=%v err=%v", created, err)
	}
	jwt := auth.NewJWTService("first-sign-in-secret", 15*time.Minute, 7*24*time.Hour)
	return &firstSignIn{
		db:    gdb,
		jwt:   jwt,
		auth:  NewAuthHandler(gdb, jwt, firstSignInKey),
		twofa: NewTwoFactorHandler(gdb, firstSignInKey, true),
		name:  name,
	}
}

type nativeSession struct {
	AccessToken   string   `json:"access_token"`
	RefreshToken  string   `json:"refresh_token"`
	Capabilities  []string `json:"capabilities"`
	SetupRequired []string `json:"setup_required"`
}

// sessionFrom reads a session out of the {"data": ...} envelope.
func sessionFrom(rr *httptest.ResponseRecorder) nativeSession {
	var env struct {
		Data nativeSession `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	return env.Data
}

func (f *firstSignIn) login(password, totp string) (*httptest.ResponseRecorder, nativeSession) {
	body := map[string]string{"username": f.name, "password": password, "client": "native"}
	if totp != "" {
		body["totp_code"] = totp
	}
	rr := doLogin(f.auth, body)
	return rr, sessionFrom(rr)
}

func (f *firstSignIn) refresh(refreshToken string) (*httptest.ResponseRecorder, nativeSession) {
	b, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.auth.Refresh(rr, req)
	return rr, sessionFrom(rr)
}

// call runs handler behind JWT auth, as the routes do, with a JSON body.
func (f *firstSignIn) call(token string, handler http.Handler, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	middleware.JWTMiddleware(f.jwt)(handler).ServeHTTP(rr, req)
	return rr
}

var reached = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

// adminAPI is any admin route, including one that checks no capability.
func (f *firstSignIn) adminAPI(token string) *httptest.ResponseRecorder {
	return f.call(token, middleware.AdminOnly(reached), nil)
}

func (f *firstSignIn) changePassword(token, current, next string) *httptest.ResponseRecorder {
	return f.call(token, http.HandlerFunc(f.auth.ChangePassword),
		map[string]string{"current_password": current, "new_password": next})
}

// enrollTOTP runs enroll then confirm and returns the authenticator's secret.
func (f *firstSignIn) enrollTOTP(t *testing.T, token string) string {
	t.Helper()
	rr := f.call(token, http.HandlerFunc(f.twofa.Enroll), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("2fa enroll = %d %s", rr.Code, rr.Body)
	}
	var env struct {
		Data struct {
			Secret string `json:"secret"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	code, err := auth.GenerateTOTPCode(env.Data.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rr := f.call(token, http.HandlerFunc(f.twofa.Confirm), map[string]string{"code": code}); rr.Code != http.StatusNoContent {
		t.Fatalf("2fa confirm = %d %s", rr.Code, rr.Body)
	}
	return env.Data.Secret
}

func totpNow(t *testing.T, secret string) string {
	t.Helper()
	code, err := auth.GenerateTOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestFirstSignIn_CanOnlyFinishSetup(t *testing.T) {
	f := newFirstSignIn(t)

	rr, s := f.login(bootstrapPassword, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rr.Code, rr.Body)
	}
	if want := []string{"password", "two_factor"}; !reflect.DeepEqual(s.SetupRequired, want) {
		t.Errorf("setup_required = %v, want %v", s.SetupRequired, want)
	}
	if len(s.Capabilities) != 0 {
		t.Errorf("setup session was handed capabilities %v", s.Capabilities)
	}

	// The admin API refuses it — including a route that checks no capability.
	if got := f.adminAPI(s.AccessToken); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "setup_required") {
		t.Fatalf("admin API in setup = %d %s, want 403 setup_required", got.Code, got.Body)
	}

	// Refreshing is not a way out.
	rr, refreshed := f.refresh(s.RefreshToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh = %d %s", rr.Code, rr.Body)
	}
	if got := f.adminAPI(refreshed.AccessToken); got.Code != http.StatusForbidden {
		t.Fatalf("admin API after refresh = %d, want 403: the refresh escaped setup", got.Code)
	}
}

func TestFirstSignIn_PasswordAloneIsNotEnough(t *testing.T) {
	f := newFirstSignIn(t)
	_, s := f.login(bootstrapPassword, "")

	if rr := f.changePassword(s.AccessToken, bootstrapPassword, chosenPassword); rr.Code != http.StatusOK {
		t.Fatalf("change password = %d %s", rr.Code, rr.Body)
	}
	// Every session from before is over, and the deploy-time password no longer works.
	if rr, _ := f.refresh(s.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Errorf("old refresh token after change = %d, want 401", rr.Code)
	}
	if rr, _ := f.login(bootstrapPassword, ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("bootstrap password after change = %d, want 401", rr.Code)
	}

	// With a new password but no authenticator, the account is still in setup.
	rr, s2 := f.login(chosenPassword, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("login with new password = %d %s", rr.Code, rr.Body)
	}
	if want := []string{"two_factor"}; !reflect.DeepEqual(s2.SetupRequired, want) {
		t.Errorf("setup_required = %v, want %v", s2.SetupRequired, want)
	}
	if got := f.adminAPI(s2.AccessToken); got.Code != http.StatusForbidden {
		t.Errorf("admin API with password but no 2FA = %d, want 403", got.Code)
	}
}

func TestFirstSignIn_PasswordAndAuthenticatorUnlockTheAccount(t *testing.T) {
	f := newFirstSignIn(t)
	_, s := f.login(bootstrapPassword, "")

	// Either order works; authenticator first here, from the same setup session.
	secret := f.enrollTOTP(t, s.AccessToken)
	if rr := f.changePassword(s.AccessToken, bootstrapPassword, chosenPassword); rr.Code != http.StatusOK {
		t.Fatalf("change password = %d %s", rr.Code, rr.Body)
	}

	// Signing in now takes the new password and a code.
	if rr, _ := f.login(chosenPassword, ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("login without a code = %d, want 401", rr.Code)
	}
	rr, done := f.login(chosenPassword, totpNow(t, secret))
	if rr.Code != http.StatusOK {
		t.Fatalf("login with password and code = %d %s", rr.Code, rr.Body)
	}
	if len(done.SetupRequired) != 0 {
		t.Errorf("setup_required = %v after finishing setup", done.SetupRequired)
	}
	hasWildcard := false
	for _, c := range done.Capabilities {
		hasWildcard = hasWildcard || c == "*"
	}
	if !hasWildcard {
		t.Errorf("superadmin capabilities = %v, want *", done.Capabilities)
	}
	if got := f.adminAPI(done.AccessToken); got.Code != http.StatusTeapot {
		t.Errorf("admin API after setup = %d, want it reached", got.Code)
	}

	// And 2FA stays: a required authenticator cannot be switched off.
	if rr := f.call(done.AccessToken, http.HandlerFunc(f.twofa.Disable), map[string]string{"code": totpNow(t, secret)}); rr.Code != http.StatusForbidden {
		t.Errorf("disable required 2FA = %d %s, want 403", rr.Code, rr.Body)
	}
}

func TestChangePassword_RefusesBadRequests(t *testing.T) {
	f := newFirstSignIn(t)
	_, s := f.login(bootstrapPassword, "")

	cases := []struct {
		name, current, next string
		want                int
	}{
		// A stolen access token alone must not be enough to take the account.
		{"wrong current password", "not-the-password-at-all", "a-brand-new-passphrase", http.StatusUnauthorized},
		{"too short", bootstrapPassword, "short", http.StatusUnprocessableEntity},
		{"unchanged", bootstrapPassword, bootstrapPassword, http.StatusUnprocessableEntity},
		{"missing new", bootstrapPassword, "", http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		if got := f.changePassword(s.AccessToken, c.current, c.next); got.Code != c.want {
			t.Errorf("%s: = %d %s, want %d", c.name, got.Code, got.Body, c.want)
		}
	}

	// None of those changed anything: the bootstrap password still works and is still owed.
	if _, again := f.login(bootstrapPassword, ""); len(again.SetupRequired) == 0 || again.SetupRequired[0] != "password" {
		t.Errorf("a refused change cleared the password step: setup_required = %v", again.SetupRequired)
	}
}

func TestChangePassword_MailboxSessionsAreRefused(t *testing.T) {
	f := newFirstSignIn(t)
	pair, err := f.jwt.GenerateTokenPair(1, "someone@example.test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.changePassword(pair.AccessToken, "x", "y-long-enough-pass"); got.Code != http.StatusForbidden {
		t.Errorf("mailbox session = %d, want 403", got.Code)
	}
}
