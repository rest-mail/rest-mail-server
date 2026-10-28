package handlers

// Install-time Sieve validation + content-hash gating, at the API boundary.
// These reuse the DB harness from authz_idor_test.go (openAuthzTestDB /
// seedAuthzFixture / authzRequest / uintToStr) and therefore skip when no test
// database is reachable, matching the repo's DB-in-CI convention.

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	serveSieve(h, rr, authzRequest(http.MethodPut, "/sieve", ownerAccountID, params, body))
	return rr
}

func sieveRowCount(t *testing.T, h *SieveHandler, mailboxID uint) int64 {
	t.Helper()
	var n int64
	h.db.Model(&models.SieveScript{}).Where("mailbox_id = ?", mailboxID).Count(&n)
	return n
}

func TestSieveScriptRequiredByContract(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewSieveHandler(tx)

	for _, body := range []string{`{}`, `{"script":null}`} {
		t.Run(body, func(t *testing.T) {
			put := putSieve(t, h, fx.ownerMailbox.ID, body)
			if put.Code != http.StatusUnprocessableEntity {
				t.Errorf("PUT: want 422, got %d (%s)", put.Code, put.Body.String())
			}
			if !strings.Contains(put.Body.String(), `"script":"required"`) {
				t.Errorf("PUT: missing script field error: %s", put.Body.String())
			}
			validate := httptest.NewRecorder()
			serveSieve(h, validate, authzRequest(http.MethodPost, "/sieve/validate", fx.ownerMailbox.ID,
				map[string]string{"id": uintToStr(fx.ownerMailbox.ID)}, body))
			if validate.Code != http.StatusUnprocessableEntity {
				t.Errorf("validate: want 422, got %d (%s)", validate.Code, validate.Body.String())
			}
			if !strings.Contains(validate.Body.String(), `"script":"required"`) {
				t.Errorf("validate: missing script field error: %s", validate.Body.String())
			}
		})
	}
	if n := sieveRowCount(t, h, fx.ownerMailbox.ID); n != 0 {
		t.Fatalf("missing script must not be stored, found %d row(s)", n)
	}

	// An explicit empty string satisfies the required field and means no rules.
	if rr := putSieve(t, h, fx.ownerMailbox.ID, `{"script":""}`); rr.Code != http.StatusCreated {
		t.Fatalf("explicit empty script: want 201, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// (a) An invalid script (uses fileinto without require) is rejected at install
// with HTTP 422 and is NOT stored.
func TestSieveInstall_InvalidScriptRejected(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewSieveHandler(tx)

	rr := putSieve(t, h, fx.ownerMailbox.ID,
		`{"script":"if header :contains \"Subject\" \"x\" { fileinto \"Spam\"; }","active":true}`)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for an invalid script, got %d (%s)", rr.Code, rr.Body.String())
	}
	if n := sieveRowCount(t, h, fx.ownerMailbox.ID); n != 0 {
		t.Fatalf("invalid script must not be stored, found %d row(s)", n)
	}
}

// (b) A script that PARSES but fails the safe dry-run (redirect with no domain)
// is rejected at install with HTTP 422 and is NOT stored.
func TestSieveInstall_DryRunErrorRejected(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewSieveHandler(tx)

	rr := putSieve(t, h, fx.ownerMailbox.ID, `{"script":"redirect \"bogus\";","active":true}`)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a dry-run failure, got %d (%s)", rr.Code, rr.Body.String())
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
	rr := putSieve(t, h, fx.ownerMailbox.ID,
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
