package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

func TestRESTMAILDiscoveryInvalidDomain(t *testing.T) {
	h := NewRestmailDiscoveryHandler(nil, "mx.example.test")
	for _, query := range []string{"", "domain=", "domain=alice%40example.test", "domain=example.test%2Fpath", "domain=example.test&domain=other.test"} {
		t.Run(query, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.Discover(rr, httptest.NewRequest(http.MethodGet, "/.well-known/restmail?"+query, nil))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestRESTMAILDiscoveryInvalidHostname(t *testing.T) {
	for _, host := range []string{"", "localhost", "https://mx.example.test", "mx.example.test/path", "mx.example.test@evil.test"} {
		t.Run(host, func(t *testing.T) {
			rr := httptest.NewRecorder()
			NewRestmailDiscoveryHandler(nil, host).Discover(rr, httptest.NewRequest(http.MethodGet, "/.well-known/restmail?domain=example.test", nil))
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestRESTMAILDiscoveryHostedDomain(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("discovery DB test: %v", err)
	}
	if err := db.AutoMigrate(&models.Domain{}); err != nil {
		t.Fatalf("migrate domains: %v", err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })

	// No mailboxes exist for these domains. Discovery is about the hosted
	// domain alone, independent of a mailbox's existence or server type.
	for _, domain := range []models.Domain{
		{Name: "discovery-traditional.test", Active: true, ServerType: "traditional"},
		{Name: "discovery-restmail.test", Active: true, ServerType: "restmail"},
		{Name: "discovery-inactive.test", Active: false},
		{Name: "xn--mnchen-3ya.test", Active: true},
	} {
		if err := tx.Create(&domain).Error; err != nil {
			t.Fatal(err)
		}
	}
	h := NewRestmailDiscoveryHandler(tx, "MX.Example.Test.")
	for _, tc := range []struct {
		domain string
		status int
	}{
		{"discovery-traditional.test", http.StatusOK},
		{"DISCOVERY-RESTMAIL.TEST.", http.StatusOK},
		{"münchen.test", http.StatusOK},
		{"discovery-inactive.test", http.StatusNotFound},
		{"discovery-unknown.test", http.StatusNotFound},
	} {
		t.Run(tc.domain, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/.well-known/restmail?domain="+url.QueryEscape(tc.domain)+"&address=nonexistent%40example.test", nil)
			req.Host = "attacker.test"
			req.Header.Set("X-Forwarded-Host", "attacker.test")
			req.Header.Set("Forwarded", "host=attacker.test;proto=http")
			rr := httptest.NewRecorder()
			h.Discover(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if rr.Header().Get("Cache-Control") != "no-store" {
				t.Error("discovery must not leave stale domain status in HTTP caches")
			}
			if tc.status == http.StatusOK {
				var result map[string]string
				if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result) != 1 || result["endpoint"] != "https://mx.example.test/restmail" {
					t.Fatalf("unexpected discovery document: %s", rr.Body.String())
				}
			} else if strings.Contains(rr.Body.String(), "endpoint") {
				t.Fatal("unhosted domain received a delivery endpoint")
			}
		})
	}
}
