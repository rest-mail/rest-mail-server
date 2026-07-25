package handlers

// Install-time Sieve validation for ADMIN-authored domain-pipeline filter lists.
// An admin who saves a domain/instance pipeline with an embedded "sieve" filter
// whose script is broken must be rejected at SAVE time (HTTP 400), because such
// a script otherwise fails to build at DELIVERY and — under the engine's
// fail-closed policy — DEFERS every message for the whole domain.
//
// The pure-function tests (validateSievePipelineFilters) run without a database.
// The handler-level tests reuse the DB harness from authz_idor_test.go
// (openAuthzTestDB / uintToStr) and therefore SKIP when no unit-test database is
// reachable, matching the repo's DB-in-CI convention and PR #250's own tests.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

// ── Pure-function gate (no database) ─────────────────────────────────

// missingRequireFilters is a one-filter pipeline whose Sieve uses `fileinto`
// without the `require "fileinto";` it needs — a parse-stage failure.
const missingRequireFilters = `[{"name":"sieve","type":"transform","enabled":true,` +
	`"config":{"script":"if header :contains \"Subject\" \"x\" { fileinto \"Spam\"; }"}}]`

// dryRunErrorFilters is a one-filter pipeline whose Sieve PARSES but fails the
// safe dry-run (redirect to a target with no domain).
const dryRunErrorFilters = `[{"name":"sieve","type":"transform","enabled":true,` +
	`"config":{"script":"redirect \"bogus\";"}}]`

// validSieveFilters is a one-filter pipeline whose Sieve is valid.
const validSieveFilters = `[{"name":"sieve","type":"transform","enabled":true,` +
	`"config":{"script":"require \"fileinto\";\nif header :contains \"Subject\" \"x\" { fileinto \"Filed\"; }"}}]`

func TestValidateSievePipelineFilters_MissingRequireRejected(t *testing.T) {
	err := validateSievePipelineFilters(json.RawMessage(missingRequireFilters))
	if err == nil {
		t.Fatal("expected a Sieve validation error for fileinto-without-require, got nil")
	}
	var se *filters.SieveInstallError
	if !errors.As(err, &se) {
		t.Fatalf("expected *filters.SieveInstallError, got %T (%v)", err, err)
	}
	if se.Stage != "parse" {
		t.Errorf("expected parse-stage failure, got stage %q", se.Stage)
	}
	if se.MissingRequire != "fileinto" {
		t.Errorf("expected MissingRequire=fileinto, got %q", se.MissingRequire)
	}
}

func TestValidateSievePipelineFilters_DryRunErrorRejected(t *testing.T) {
	err := validateSievePipelineFilters(json.RawMessage(dryRunErrorFilters))
	if err == nil {
		t.Fatal("expected a Sieve validation error for a dry-run failure, got nil")
	}
	var se *filters.SieveInstallError
	if !errors.As(err, &se) {
		t.Fatalf("expected *filters.SieveInstallError, got %T (%v)", err, err)
	}
	if se.Stage != "dryrun" {
		t.Errorf("expected dryrun-stage failure, got stage %q", se.Stage)
	}
}

func TestValidateSievePipelineFilters_ValidAccepted(t *testing.T) {
	if err := validateSievePipelineFilters(json.RawMessage(validSieveFilters)); err != nil {
		t.Fatalf("valid Sieve pipeline rejected: %v", err)
	}
}

// A filter list with no Sieve block, an empty-script Sieve block (the default
// template shape, `{}`), an empty list, or nil must all pass — this gate only
// rejects a genuinely broken embedded Sieve script.
func TestValidateSievePipelineFilters_NothingToValidateAccepted(t *testing.T) {
	cases := map[string]string{
		"nil":              "",
		"empty array":      `[]`,
		"no sieve":         `[{"name":"header_validate","type":"action","enabled":true,"config":{}}]`,
		"empty sieve cfg":  `[{"name":"sieve","type":"transform","enabled":true,"config":{}}]`,
		"sieve empty str":  `[{"name":"sieve","type":"transform","enabled":true,"config":{"script":""}}]`,
		"malformed filters": `{"not":"an array"}`,
	}
	for name, raw := range cases {
		if err := validateSievePipelineFilters(json.RawMessage(raw)); err != nil {
			t.Errorf("%s: expected acceptance, got %v", name, err)
		}
	}
}

// A broken Sieve block is rejected even when it is disabled: it would fail the
// moment an operator flips `enabled` to true, so the save is rejected now.
func TestValidateSievePipelineFilters_DisabledBlockStillValidated(t *testing.T) {
	raw := `[{"name":"sieve","type":"transform","enabled":false,` +
		`"config":{"script":"if header :contains \"Subject\" \"x\" { fileinto \"Spam\"; }"}}]`
	if err := validateSievePipelineFilters(json.RawMessage(raw)); err == nil {
		t.Fatal("expected a disabled-but-broken Sieve block to be rejected, got nil")
	}
}

