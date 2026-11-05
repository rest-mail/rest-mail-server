package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dns"
	"gorm.io/gorm"
)

// The DNS check looks for DKIM under the selector the domain signs with, not
// under "default" (#358). A domain with no DKIM key has nothing to publish, and
// the check says so.
func TestDNSCheck_DKIMUsesTheDomainsSelector(t *testing.T) {
	gdb := openDNSCheckDB(t)

	cases := []struct {
		name               string
		selector, key      string
		wantName           string // "%s" is the domain
		wantStatus         string
		publishedSelectors []string
	}{
		{"own selector published", "rm2026", dnsCheckKeyPEM(t), "rm2026._domainkey.%s", "ok", []string{"rm2026"}},
		{"own selector missing", "rm2026", dnsCheckKeyPEM(t), "rm2026._domainkey.%s", "missing", []string{"default"}},
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
				published[sel+"._domainkey."+domain.Name] = []string{checkDKIMRecord}
			}

			h := NewDomainHandler(tx, nil)
			publishedZone{txt: published}.install(h)

			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/domains/%d/dns", domain.ID), nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", fmt.Sprint(domain.ID))
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rr := httptest.NewRecorder()
			domainRoutes(h).ServeHTTP(rr, req)
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

// An internationalized domain's records live under its A-label (punycode)
// name (#153): the check looks them up, and names them to publish, in that
// form, while the response keeps the stored name. A stored name that is not a
// valid DNS name is reported, never looked up.
func TestDNSCheck_IDNLooksUpALabels(t *testing.T) {
	gdb := openDNSCheckDB(t)

	t.Run("unicode name", func(t *testing.T) {
		tx := gdb.Begin()
		t.Cleanup(func() { tx.Rollback() })

		n := time.Now().UnixNano()
		stored := fmt.Sprintf("Bücher.D%d.Test", n)
		ascii := fmt.Sprintf("xn--bcher-kva.d%d.test", n)
		domain := models.Domain{Name: stored, DKIMSelector: "rm2026", DKIMPrivateKey: dnsCheckKeyPEM(t)}
		if err := tx.Create(&domain).Error; err != nil {
			t.Fatalf("create domain: %v", err)
		}
		zone := aCorrectZone(ascii)
		zone.txt["_mta-sts."+ascii] = []string{"v=STSv1; id=1"}
		var queried []string
		h := NewDomainHandler(tx, nil)
		h.SetMailServer(checkMailHost, "", dns.ClientPorts{Submission: 465, IMAP: 993, POP3: 995})
		zone.install(h)
		var mu sync.Mutex
		lookupTXT := h.lookupTXT
		h.lookupTXT = func(ctx context.Context, name string) ([]string, error) {
			mu.Lock()
			queried = append(queried, name)
			mu.Unlock()
			return lookupTXT(ctx, name)
		}
		lookupSRV := h.lookupSRV
		h.lookupSRV = func(ctx context.Context, name string) ([]*net.SRV, error) {
			mu.Lock()
			queried = append(queried, name)
			mu.Unlock()
			return lookupSRV(ctx, name)
		}

		resp := runDNSCheck(t, h, domain.ID)
		for _, q := range queried {
			if q != ascii && !strings.HasSuffix(q, "."+ascii) {
				t.Errorf("looked up %q, want a name under %q", q, ascii)
			}
		}
		if resp.Domain != stored {
			t.Errorf("domain = %q, want the stored %q", resp.Domain, stored)
		}
		if len(resp.Records) != 16 {
			t.Fatalf("got %d records (%+v), want 16", len(resp.Records), resp.Records)
		}
		for _, rec := range resp.Records {
			if rec.Type == "A" || rec.Type == "PTR" {
				continue // the mail host's, not the domain's
			}
			if rec.Name != ascii && !strings.HasSuffix(rec.Name, "."+ascii) {
				t.Errorf("%s check = %s, want a name under %s", rec.Type, rec.Name, ascii)
			}
			if rec.Type != "MTA-STS" && rec.Status != "ok" {
				t.Errorf("%s check = %s %s (%s), want ok", rec.Type, rec.Name, rec.Status, rec.Message)
			}
		}
	})

	t.Run("invalid stored name", func(t *testing.T) {
		tx := gdb.Begin()
		t.Cleanup(func() { tx.Rollback() })

		domain := models.Domain{Name: fmt.Sprintf("bad_name-%d.test", time.Now().UnixNano())}
		if err := tx.Create(&domain).Error; err != nil {
			t.Fatalf("create domain: %v", err)
		}
		h := NewDomainHandler(tx, nil)
		h.lookupMX = func(_ context.Context, name string) ([]*net.MX, error) {
			t.Errorf("looked up MX %q for an invalid name", name)
			return nil, nil
		}
		h.lookupTXT = func(_ context.Context, name string) ([]string, error) {
			t.Errorf("looked up TXT %q for an invalid name", name)
			return nil, nil
		}

		resp := runDNSCheck(t, h, domain.ID)
		if len(resp.Records) != 1 || resp.Records[0].Status != "error" || resp.Records[0].Name != domain.Name {
			t.Fatalf("records = %+v, want one error for %s", resp.Records, domain.Name)
		}
		if resp.Summary != "0/1 records verified" {
			t.Errorf("summary = %q", resp.Summary)
		}
	})
}

// openDNSCheckDB has the tables the DNS check reads beside the domain's own.
func openDNSCheckDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := openAuthzTestDB(t)
	if err := migrateModels(gdb, &models.MTASTSPolicy{}, &models.DKIMRotationKey{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return gdb
}

type dnsCheckResponse struct {
	Domain  string `json:"domain"`
	Summary string `json:"summary"`
	Records []struct {
		Type, Name, Status, Message string
	} `json:"records"`
}

func runDNSCheck(t *testing.T, h *DomainHandler, id uint) dnsCheckResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/domains/%d/dns", id), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", fmt.Sprint(id))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	domainRoutes(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data dnsCheckResponse `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Data
}
