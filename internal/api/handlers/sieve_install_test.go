package handlers

// Install-time Sieve validation + content-hash gating, at the API boundary.
// These reuse the DB harness from authz_idor_test.go (openAuthzTestDB /
// seedAuthzFixture / authzRequest / uintToStr) and therefore skip when no test
// database is reachable, matching the repo's DB-in-CI convention.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

// putSieve issues an owner PUT of body to the owner's sieve resource and returns
// the recorder.
func putSieve(t *testing.T, h *SieveHandler, ownerAccountID uint, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	params := map[string]string{"id": uintToStr(ownerAccountID)}
	h.PutScript(rr, authzRequest(http.MethodPut, "/sieve", ownerAccountID, params, body))
	return rr
}

func sieveRowCount(t *testing.T, h *SieveHandler, mailboxID uint) int64 {
	t.Helper()
	var n int64
	h.db.Model(&models.SieveScript{}).Where("mailbox_id = ?", mailboxID).Count(&n)
	return n
}

// (a) An invalid script (uses fileinto without require) is rejected at install
// with HTTP 400 and is NOT stored.
func TestSieveInstall_InvalidScriptRejected(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewSieveHandler(tx)

	rr := putSieve(t, h, fx.ownerAccount.ID,
		`{"script":"if header :contains \"Subject\" \"x\" { fileinto \"Spam\"; }","active":true}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid script, got %d (%s)", rr.Code, rr.Body.String())
	}
	if n := sieveRowCount(t, h, fx.ownerMailbox.ID); n != 0 {
		t.Fatalf("invalid script must not be stored, found %d row(s)", n)
	}
}

// (b) A script that PARSES but fails the safe dry-run (redirect with no domain)
// is rejected at install with HTTP 400 and is NOT stored.
func TestSieveInstall_DryRunErrorRejected(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewSieveHandler(tx)

	rr := putSieve(t, h, fx.ownerAccount.ID, `{"script":"redirect \"bogus\";","active":true}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a dry-run failure, got %d (%s)", rr.Code, rr.Body.String())
	}
	if n := sieveRowCount(t, h, fx.ownerMailbox.ID); n != 0 {
		t.Fatalf("dry-run-failing script must not be stored, found %d row(s)", n)
	}
}

// (c) A valid script is stored WITH a correct sha256 script_hash and a
// validated_at timestamp.
func TestSieveInstall_ValidScriptStoredWithHash(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewSieveHandler(tx)

	script := "require \"fileinto\";\nif header :contains \"Subject\" \"x\" { fileinto \"Filed\"; }"
	rr := putSieve(t, h, fx.ownerAccount.ID,
		`{"script":"require \"fileinto\";\nif header :contains \"Subject\" \"x\" { fileinto \"Filed\"; }","active":true}`)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for a valid script, got %d (%s)", rr.Code, rr.Body.String())
	}

	var stored models.SieveScript
	if err := tx.Where("mailbox_id = ?", fx.ownerMailbox.ID).First(&stored).Error; err != nil {
		t.Fatalf("valid script should be stored: %v", err)
	}
	want := filters.HashSieveScript(script)
	if stored.ScriptHash != want {
		t.Errorf("stored script_hash = %q, want %q", stored.ScriptHash, want)
	}
	if stored.ValidatedAt == nil {
		t.Error("expected validated_at to be set on a successful install")
	}
	if stored.Script != script {
		t.Errorf("stored script bytes differ from request:\n got: %q\nwant: %q", stored.Script, script)
	}
}