// ── Handler gate (DB-backed; skips without a database) ───────────────

// migratePipelineTestDB returns a DB with the pipelines/domains tables migrated,
// skipping (never failing) when no unit-test database is reachable.
func migratePipelineTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.Pipeline{}); err != nil {
		t.Skipf("pipeline sieve test skipped: migrate failed (%v)", err)
	}
	return db
}

// adminPipelineReq builds a request for the admin pipeline endpoints (which take
// no mailbox claims), carrying the given chi URL params and JSON body.
func adminPipelineReq(method, target string, params map[string]string, body string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx := context.WithValue(context.Background(), chi.RouteCtxKey, rctx)
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	return r.WithContext(ctx)
}

func pipelineRowCount(t *testing.T, db *gorm.DB, domainID uint) int64 {
	t.Helper()
	var n int64
	db.Model(&models.Pipeline{}).Where("domain_id = ?", domainID).Count(&n)
	return n
}

// CreatePipeline with an invalid embedded Sieve → 400 invalid_sieve, NOT
// persisted. RED on pre-fix code: the pipeline is stored (201) and only fails at
// delivery, deferring the whole domain's mail.
func TestPipelineCreate_InvalidSieveRejected(t *testing.T) {
	db := migratePipelineTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	dom := models.Domain{Name: "sieve-pipeline.test"}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	h := NewPipelineHandler(tx, nil)
	body := `{"domain_id":` + uintToStr(dom.ID) + `,"direction":"inbound","active":true,"filters":` + missingRequireFilters + `}`

	rr := httptest.NewRecorder()
	h.CreatePipeline(rr, adminPipelineReq(http.MethodPost, "/pipelines", nil, body))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid embedded Sieve, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid_sieve") {
		t.Errorf("expected invalid_sieve error code, got %s", rr.Body.String())
	}
	if n := pipelineRowCount(t, tx, dom.ID); n != 0 {
		t.Fatalf("an invalid pipeline must not be persisted, found %d row(s)", n)
	}
}

// CreatePipeline with a valid embedded Sieve → 201 and persisted.
func TestPipelineCreate_ValidSievePersisted(t *testing.T) {
	db := migratePipelineTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	dom := models.Domain{Name: "sieve-pipeline-ok.test"}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	h := NewPipelineHandler(tx, nil)
	body := `{"domain_id":` + uintToStr(dom.ID) + `,"direction":"inbound","active":true,"filters":` + validSieveFilters + `}`

	rr := httptest.NewRecorder()
	h.CreatePipeline(rr, adminPipelineReq(http.MethodPost, "/pipelines", nil, body))

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for a valid embedded Sieve, got %d (%s)", rr.Code, rr.Body.String())
	}
	if n := pipelineRowCount(t, tx, dom.ID); n != 1 {
		t.Fatalf("a valid pipeline must be persisted, found %d row(s)", n)
	}
}

// UpdatePipeline with an invalid embedded Sieve → 400 invalid_sieve and the
// stored config is UNCHANGED (the broken filters never overwrite the good ones).
func TestPipelineUpdate_InvalidSieveRejected(t *testing.T) {
	db := migratePipelineTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	dom := models.Domain{Name: "sieve-pipeline-update.test"}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	// Seed a valid pipeline first.
	seed := models.Pipeline{
		DomainID:  dom.ID,
		Direction: "inbound",
		Filters:   json.RawMessage(validSieveFilters),
		Active:    true,
	}
	if err := tx.Create(&seed).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}

	h := NewPipelineHandler(tx, nil)
	params := map[string]string{"id": uintToStr(seed.ID)}
	body := `{"filters":` + missingRequireFilters + `}`

	rr := httptest.NewRecorder()
	h.UpdatePipeline(rr, adminPipelineReq(http.MethodPatch, "/pipelines/"+uintToStr(seed.ID), params, body))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid embedded Sieve on update, got %d (%s)", rr.Code, rr.Body.String())
	}

	var after models.Pipeline
	if err := tx.First(&after, seed.ID).Error; err != nil {
		t.Fatalf("reload pipeline: %v", err)
	}
	// The good config must survive untouched. Compare semantically, not byte-for-
	// byte: Postgres jsonb reformats (whitespace, key order), so assert the stored
	// filters still hold the VALID script (its "Filed" folder, and still pass the
	// gate) and never picked up the rejected one ("Spam").
	if err := validateSievePipelineFilters(after.Filters); err != nil {
		t.Fatalf("stored filters no longer valid after a rejected update: %v", err)
	}
	if !strings.Contains(string(after.Filters), "Filed") || strings.Contains(string(after.Filters), "Spam") {
		t.Fatalf("stored filters were overwritten by an invalid update:\n got: %s", string(after.Filters))
	}
}
