package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

func openDomainMutationScratchDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Reuse the allocator that verifies a fresh database identity before writes.
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Pipeline{}, &models.Certificate{}); err != nil {
		t.Fatal(err)
	}
	return db
}
func domainMutationRequest(t *testing.T, h *DomainHandler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	domainRoutes(h).ServeHTTP(rr, r)
	return rr
}
func TestDomainMutationCreationIsAtomicAndHonorsZeroQuota(t *testing.T) {
	db := openDomainMutationScratchDB(t)
	h := NewDomainHandler(db, nil)
	counts := func() (int64, int64) {
		t.Helper()
		var a, b int64
		if err := db.Model(&models.Domain{}).Count(&a).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&models.Pipeline{}).Count(&b).Error; err != nil {
			t.Fatal(err)
		}
		return a, b
	}
	create := func(name string, zero bool) *httptest.ResponseRecorder {
		t.Helper()
		body := map[string]interface{}{"name": name}
		if zero {
			body["default_quota_bytes"] = 0
		}
		raw, _ := json.Marshal(body)
		return domainMutationRequest(t, h, http.MethodPost, "/api/v1/admin/domains", string(raw))
	}
	for _, tc := range []struct {
		name  string
		zero  bool
		quota int64
	}{{"default-domain.test", false, 1 << 30}, {"zero-domain.test", true, 0}} {
		rr := create(tc.name, tc.zero)
		if rr.Code != 201 {
			t.Fatalf("create:%d %s", rr.Code, rr.Body)
		}
		var answer struct{ Data Domain }
		if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
			t.Fatal(err)
		}
		var stored models.Domain
		if err := db.First(&stored, answer.Data.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Active || stored.ServerType != "traditional" || stored.DefaultQuotaBytes != tc.quota || answer.Data.DefaultQuotaBytes != tc.quota {
			t.Fatalf("domain defaults/explicit zero:%+v %s", stored, rr.Body)
		}
		var rows []models.Pipeline
		if err := db.Where("domain_id = ?", stored.ID).Order("direction ASC").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("default pipelines:%+v", rows)
		}
		for i, direction := range []string{"inbound", "outbound"} {
			var a, b interface{}
			if err := json.Unmarshal(rows[i].Filters, &a); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(pipeline.DefaultPipelineJSON(direction), &b); err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(a)
			want, _ := json.Marshal(b)
			if rows[i].Direction != direction || !rows[i].Active || !bytes.Equal(got, want) {
				t.Fatalf("default pipeline mismatch:%+v", rows[i])
			}
		}
	}
	beforeA, beforeB := counts()
	rr := create("default-domain.test", false)
	if rr.Code != 409 {
		t.Fatalf("duplicate:%d %s", rr.Code, rr.Body)
	}
	failDirection := "inbound"
	const callback = "domain_mutation_pipeline_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*models.Pipeline); ok && row.Direction == failDirection {
			tx.AddError(errors.New("private pipeline storage detail"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(callback) })
	for _, direction := range []string{"inbound", "outbound"} {
		failDirection = direction
		rr = create(direction+"-failure.test", false)
		if rr.Code != 500 || strings.Contains(rr.Body.String(), "private") {
			t.Fatalf("pipeline failure:%d %s", rr.Code, rr.Body)
		}
		a, b := counts()
		if a != beforeA || b != beforeB {
			t.Fatalf("partial provisioning persisted after%s failure:%d/%d want%d/%d", direction, a, b, beforeA, beforeB)
		}
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/domains", strings.NewReader(`{"name":"cancelled-domain.test"}`))
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	r = r.WithContext(ctx)
	rr = httptest.NewRecorder()
	domainRoutes(h).ServeHTTP(rr, r)
	if rr.Code != 500 {
		t.Fatalf("cancelled creation:%d %s", rr.Code, rr.Body)
	}
	a, b := counts()
	if a != beforeA || b != beforeB {
		t.Fatal("cancelled transaction changed rows")
	}
}
func TestDomainMutationActivationAndReloadFailure(t *testing.T) {
	db := openDomainMutationScratchDB(t)
	h := NewDomainHandler(db, nil)
	rr := domainMutationRequest(t, h, http.MethodPost, "/api/v1/admin/domains", `{"name":"activation-domain.test","default_quota_bytes":0,"sending_policy":"bulk"}`)
	if rr.Code != 201 {
		t.Fatalf("create:%d %s", rr.Code, rr.Body)
	}
	var answer struct{ Data Domain }
	if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/domains/%d", answer.Data.ID)
	patch := func(body string) *httptest.ResponseRecorder {
		return domainMutationRequest(t, h, http.MethodPatch, path, body)
	}
	rr = patch(`{"active":true}`)
	if rr.Code != 409 {
		t.Fatalf("activation without cert:%d %s", rr.Code, rr.Body)
	}
	now := time.Now()
	for _, window := range [][2]time.Time{{now.Add(-2 * time.Hour), now.Add(-time.Hour)}, {now.Add(time.Hour), now.Add(2 * time.Hour)}} {
		if err := db.Create(&models.Certificate{DomainID: answer.Data.ID, CertPEM: "public-test-certificate", KeyPEM: "public-test-key", Issuer: "Fixture", NotBefore: window[0], NotAfter: window[1]}).Error; err != nil {
			t.Fatal(err)
		}
	}
	rr = patch(`{"active":true}`)
	if rr.Code != 409 {
		t.Fatalf("expired/future certificate accepted:%d %s", rr.Code, rr.Body)
	}
	if err := db.Create(&models.Certificate{DomainID: answer.Data.ID, CertPEM: "public-test-certificate", KeyPEM: "public-test-key", Issuer: "Fixture", NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	rr = patch(`{"active":true}`)
	if rr.Code != 200 {
		t.Fatalf("valid certificate activation:%d %s", rr.Code, rr.Body)
	}
	rr = patch(`{"active":false,"default_quota_bytes":0,"sending_policy":"","dmarc_policy":"quarantine"}`)
	if rr.Code != 200 {
		t.Fatalf("partial update:%d %s", rr.Code, rr.Body)
	}
	var stored models.Domain
	if err := db.First(&stored, answer.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Active || stored.DefaultQuotaBytes != 0 || stored.SendingPolicy != "" || stored.DMARCPolicy != "quarantine" || stored.Name != answer.Data.Name || !stored.CreatedAt.Equal(answer.Data.CreatedAt) {
		t.Fatalf("partial update changed wrong fields:%+v", stored)
	}
	reads := 0
	const callback = "domain_mutation_reload_failure"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "domains" {
			reads++
			if reads == 2 {
				tx.AddError(errors.New("private domain reload detail"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(callback) })
	rr = patch(`{"sending_policy":"bulk"}`)
	if rr.Code != 500 || strings.Contains(rr.Body.String(), "private") {
		t.Fatalf("reload failure:%d %s", rr.Code, rr.Body)
	}
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&stored, answer.Data.ID).Error; err != nil || stored.SendingPolicy != "bulk" {
		t.Fatalf("preceding update persistence:%+v %v", stored, err)
	}
	// PATCH does not promise rollback when a subsequent reload fails.
}
