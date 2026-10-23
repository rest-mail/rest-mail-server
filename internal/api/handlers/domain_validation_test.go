package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

// TestDomainCreate_NameValidation proves the domain create endpoint rejects
// non-hostname input before it is inserted (issue #202: domain name validation).
// A stored domain name flows into DNS lookups, MTA-STS host matching, sieve
// redirect allowlists and outbound routing, so bogus/oversized values must be
// refused with a client error while a valid internationalized domain is accepted
// and normalized to its ASCII (punycode) form.
func TestDomainCreate_NameValidation(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.Pipeline{}); err != nil {
		t.Skipf("domain validation test skipped: migrate Pipeline failed (%v)", err)
	}

	create := func(t *testing.T, name string) *httptest.ResponseRecorder {
		t.Helper()
		tx := gdb.Begin()
		t.Cleanup(func() { tx.Rollback() })
		h := NewDomainHandler(tx, nil)
		body, _ := json.Marshal(map[string]string{"name": name})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/domains", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		domainRoutes(h).ServeHTTP(rr, req)
		return rr
	}

	// A name that is not a hostname is the 422 validation failure on name, like
	// every other refused field of the body (#645).
	refusedName := func(t *testing.T, rr *httptest.ResponseRecorder) {
		t.Helper()
		var resp struct {
			Error struct {
				Code    string `json:"code"`
				Details struct {
					Fields map[string]string `json:"fields"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if rr.Code != http.StatusUnprocessableEntity || resp.Error.Code != "validation_failed" ||
			!strings.HasPrefix(resp.Error.Details.Fields["name"], "Invalid domain name: ") {
			t.Fatalf("status = %d, want 422 validation_failed on name (body %s)", rr.Code, rr.Body.String())
		}
	}

	t.Run("bogus name rejected", func(t *testing.T) {
		rr := create(t, "not a valid domain!!")
		refusedName(t, rr)
	})

	t.Run("oversized name rejected", func(t *testing.T) {
		rr := create(t, strings.Repeat("a", 300)+".test")
		refusedName(t, rr)
	})

	t.Run("single-label host rejected", func(t *testing.T) {
		rr := create(t, "localhost")
		refusedName(t, rr)
	})

	t.Run("valid IDN accepted and normalized", func(t *testing.T) {
		rr := create(t, "münchen.test")
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body %s)", rr.Code, rr.Body.String())
		}
		var resp struct {
			Data models.Domain `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := resp.Data.Name
		if got != strings.ToLower(got) {
			t.Errorf("stored name %q is not lowercased", got)
		}
		for _, c := range got {
			if c >= 0x80 {
				t.Fatalf("stored name %q is not ASCII-normalized (punycode) — got a non-ASCII rune", got)
			}
		}
		if !strings.HasPrefix(got, "xn--") {
			t.Errorf("stored name %q, want the IDN encoded as an xn-- A-label", got)
		}
	})

	t.Run("plain ASCII domain accepted", func(t *testing.T) {
		rr := create(t, "Example.COM")
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body %s)", rr.Code, rr.Body.String())
		}
		var resp struct {
			Data models.Domain `json:"data"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &resp)
		if resp.Data.Name != "example.com" {
			t.Errorf("stored name = %q, want example.com (lowercased)", resp.Data.Name)
		}
	})
}
