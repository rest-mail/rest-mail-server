package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
)

// An internationalized domain's policy host is fetched under its A-label
// (#153): the Host header never carries the Unicode name the domain may be
// stored under. Every spelling of the host finds the policy, and the policy's
// mx patterns are written as A-labels.
func TestServePolicy_IDN(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.MTASTSPolicy{}); err != nil {
		t.Skipf("MTA-STS DB test skipped: migrate failed (%v)", err)
	}

	serve := func(t *testing.T, h *MTASTSHandler, host string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/.well-known/mta-sts.txt", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		policyFile(h)(rec, req)
		return rec
	}

	n := time.Now().UnixNano()
	unicode := fmt.Sprintf("bücher.d%d.test", n)
	ascii := fmt.Sprintf("xn--bcher-kva.d%d.test", n)

	for _, stored := range []string{unicode, ascii} {
		t.Run("stored "+stored, func(t *testing.T) {
			tx := gdb.Begin()
			t.Cleanup(func() { tx.Rollback() })

			domain := models.Domain{Name: stored}
			if err := tx.Create(&domain).Error; err != nil {
				t.Fatalf("create domain: %v", err)
			}
			policy := models.MTASTSPolicy{
				DomainID: domain.ID, Mode: "enforce", MaxAge: 86400, Active: true,
				MXHosts: "mx.bücher.example, *.Bücher.example,mx.example.com",
			}
			if err := tx.Create(&policy).Error; err != nil {
				t.Fatalf("create policy: %v", err)
			}
			h := NewMTASTSHandler(tx)

			want := "version: STSv1\nmode: enforce\n" +
				"mx: mx.xn--bcher-kva.example\nmx: *.xn--bcher-kva.example\nmx: mx.example.com\n" +
				"max_age: 86400\n"
			for _, host := range []string{
				"mta-sts." + ascii,
				"MTA-STS." + strings.ToUpper(ascii),
				"mta-sts." + ascii + ".",
				"mta-sts." + ascii + ":443",
				"mta-sts." + unicode,
			} {
				rec := serve(t, h, host)
				if rec.Code != http.StatusOK || rec.Body.String() != want {
					t.Errorf("Host %q: %d %q, want 200 %q", host, rec.Code, rec.Body.String(), want)
				}
			}
		})
	}

	t.Run("invalid host", func(t *testing.T) {
		h := NewMTASTSHandler(gdb)
		for _, host := range []string{"mta-sts.bad_.test", "mta-sts.xn--.test", ""} {
			if rec := serve(t, h, host); rec.Code != http.StatusNotFound {
				t.Errorf("Host %q: %d, want 404", host, rec.Code)
			}
		}
	})

	t.Run("invalid mx host", func(t *testing.T) {
		tx := gdb.Begin()
		t.Cleanup(func() { tx.Rollback() })

		domain := models.Domain{Name: unicode}
		if err := tx.Create(&domain).Error; err != nil {
			t.Fatalf("create domain: %v", err)
		}
		policy := models.MTASTSPolicy{DomainID: domain.ID, Mode: "enforce", MaxAge: 86400, Active: true, MXHosts: "mx.example.com,bad_.example"}
		if err := tx.Create(&policy).Error; err != nil {
			t.Fatalf("create policy: %v", err)
		}
		rec := serve(t, NewMTASTSHandler(tx), "mta-sts."+ascii)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "mx:") {
			t.Errorf("got %d %q, want 500 and no policy", rec.Code, rec.Body.String())
		}
	})
}

// policyFile is the MTA-STS policy file through its registration.
func policyFile(h *MTASTSHandler) http.HandlerFunc {
	routes := chi.NewRouter()
	h.RegisterPolicyFile(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()})
	return routes.ServeHTTP
}

// A Host that is not a domain is 404 before any domain is looked up, in the API's error
// envelope: a sending MTA reads any answer but 200 as no policy.
func TestServePolicy_HostNotADomain(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/.well-known/mta-sts.txt", nil)
	req.Host = "not a domain"
	rec := httptest.NewRecorder()
	policyFile(NewMTASTSHandler(nil))(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Fatalf("want 404 not_found, got %d %s", rec.Code, rec.Body)
	}
}
