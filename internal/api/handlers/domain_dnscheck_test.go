package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
)

// The DNS check looks for DKIM under the selector the domain signs with, not
// under "default" (#358). A domain with no DKIM key has nothing to publish, and
// the check says so.
func TestDNSCheck_DKIMUsesTheDomainsSelector(t *testing.T) {
	gdb := openAuthzTestDB(t)

	cases := []struct {
		name               string
		selector, key      string
		wantName           string // "%s" is the domain
		wantStatus         string
		publishedSelectors []string
	}{
		{"own selector published", "rm2026", "-- key --", "rm2026._domainkey.%s", "ok", []string{"rm2026"}},
		{"own selector missing", "rm2026", "-- key --", "rm2026._domainkey.%s", "missing", []string{"default"}},
		{"no key", "", "", "%s", "missing", []string{"default"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := gdb.Begin()
			t.Cleanup(func() { tx.Rollback() })

			domain := models.Domain{
				Name:           fmt.Sprintf("dkim-%d.test", time.Now().UnixNano()),
				DKIMSelector:   tc.selector,
				DKIMPrivateKey: tc.key,
			}
			if err := tx.Create(&domain).Error; err != nil {
				t.Fatalf("create domain: %v", err)
			}
			published := map[string][]string{}
			for _, sel := range tc.publishedSelectors {
				published[sel+"._domainkey."+domain.Name] = []string{"v=DKIM1; k=rsa; p=MIIB"}
			}

			h := NewDomainHandler(tx, nil)
			h.lookupMX = func(name string) ([]*net.MX, error) {
				return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
			}
			h.lookupTXT = func(name string) ([]string, error) {
				if txts, ok := published[name]; ok {
					return txts, nil
				}
				return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
			}

			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/domains/%d/dns", domain.ID), nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", fmt.Sprint(domain.ID))
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rr := httptest.NewRecorder()
			h.DNSCheck(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
			}

			var resp struct {
				Data struct {
					Records []struct {
						Type, Name, Status, Message string
					} `json:"records"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			var dkim []string
			for _, rec := range resp.Data.Records {
				if rec.Type != "DKIM" {
					continue
				}
				dkim = append(dkim, rec.Name)
				if want := fmt.Sprintf(tc.wantName, domain.Name); rec.Name != want || rec.Status != tc.wantStatus {
					t.Errorf("DKIM check = %s %s (%s), want %s %s", rec.Name, rec.Status, rec.Message, want, tc.wantStatus)
				}
			}
			if len(dkim) != 1 {
				t.Fatalf("got %d DKIM checks (%v), want 1", len(dkim), dkim)
			}
		})
	}
}
