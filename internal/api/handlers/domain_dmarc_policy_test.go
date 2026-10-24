package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
)

// A domain's DMARC policy starts at none, is set when it is created or later through the
// admin API, and anything but none, quarantine or reject is refused with nothing changed
// (RFC 9989 §7.4, #729).
func TestDomainDMARCPolicy_SetThroughTheAdminAPI(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.Pipeline{}); err != nil {
		t.Skipf("domain DMARC policy test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	h := NewDomainHandler(tx, nil)

	create := func(body string) (int, Domain) {
		t.Helper()
		rr := httptest.NewRecorder()
		domainRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/domains", bytes.NewBufferString(body)))
		var answer struct {
			Data Domain `json:"data"`
		}
		if rr.Code == http.StatusCreated {
			if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decode the created domain: %v", err)
			}
		}
		return rr.Code, answer.Data
	}
	stored := func(id uint) string {
		t.Helper()
		var d models.Domain
		if err := tx.First(&d, id).Error; err != nil {
			t.Fatalf("reload the domain: %v", err)
		}
		return d.DMARCPolicy
	}

	stamp := time.Now().UnixNano()
	code, plain := create(fmt.Sprintf(`{"name":"dmarc-%d.test"}`, stamp))
	if code != http.StatusCreated || plain.DMARCPolicy != models.DMARCPolicyNone || stored(plain.ID) != models.DMARCPolicyNone {
		t.Fatalf("a new domain: %d, answered %q, stored %q; want 201 and none", code, plain.DMARCPolicy, stored(plain.ID))
	}
	code, strict := create(fmt.Sprintf(`{"name":"dmarc-reject-%d.test","dmarc_policy":"reject"}`, stamp))
	if code != http.StatusCreated || strict.DMARCPolicy != models.DMARCPolicyReject || stored(strict.ID) != models.DMARCPolicyReject {
		t.Fatalf("created asking for reject: %d, answered %q; want 201 and reject", code, strict.DMARCPolicy)
	}
	if code, _ := create(fmt.Sprintf(`{"name":"dmarc-bad-%d.test","dmarc_policy":"strict"}`, stamp)); code != http.StatusUnprocessableEntity {
		t.Fatalf("created asking for strict: %d, want 422", code)
	}

	update := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/domains/"+fmt.Sprint(plain.ID), bytes.NewBufferString(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprint(plain.ID))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		domainRoutes(h).ServeHTTP(rr, req)
		return rr
	}
	if rr := update(`{"dmarc_policy":"quarantine"}`); rr.Code != http.StatusOK || stored(plain.ID) != models.DMARCPolicyQuarantine {
		t.Fatalf("update to quarantine: %d %s, stored %q", rr.Code, rr.Body, stored(plain.ID))
	}
	if rr := update(`{"active":false}`); rr.Code != http.StatusOK || stored(plain.ID) != models.DMARCPolicyQuarantine {
		t.Fatalf("an update naming no policy changed it: %d, stored %q", rr.Code, stored(plain.ID))
	}
	if rr := update(`{"dmarc_policy":""}`); rr.Code != http.StatusUnprocessableEntity || stored(plain.ID) != models.DMARCPolicyQuarantine {
		t.Fatalf("update to an empty policy: %d %s, stored %q; want 422 and unchanged", rr.Code, rr.Body, stored(plain.ID))
	}
}

// Binding refuses a DMARC policy that is not one, in any case and empty among them, before
// anything is looked up or stored.
func TestDomainDMARCPolicy_RefusedBeforeTheDatabase(t *testing.T) {
	const refused = `{"error":{"code":"validation_failed","message":"Request body failed validation",` +
		`"details":{"fields":{"dmarc_policy":"must be 'none', 'quarantine' or 'reject'"}}}}`
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/admin/domains", `{"name":"example.org","dmarc_policy":"strict"}`},
		{http.MethodPost, "/api/v1/admin/domains", `{"name":"example.org","dmarc_policy":""}`},
		{http.MethodPatch, "/api/v1/admin/domains/1", `{"dmarc_policy":"REJECT"}`},
		{http.MethodPatch, "/api/v1/admin/domains/1", `{"dmarc_policy":""}`},
	} {
		rr := httptest.NewRecorder()
		domainRoutes(NewDomainHandler(nil, nil)).ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body)))
		if rr.Code != http.StatusUnprocessableEntity || rr.Body.String() != refused+"\n" {
			t.Errorf("%s %s %s = %d %s, want 422 %s", tc.method, tc.path, tc.body, rr.Code, rr.Body, refused)
		}
	}
}
