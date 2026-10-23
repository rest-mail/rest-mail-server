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
	rmdb "github.com/restmail/restmail/internal/db"
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
	if err := base.AutoMigrate(append(accountModels, &models.Pipeline{})...); err != nil {
		t.Skipf("first sign-in DB test skipped: migrate failed (%v)", err)
	}
	// The bootstrap admin's grants are recorded in the audit log.
	if err := rmdb.MigrateAuditLog(base); err != nil {
		t.Skipf("first sign-in DB test skipped: audit log migrate failed (%v)", err)
	}
	gdb := base.Begin()
	t.Cleanup(func() { gdb.Rollback() })
	// Grants only: "an admin exists" is any grant at "*", and the roles and
	// capabilities are made again below.
	tables := strings.Join([]string{
		models.Grant{}.TableName(), models.GrantActivation{}.TableName(), models.RoleCapability{}.TableName(),
		models.Role{}.TableName(), models.Capability{}.TableName(),
	}, ", ")
	if err := gdb.Exec("TRUNCATE " + tables + " RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatalf("empty RBAC tables: %v", err)
	}

	if err := bootstrap.EnsureRBAC(gdb); err != nil {
		t.Fatal(err)
	}
	name := "admin@boot-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".test"
	if outcome, err := bootstrap.EnsureFirstAdmin(gdb, name, bootstrapPassword, true, time.Now()); err != nil || outcome != bootstrap.FirstAdminCreated {
		t.Fatalf("bootstrap admin: outcome=%v err=%v", outcome, err)
	}
	jwt := auth.NewJWTService("first-sign-in-secret", 15*time.Minute, 7*24*time.Hour)
	return &firstSignIn{
		db:    gdb,
		jwt:   jwt,
		auth:  NewAuthHandler(gdb, jwt, firstSignInKey, nil),
		twofa: NewTwoFactorHandler(gdb, firstSignInKey, true, nil),
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
	body := map[string]string{"email": f.name, "password": password, "client": "native"}
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

// call posts to path on handler behind JWT auth, as the routes do, with a JSON body.
func (f *firstSignIn) call(token, path string, handler http.Handler, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	// The chain the routes put every authenticated call through.
	middleware.JWTMiddleware(f.jwt)(
		middleware.RejectStaleSessions(SessionCurrent(f.db))(handler),
	).ServeHTTP(rr, req)
	return rr
}

var reached = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

// adminAPI is any route past setup: an admin route, the mail routes, or
// creating an account.
func (f *firstSignIn) adminAPI(token string) *httptest.ResponseRecorder {
	return f.call(token, "/", middleware.SetupDone(reached), nil)
}

func (f *firstSignIn) changePassword(token, current, next string) *httptest.ResponseRecorder {
	return f.call(token, "/api/v1/auth/password", passwordChangeRoutes(f.auth),
		map[string]string{"current_password": current, "new_password": next})
}

// enrollTOTP runs enroll then confirm and returns the authenticator's secret.
func (f *firstSignIn) enrollTOTP(t *testing.T, token string) string {
	t.Helper()
	rr := f.call(token, "/api/v1/auth/2fa/enroll", twoFactorRoutes(f.twofa), nil)
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
	if rr := f.call(token, "/api/v1/auth/2fa/confirm", twoFactorRoutes(f.twofa), map[string]string{"totp_code": code}); rr.Code != http.StatusNoContent {
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
	if want := []string{"password", "two_factor"}; !reflect.DeepEqual(refreshed.SetupRequired, want) {
		t.Errorf("refresh setup_required = %v, want %v", refreshed.SetupRequired, want)
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
	if rr, refreshed := f.refresh(s2.RefreshToken); rr.Code != http.StatusOK || !reflect.DeepEqual(refreshed.SetupRequired, []string{"two_factor"}) {
		t.Errorf("refresh after password = %d, setup_required %v, want two_factor", rr.Code, refreshed.SetupRequired)
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
	// The admin role's capabilities, day to day; superadmin's "*" only once it
	// is switched on (#346).
	hasWildcard, canBuild := false, false
	for _, c := range done.Capabilities {
		hasWildcard = hasWildcard || c == "*"
		canBuild = canBuild || c == "domains:write"
	}
	if hasWildcard || !canBuild {
		t.Errorf("capabilities = %v, want the admin role's, without *", done.Capabilities)
	}
	if got := f.adminAPI(done.AccessToken); got.Code != http.StatusTeapot {
		t.Errorf("admin API after setup = %d, want it reached", got.Code)
	}

	// And 2FA stays: a required authenticator cannot be switched off.
	if rr := f.call(done.AccessToken, "/api/v1/auth/2fa/disable", twoFactorRoutes(f.twofa), map[string]string{"totp_code": totpNow(t, secret)}); rr.Code != http.StatusForbidden {
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
		// 403, never 401: the session is still good (#392).
		{"wrong current password", "not-the-password-at-all", "a-brand-new-passphrase", http.StatusForbidden},
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

// A session whose account is gone changes nothing.
func TestChangePassword_NoSuchAccountIsRefused(t *testing.T) {
	f := newFirstSignIn(t)
	pair, err := f.jwt.GenerateSession(1<<30, "someone@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.changePassword(pair.AccessToken, "x", "y-long-enough-pass"); got.Code != http.StatusUnauthorized {
		t.Errorf("no such account = %d, want 401", got.Code)
	}
}

// expire moves the bootstrap password's deadline into the past, as 72 hours of
// nobody finishing the install would.
func (f *firstSignIn) expire(t *testing.T) {
	t.Helper()
	if err := f.db.Model(&models.Mailbox{}).Where("address = ?", f.name).
		Update("bootstrap_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
}

func TestFirstSignIn_ExpiredBootstrapPasswordIsRefused(t *testing.T) {
	f := newFirstSignIn(t)
	f.expire(t)

	rr, _ := f.login(bootstrapPassword, "")
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "bootstrap_expired") {
		t.Fatalf("expired bootstrap password = %d %s, want 401 bootstrap_expired", rr.Code, rr.Body)
	}
	// Only someone who knows the password learns why: a wrong one gets the
	// ordinary refusal, so the expiry says nothing to a guesser.
	rr, _ = f.login("not-the-bootstrap-password-at-all", "")
	if rr.Code != http.StatusUnauthorized || strings.Contains(rr.Body.String(), "bootstrap_expired") {
		t.Fatalf("wrong password after expiry = %d %s, want a plain 401", rr.Code, rr.Body)
	}
}

// A session opened before the deadline does not outlive it: not to finish the
// setup, and not by refreshing.
func TestFirstSignIn_ExpiryEndsSessionsAlreadyOpen(t *testing.T) {
	f := newFirstSignIn(t)
	_, s := f.login(bootstrapPassword, "")
	f.expire(t)

	if rr := f.changePassword(s.AccessToken, bootstrapPassword, chosenPassword); rr.Code != http.StatusUnauthorized {
		t.Errorf("setup after expiry = %d %s, want 401", rr.Code, rr.Body)
	}
	if rr, _ := f.refresh(s.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Errorf("refresh after expiry = %d %s, want 401", rr.Code, rr.Body)
	}
}

// The deadline is on the deploy-time password, not the account: once the admin
// has chosen its own, time no longer matters.
func TestFirstSignIn_ChoosingAPasswordEndsTheDeadline(t *testing.T) {
	f := newFirstSignIn(t)
	_, s := f.login(bootstrapPassword, "")
	if rr := f.changePassword(s.AccessToken, bootstrapPassword, chosenPassword); rr.Code != http.StatusOK {
		t.Fatalf("change password = %d %s", rr.Code, rr.Body)
	}

	var user models.Mailbox
	if err := f.db.Where("address = ?", f.name).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.BootstrapExpiresAt != nil {
		t.Errorf("bootstrap_expires_at = %v after choosing a password, want it cleared", user.BootstrapExpiresAt)
	}
	if rr, _ := f.login(chosenPassword, ""); rr.Code != http.StatusOK {
		t.Errorf("login with the chosen password = %d %s", rr.Code, rr.Body)
	}
}

// The recovery for a password that leaked, or ran out, before anyone finished
// the install: deploy again with a new one.
func TestFirstSignIn_RedeployingWithANewPasswordRearms(t *testing.T) {
	f := newFirstSignIn(t)
	_, old := f.login(bootstrapPassword, "") // say, whoever found the leaked one
	f.expire(t)

	const redeployed = "the-password-of-the-second-deploy-42"
	outcome, err := bootstrap.EnsureFirstAdmin(f.db, f.name, redeployed, true, time.Now())
	if err != nil || outcome != bootstrap.FirstAdminRearmed {
		t.Fatalf("redeploy: outcome=%v err=%v, want rearmed", outcome, err)
	}

	if rr, _ := f.login(bootstrapPassword, ""); rr.Code != http.StatusUnauthorized || strings.Contains(rr.Body.String(), "bootstrap_expired") {
		t.Errorf("old password after redeploy = %d %s, want a plain 401", rr.Code, rr.Body)
	}
	if rr, _ := f.refresh(old.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Errorf("old session's refresh after redeploy = %d, want 401", rr.Code)
	}
	rr, s := f.login(redeployed, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("new password = %d %s", rr.Code, rr.Body)
	}
	if want := []string{"password", "two_factor"}; !reflect.DeepEqual(s.SetupRequired, want) {
		t.Errorf("setup_required = %v, want %v", s.SetupRequired, want)
	}
}
