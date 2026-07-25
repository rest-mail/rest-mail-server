package handlers

// Mass-assignment guard for CreateCustomFilter (#202).
//
// CreateCustomFilter must decode only the client-settable fields of a custom
// filter and construct the model explicitly. It must NOT honour a nested
// `domain` association object (GORM auto-saves associations on Create, so a
// caller holding only a pipelines:write token could otherwise insert a Domain
// row without domains:write — a privilege escalation) nor a client-supplied
// primary key / timestamps.
//
// These tests reuse the DB harness from authz_idor_test.go (openAuthzTestDB /
// uintToStr) and pipeline_sieve_validate_test.go (adminPipelineReq); they SKIP
// when no unit-test database is reachable, matching the repo's DB-in-CI
// convention.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// migrateCustomFilterTestDB returns a DB with the domains/custom_filters tables
// migrated, skipping (never failing) when no unit-test database is reachable.
func migrateCustomFilterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openAuthzTestDB(t) // migrates Domain (among others)
	if err := db.AutoMigrate(&models.CustomFilter{}); err != nil {
		t.Skipf("custom filter test skipped: migrate failed (%v)", err)
	}
	return db
}

func domainCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	db.Model(&models.Domain{}).Count(&n)
	return n
}

// A CreateCustomFilter body that embeds a nested `domain` object AND a
// client-set primary key must NOT create a Domain row and must ignore the
// client id. RED on pre-fix code: GORM auto-saves the nested association, so an
// extra Domain row is inserted (privilege escalation) and the custom filter is
// stored under the client-controlled id.
func TestCreateCustomFilter_RejectsNestedDomainMassAssignment(t *testing.T) {
	db := migrateCustomFilterTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	dom := models.Domain{Name: "massassign-owner.test"}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	domainsBefore := domainCount(t, tx)

	h := NewPipelineHandler(tx, nil)
	// id + created_at are client-forged; `domain` is a nested association that,
	// under the old code, GORM would auto-create as a brand-new domain row.
	const clientForgedID = 987654
	body := `{
		"id": 987654,
		"created_at": "2000-01-01T00:00:00Z",
		"domain_id": ` + uintToStr(dom.ID) + `,
		"name": "massassign-filter",
		"filter_type": "action",
		"direction": "inbound",
		"config": {"k":"v"},
		"enabled": true,
		"domain": {"name": "evil-massassign.test", "server_type": "restmail", "active": true}
	}`

	rr := httptest.NewRecorder()
	h.CreateCustomFilter(rr, adminPipelineReq(http.MethodPost, "/custom-filters", nil, body))

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}

	// (1) No Domain row may be created as a side effect of a filter create.
	if after := domainCount(t, tx); after != domainsBefore {
		t.Fatalf("CreateCustomFilter inserted %d Domain row(s) via a nested association (privilege escalation); count %d -> %d",
			after-domainsBefore, domainsBefore, after)
	}
	// Belt-and-suspenders: the specific forged domain must not exist.
	var evil int64
	tx.Model(&models.Domain{}).Where("name = ?", "evil-massassign.test").Count(&evil)
	if evil != 0 {
		t.Fatalf("the client-supplied nested domain 'evil-massassign.test' was inserted (%d row(s))", evil)
	}

	// (2) The stored filter must belong to the FK domain, ignore the client id,
	// and be otherwise well-formed.
	var cf models.CustomFilter
	if err := tx.Where("name = ?", "massassign-filter").First(&cf).Error; err != nil {
		t.Fatalf("created custom filter not found: %v", err)
	}
	if cf.ID == clientForgedID {
		t.Fatalf("client-supplied primary key %d was honoured instead of a server-assigned id", clientForgedID)
	}
	if cf.DomainID != dom.ID {
		t.Fatalf("custom filter DomainID = %d, want the FK-supplied %d", cf.DomainID, dom.ID)
	}
}

// A normal, well-formed create still works and is persisted.
func TestCreateCustomFilter_ValidCreatePersisted(t *testing.T) {
	db := migrateCustomFilterTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	dom := models.Domain{Name: "massassign-ok.test"}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	h := NewPipelineHandler(tx, nil)
	body := `{
		"domain_id": ` + uintToStr(dom.ID) + `,
		"name": "valid-filter",
		"description": "a normal filter",
		"filter_type": "transform",
		"direction": "both",
		"config": {"script":"noop"},
		"enabled": true
	}`

	rr := httptest.NewRecorder()
	h.CreateCustomFilter(rr, adminPipelineReq(http.MethodPost, "/custom-filters", nil, body))

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for a valid create, got %d (%s)", rr.Code, rr.Body.String())
	}

	var cf models.CustomFilter
	if err := tx.Where("domain_id = ? AND name = ?", dom.ID, "valid-filter").First(&cf).Error; err != nil {
		t.Fatalf("valid custom filter was not persisted: %v", err)
	}
	if cf.ID == 0 {
		t.Fatalf("persisted filter has no server-assigned id")
	}
	if cf.FilterType != "transform" || cf.Direction != "both" || cf.Description != "a normal filter" {
		t.Fatalf("persisted filter fields not stored as sent: %+v", cf)
	}
	if !cf.Enabled {
		t.Fatalf("persisted filter should be enabled")
	}
}
