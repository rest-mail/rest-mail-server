package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
)

// A domain that still holds an account or an alias is not deleted (#388):
// the delete answers 409 domain_not_empty and the domain stays. Deleting it
// had left its accounts pointing at no domain, so every send from one failed
// with a 500. An empty domain is deleted with 204. Needs the unit-test
// Postgres; skips when none is reachable.
func TestDomainDelete_RefusesADomainWithAddresses(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.Alias{}); err != nil {
		t.Skipf("domain delete test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	h := NewDomainHandler(tx, nil)

	stamp := time.Now().UnixNano()
	domain := func(label string) models.Domain {
		d := models.Domain{Name: fmt.Sprintf("%s-%d.test", label, stamp), ServerType: "traditional", Active: true}
		if err := tx.Create(&d).Error; err != nil {
			t.Fatalf("create domain %s: %v", d.Name, err)
		}
		return d
	}
	del := func(id uint) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/admin/domains/%d", id), nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprint(id))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		domainRoutes(h).ServeHTTP(rr, req)
		return rr
	}
	exists := func(id uint) bool {
		var n int64
		if err := tx.Model(&models.Domain{}).Where("id = ?", id).Count(&n).Error; err != nil {
			t.Fatalf("count domain %d: %v", id, err)
		}
		return n > 0
	}

	withAccount := domain("with-account")
	if err := tx.Create(&models.Mailbox{DomainID: withAccount.ID, LocalPart: "alice",
		Address: "alice@" + withAccount.Name}).Error; err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	withAlias := domain("with-alias")
	if err := tx.Create(&models.Alias{DomainID: withAlias.ID, SourceAddress: "sales@" + withAlias.Name,
		DestinationAddress: "alice@" + withAccount.Name}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	empty := domain("empty")

	for _, tc := range []struct {
		name   string
		id     uint
		status int
		code   string
		kept   bool
	}{
		{"with an account", withAccount.ID, http.StatusConflict, "domain_not_empty", true},
		{"with an alias", withAlias.ID, http.StatusConflict, "domain_not_empty", true},
		{"empty", empty.ID, http.StatusNoContent, "", false},
	} {
		rr := del(tc.id)
		if rr.Code != tc.status {
			t.Fatalf("delete the domain %s: status = %d, want %d (body %s)", tc.name, rr.Code, tc.status, rr.Body.String())
		}
		if tc.code != "" && errorCodeOf(rr) != tc.code {
			t.Errorf("delete the domain %s: code = %q, want %q", tc.name, errorCodeOf(rr), tc.code)
		}
		if got := exists(tc.id); got != tc.kept {
			t.Errorf("after deleting the domain %s: exists = %v, want %v", tc.name, got, tc.kept)
		}
	}
}
