package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// openLoginTestDB connects to the unit-test Postgres and migrates the tables the
// login/refresh flow touches. It skips (never fails) when no database is
// reachable, matching the repo's depless-local / DB-in-CI convention.
func openLoginTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := connectTestDB()
	if err != nil {
		t.Skipf("login DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(accountModels...); err != nil {
		t.Skipf("login DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func doLogin(h *AuthHandler, body map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	serveAuth(h, rr, req)
	return rr
}

func loginErrorMessage(rr *httptest.ResponseRecorder) string {
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	return resp.Error.Message
}

func seedLoginMailbox(t *testing.T, gdb *gorm.DB, password string) string {
	t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	addr := "ct-" + suffix + "@example.test"
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	mb := models.Mailbox{
		DomainID:  1,
		LocalPart: "ct-" + suffix,
		Address:   addr,
		Active:    true,
	}
	if err := gdb.Create(&mb).Error; err != nil {
		t.Fatalf("seed mailbox: %v", err)
	}
	if password != "" {
		if _, err := repositories.NewCredentialRepository(gdb).SetPassword(mb.ID, hash, nil, time.Now()); err != nil {
			t.Fatalf("seed password: %v", err)
		}
	}
	t.Cleanup(func() {
		gdb.Where("account_id = ?", mb.ID).Delete(&models.Credential{})
		gdb.Where("address = ?", addr).Delete(&models.Mailbox{})
	})
	return addr
}

// TestLogin_UnknownUserAndWrongPasswordAreIndistinguishable: OSI-24. An unknown
// address and a known address with the wrong password must return the SAME
// status and message, so a missing account can't be distinguished from a wrong
// password. (The constant-time bcrypt burn against the dummy hash equalizes the
// timing; here we assert the observable response is uniform.)
func TestLogin_UnknownUserAndWrongPasswordAreIndistinguishable(t *testing.T) {
	gdb := openLoginTestDB(t)
	jwtSvc := auth.NewJWTService("login-secret", 15*time.Minute, 7*24*time.Hour)
	h := NewAuthHandler(gdb, jwtSvc, "", nil)

	addr := seedLoginMailbox(t, gdb, "correct-horse-battery")
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	unknown := doLogin(h, map[string]string{"email": "nobody-" + suffix + "@example.test", "password": "whatever"})
	wrong := doLogin(h, map[string]string{"email": addr, "password": "definitely-wrong"})

	if unknown.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized {
		t.Fatalf("expected both 401, got unknown=%d wrong=%d", unknown.Code, wrong.Code)
	}
	if um, wm := loginErrorMessage(unknown), loginErrorMessage(wrong); um != wm {
		t.Errorf("unknown-user vs wrong-password messages differ: %q vs %q", um, wm)
	}
	if got := loginErrorMessage(unknown); got != "Invalid email or password" {
		t.Errorf("unexpected uniform message: %q", got)
	}
}

// TestLogin_PersistsActiveRefreshToken: OSI-10. A successful login records its
// refresh token in the rotation ledger as active, keyed by the token's jti.
func TestLogin_PersistsActiveRefreshToken(t *testing.T) {
	gdb := openLoginTestDB(t)
	jwtSvc := auth.NewJWTService("login-secret", 15*time.Minute, 7*24*time.Hour)
	h := NewAuthHandler(gdb, jwtSvc, "", nil)

	addr := seedLoginMailbox(t, gdb, "correct-horse-battery")

	rr := doLogin(h, map[string]string{"email": addr, "password": "correct-horse-battery"})
	if rr.Code != http.StatusOK {
		t.Fatalf("login: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	var cookie string
	for _, c := range rr.Result().Cookies() {
		if c.Name == "restmail_refresh" {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("login set no refresh cookie")
	}
	claims, err := jwtSvc.ValidateRefreshToken(cookie)
	if err != nil {
		t.Fatalf("refresh cookie invalid: %v", err)
	}
	t.Cleanup(func() { gdb.Where("jti = ?", claims.ID).Delete(&models.RefreshToken{}) })

	rec, err := repositories.NewRefreshTokenRepository(gdb).GetByJTI(claims.ID)
	if err != nil {
		t.Fatalf("refresh token not in ledger: %v", err)
	}
	if rec.Status != models.RefreshTokenActive {
		t.Errorf("ledger status = %q, want active", rec.Status)
	}
	if rec.AccountID != claims.AccountID || rec.AccountID == 0 {
		t.Errorf("ledger account_id = %d, want %d", rec.AccountID, claims.AccountID)
	}
}

// TestLogin_ByAddressOnly: sign-in takes the account's address. A username is
// not a field it knows: without an email it is a 422. An account with no
// password of its own (one reached only through its owners) cannot be signed
// in to, and is answered as a wrong password.
func TestLogin_ByAddressOnly(t *testing.T) {
	gdb := openLoginTestDB(t)
	jwtSvc := auth.NewJWTService("login-secret", 15*time.Minute, 7*24*time.Hour)
	h := NewAuthHandler(gdb, jwtSvc, "", nil)

	if rr := doLogin(h, map[string]string{"username": "admin", "password": "whatever"}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("username sign-in: %d, want 422 (%s)", rr.Code, rr.Body.String())
	}
	owned := seedLoginMailbox(t, gdb, "")
	if rr := doLogin(h, map[string]string{"email": owned, "password": "anything at all"}); rr.Code != http.StatusUnauthorized || loginErrorMessage(rr) != "Invalid email or password" {
		t.Errorf("an account with no password: %d %s", rr.Code, rr.Body.String())
	}

	addr := seedLoginMailbox(t, gdb, "correct-horse-battery")
	rr := doLogin(h, map[string]string{"email": addr, "password": "correct-horse-battery"})
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data loginResponse `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var mb models.Mailbox
	gdb.Where("address = ?", addr).First(&mb)
	if resp.Data.User.ID != mb.ID || resp.Data.User.Email != addr || len(resp.Data.Capabilities) != 0 {
		t.Errorf("user = %+v, capabilities %v; want the mailbox's id and address, and no admin capabilities", resp.Data.User, resp.Data.Capabilities)
	}
	claims, err := jwtSvc.ValidateAccessToken(accessCookieValue(rr))
	if err != nil || claims.AccountID != mb.ID || claims.Address != addr || !claims.HasAMR(auth.AMRPassword) {
		t.Errorf("session claims = %+v (%v)", claims, err)
	}
	// Addresses are matched as they are stored: in lower case.
	if rr := doLogin(h, map[string]string{"email": " " + strings.ToUpper(addr) + " ", "password": "correct-horse-battery"}); rr.Code != http.StatusOK {
		t.Errorf("the address in capitals: %d", rr.Code)
	}
}
