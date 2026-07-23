package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// openAuthzTestDB connects to the unit-test Postgres and migrates the tables the
// object-level-authorization tests touch. It skips (never fails) when no database
// is reachable, matching the repo's depless-local / DB-in-CI convention.
func openAuthzTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("authz IDOR DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(
		&models.Domain{},
		&models.Mailbox{},
		&models.WebmailAccount{},
		&models.LinkedAccount{},
		&models.SieveScript{},
		&models.Quarantine{},
		&models.Message{},
	); err != nil {
		t.Skipf("authz IDOR DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// authzFixture is a pair of isolated tenants: an owner and an unrelated attacker,
// each with its own mailbox and webmail account. The two are never linked, so the
// attacker must never be able to resolve or touch the owner's mailbox data.
type authzFixture struct {
	ownerMailbox    models.Mailbox
	ownerAccount    models.WebmailAccount
	attackerMailbox models.Mailbox
	attackerAccount models.WebmailAccount
}

func seedAuthzFixture(t *testing.T, db *gorm.DB) authzFixture {
	t.Helper()
	domain := models.Domain{Name: "idor.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	mk := func(local string) models.Mailbox {
		mb := models.Mailbox{
			DomainID:  domain.ID,
			LocalPart: local,
			Address:   local + "@idor.test",
			Password:  "x",
		}
		if err := db.Create(&mb).Error; err != nil {
			t.Fatalf("create mailbox %s: %v", local, err)
		}
		return mb
	}
	ownerMB := mk("owner")
	attackerMB := mk("attacker")

	ownerAcct := models.WebmailAccount{PrimaryMailboxID: ownerMB.ID}
	if err := db.Create(&ownerAcct).Error; err != nil {
		t.Fatalf("create owner account: %v", err)
	}
	attackerAcct := models.WebmailAccount{PrimaryMailboxID: attackerMB.ID}
	if err := db.Create(&attackerAcct).Error; err != nil {
		t.Fatalf("create attacker account: %v", err)
	}

	return authzFixture{
		ownerMailbox:    ownerMB,
		ownerAccount:    ownerAcct,
		attackerMailbox: attackerMB,
		attackerAccount: attackerAcct,
	}
}

// authzRequest builds a request carrying the given mailbox-user claims and chi
// URL params, exactly as the JWT middleware + router would supply them.
func authzRequest(method, target string, webmailAccountID uint, params map[string]string, body string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	claims := &auth.Claims{
		UserType:         "mailbox",
		WebmailAccountID: webmailAccountID,
	}
	ctx := context.WithValue(context.Background(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, middleware.ClaimsKey, claims)

	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	return r.WithContext(ctx)
}

func uintToStr(u uint) string { return strconv.FormatUint(uint64(u), 10) }

// ─────────────────────────────────────────────────────────────────────────────
// #1 Sieve IDOR
// ─────────────────────────────────────────────────────────────────────────────

func TestSieveIDOR_OwnershipEnforced(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedAuthzFixture(t, tx)
	if err := tx.Create(&models.SieveScript{
		MailboxID: fx.ownerMailbox.ID,
		Script:    "# owner script",
		Active:    true,
	}).Error; err != nil {
		t.Fatalf("seed sieve script: %v", err)
	}

	h := NewSieveHandler(tx)
	acctParam := map[string]string{"id": uintToStr(fx.ownerAccount.ID)}

	// Owner GET → 200.
	rr := httptest.NewRecorder()
	h.GetScript(rr, authzRequest(http.MethodGet, "/sieve", fx.ownerAccount.ID, acctParam, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner GET: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	// Attacker GET on the owner's account → 403.
	rr = httptest.NewRecorder()
	h.GetScript(rr, authzRequest(http.MethodGet, "/sieve", fx.attackerAccount.ID, acctParam, ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attacker GET: expected 403, got %d", rr.Code)
	}

	// Attacker PUT (overwrite) → 403, and the owner's script is unchanged.
	rr = httptest.NewRecorder()
	h.PutScript(rr, authzRequest(http.MethodPut, "/sieve", fx.attackerAccount.ID, acctParam, `{"script":"# pwned","active":true}`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attacker PUT: expected 403, got %d", rr.Code)
	}
	var script models.SieveScript
	if err := tx.Where("mailbox_id = ?", fx.ownerMailbox.ID).First(&script).Error; err != nil {
		t.Fatalf("owner script should still exist: %v", err)
	}
	if script.Script != "# owner script" {
		t.Fatalf("owner script was mutated by attacker: %q", script.Script)
	}

	// Attacker DELETE → 403, and the owner's script is still present.
	rr = httptest.NewRecorder()
	h.DeleteScript(rr, authzRequest(http.MethodDelete, "/sieve", fx.attackerAccount.ID, acctParam, ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attacker DELETE: expected 403, got %d", rr.Code)
	}
	var count int64
	tx.Model(&models.SieveScript{}).Where("mailbox_id = ?", fx.ownerMailbox.ID).Count(&count)
	if count != 1 {
		t.Fatalf("owner script should survive attacker DELETE, count=%d", count)
	}

	// Owner PUT → 200 (the legitimate path still works).
	rr = httptest.NewRecorder()
	h.PutScript(rr, authzRequest(http.MethodPut, "/sieve", fx.ownerAccount.ID, acctParam, `{"script":"# updated","active":true}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner PUT: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// #2 Quarantine IDOR
// ─────────────────────────────────────────────────────────────────────────────

func TestQuarantineIDOR_OwnershipEnforced(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedAuthzFixture(t, tx)
	q := models.Quarantine{
		MailboxID:        fx.ownerMailbox.ID,
		Sender:           "spammer@evil.test",
		Subject:          "secret",
		RawMessage:       "From: spammer@evil.test\r\n\r\nbody",
		QuarantineReason: "spam",
	}
	if err := tx.Create(&q).Error; err != nil {
		t.Fatalf("seed quarantine: %v", err)
	}

	h := NewPipelineHandler(tx, nil)
	ownerAcct := map[string]string{"id": uintToStr(fx.ownerAccount.ID)}

	// Owner LIST → 200 with the one row.
	rr := httptest.NewRecorder()
	h.ListQuarantine(rr, authzRequest(http.MethodGet, "/quarantine", fx.ownerAccount.ID, ownerAcct, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner LIST: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("owner LIST should include the owner's quarantined message, got %s", rr.Body.String())
	}

	// Attacker LIST on the owner's account id → 403 (account not owned).
	rr = httptest.NewRecorder()
	h.ListQuarantine(rr, authzRequest(http.MethodGet, "/quarantine", fx.attackerAccount.ID, ownerAcct, ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attacker LIST (foreign account): expected 403, got %d", rr.Code)
	}

	// Attacker RELEASE targeting the owner's account id → 403.
	rr = httptest.NewRecorder()
	h.ReleaseQuarantine(rr, authzRequest(http.MethodPost, "/quarantine/release", fx.attackerAccount.ID,
		map[string]string{"id": uintToStr(fx.ownerAccount.ID), "mid": uintToStr(q.ID)}, ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attacker RELEASE (foreign account): expected 403, got %d", rr.Code)
	}

	// Attacker RELEASE using their OWN valid account but the owner's row id → 404,
	// and the row must remain un-released (the cross-mailbox-by-row-id vector).
	rr = httptest.NewRecorder()
	h.ReleaseQuarantine(rr, authzRequest(http.MethodPost, "/quarantine/release", fx.attackerAccount.ID,
		map[string]string{"id": uintToStr(fx.attackerAccount.ID), "mid": uintToStr(q.ID)}, ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("attacker RELEASE (foreign row via own account): expected 404, got %d", rr.Code)
	}
	var afterRelease models.Quarantine
	if err := tx.First(&afterRelease, q.ID).Error; err != nil {
		t.Fatalf("reload quarantine: %v", err)
	}
	if afterRelease.Released {
		t.Fatal("owner's quarantine row must not be released by the attacker")
	}

	// Attacker DELETE using their OWN account but the owner's row id → 404, and
	// the row must still exist.
	rr = httptest.NewRecorder()
	h.DeleteQuarantine(rr, authzRequest(http.MethodDelete, "/quarantine", fx.attackerAccount.ID,
		map[string]string{"id": uintToStr(fx.attackerAccount.ID), "mid": uintToStr(q.ID)}, ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("attacker DELETE (foreign row via own account): expected 404, got %d", rr.Code)
	}
	var count int64
	tx.Model(&models.Quarantine{}).Where("id = ?", q.ID).Count(&count)
	if count != 1 {
		t.Fatalf("owner's quarantine row must survive attacker DELETE, count=%d", count)
	}

	// Owner RELEASE → 200 and the row is marked released (legitimate path works).
	rr = httptest.NewRecorder()
	h.ReleaseQuarantine(rr, authzRequest(http.MethodPost, "/quarantine/release", fx.ownerAccount.ID,
		map[string]string{"id": uintToStr(fx.ownerAccount.ID), "mid": uintToStr(q.ID)}, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner RELEASE: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if err := tx.First(&afterRelease, q.ID).Error; err != nil {
		t.Fatalf("reload quarantine after owner release: %v", err)
	}
	if !afterRelease.Released {
		t.Fatal("owner RELEASE should mark the row released")
	}
}
