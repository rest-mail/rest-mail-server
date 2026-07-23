package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// openLoginTestDB connects to the unit-test Postgres and migrates the tables the
// login/refresh flow touches. It skips (never fails) when no database is
// reachable, matching the repo's depless-local / DB-in-CI convention.
func openLoginTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("login DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(
		&models.Mailbox{},
		&models.WebmailAccount{},
		&models.LinkedAccount{},
		&models.RefreshToken{},
	); err != nil {
		t.Skipf("login DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func doLogin(h *AuthHandler, body map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Login(rr, req)
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
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	mb := models.Mailbox{
		DomainID:  1,
		LocalPart: "ct-" + suffix,
		Address:   addr,
		Password:  hash,
		Active:    true,
	}
	if err := gdb.Create(&mb).Error; err != nil {
		t.Fatalf("seed mailbox: %v", err)
	}
	t.Cleanup(func() {
		gdb.Where("address = ?", addr).Delete(&models.Mailbox{})
		gdb.Where("primary_mailbox_id = ?", mb.ID).Delete(&models.WebmailAccount{})
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
	h := NewAuthHandler(gdb, jwtSvc)

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
	h := NewAuthHandler(gdb, jwtSvc)

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
	if rec.UserType != "mailbox" {
		t.Errorf("ledger user_type = %q, want mailbox", rec.UserType)
	}
}
