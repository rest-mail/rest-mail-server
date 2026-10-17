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
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

// openAuthzTestDB connects to the unit-test Postgres and migrates the tables the
// object-level-authorization tests touch. It skips (never fails) when no database
// is reachable, matching the repo's depless-local / DB-in-CI convention.
func openAuthzTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := connectTestDB()
	if err != nil {
		t.Skipf("authz IDOR DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(
		&models.Domain{},
		&models.Mailbox{},
		&models.Role{},
		&models.Capability{},
		&models.RoleCapability{},
		&models.Grant{},
		&models.GrantActivation{},
		&models.Credential{},
		&models.SieveScript{},
		&models.Quarantine{},
		&models.Message{},
	); err != nil {
		t.Skipf("authz IDOR DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// authzFixture is a pair of isolated tenants: an owner and an unrelated
// attacker, each an account of its own. Neither holds a grant on the other, so
// the attacker must never be able to resolve or touch the owner's mailbox data.
type authzFixture struct {
	ownerMailbox    models.Mailbox
	attackerMailbox models.Mailbox
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
			Active:    true,
		}
		if err := db.Create(&mb).Error; err != nil {
			t.Fatalf("create mailbox %s: %v", local, err)
		}
		return mb
	}
	return authzFixture{
		ownerMailbox:    mk("owner"),
		attackerMailbox: mk("attacker"),
	}
}

// authzRequest builds a request carrying the given account's session claims
// and chi URL params, exactly as the JWT middleware + router would supply
// them.
func authzRequest(method, target string, accountID uint, params map[string]string, body string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	claims := &auth.Claims{AccountID: accountID}
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
	acctParam := map[string]string{"id": uintToStr(fx.ownerMailbox.ID)}

	// Owner GET → 200.
	rr := httptest.NewRecorder()
	h.GetScript(rr, authzRequest(http.MethodGet, "/sieve", fx.ownerMailbox.ID, acctParam, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner GET: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	// Attacker GET on the owner's account → 403.
	rr = httptest.NewRecorder()
	h.GetScript(rr, authzRequest(http.MethodGet, "/sieve", fx.attackerMailbox.ID, acctParam, ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attacker GET: expected 403, got %d", rr.Code)
	}

	// Attacker PUT (overwrite) → 403, and the owner's script is unchanged.
	rr = httptest.NewRecorder()
	h.PutScript(rr, authzRequest(http.MethodPut, "/sieve", fx.attackerMailbox.ID, acctParam, `{"script":"# pwned","active":true}`))
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
	h.DeleteScript(rr, authzRequest(http.MethodDelete, "/sieve", fx.attackerMailbox.ID, acctParam, ""))
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
	h.PutScript(rr, authzRequest(http.MethodPut, "/sieve", fx.ownerMailbox.ID, acctParam, `{"script":"# updated","active":true}`))
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

	h := NewPipelineHandler(tx, nil, filters.JSSidecar{})
	ownerAcct := map[string]string{"id": uintToStr(fx.ownerMailbox.ID)}

	// Owner LIST → 200 with the one row.
	rr := httptest.NewRecorder()
	h.ListQuarantine(rr, authzRequest(http.MethodGet, "/quarantine", fx.ownerMailbox.ID, ownerAcct, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner LIST: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("owner LIST should include the owner's quarantined message, got %s", rr.Body.String())
	}

	// Attacker LIST on the owner's account id → 403 (account not owned).
	rr = httptest.NewRecorder()
	h.ListQuarantine(rr, authzRequest(http.MethodGet, "/quarantine", fx.attackerMailbox.ID, ownerAcct, ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attacker LIST (foreign account): expected 403, got %d", rr.Code)
	}

	// Attacker RELEASE targeting the owner's account id → 403.
	rr = httptest.NewRecorder()
	h.ReleaseQuarantine(rr, authzRequest(http.MethodPost, "/quarantine/release", fx.attackerMailbox.ID,
		map[string]string{"id": uintToStr(fx.ownerMailbox.ID), "mid": uintToStr(q.ID)}, ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attacker RELEASE (foreign account): expected 403, got %d", rr.Code)
	}

	// Attacker RELEASE using their OWN valid account but the owner's row id → 404,
	// and the row must remain un-released (the cross-mailbox-by-row-id vector).
	rr = httptest.NewRecorder()
	h.ReleaseQuarantine(rr, authzRequest(http.MethodPost, "/quarantine/release", fx.attackerMailbox.ID,
		map[string]string{"id": uintToStr(fx.attackerMailbox.ID), "mid": uintToStr(q.ID)}, ""))
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
	h.DeleteQuarantine(rr, authzRequest(http.MethodDelete, "/quarantine", fx.attackerMailbox.ID,
		map[string]string{"id": uintToStr(fx.attackerMailbox.ID), "mid": uintToStr(q.ID)}, ""))
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
	h.ReleaseQuarantine(rr, authzRequest(http.MethodPost, "/quarantine/release", fx.ownerMailbox.ID,
		map[string]string{"id": uintToStr(fx.ownerMailbox.ID), "mid": uintToStr(q.ID)}, ""))
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
