package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// buildTLSRPTBody marshals a syntactically valid RFC 8460 report with the given
// number of policy entries. The policy-domain is left empty so the request never
// depends on a database: the pre-fix handler skips every entry (unknown domain)
// and returns 422, while the fixed handler rejects on the policy-count cap first.
func buildTLSRPTBody(t *testing.T, numPolicies int) []byte {
	t.Helper()
	policies := make([]map[string]any, numPolicies)
	for i := range policies {
		policies[i] = map[string]any{
			"policy":  map[string]any{"policy-type": "no-policy-found"},
			"summary": map[string]any{"total-successful-session-count": 1, "total-failure-session-count": 0},
		}
	}
	report := map[string]any{
		"organization-name": "Flooder Inc",
		"date-range": map[string]string{
			"start-datetime": "2026-01-01T00:00:00Z",
			"end-datetime":   "2026-01-02T00:00:00Z",
		},
		"policies": policies,
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return b
}

// TestReceiveReport_OversizedBodyRejected proves an unauthenticated caller cannot
// stream an unbounded body into the TLS-RPT endpoint: an over-cap body must be
// rejected outright (413), not silently truncated and half-parsed. The handler is
// given a nil DB because a correct handler rejects before any storage.
func TestReceiveReport_OversizedBodyRejected(t *testing.T) {
	h := NewTLSReportHandler(nil)

	// ~2 MiB body — larger than any legitimate aggregate report and larger than
	// the accepted-body cap.
	oversized := []byte(`{"organization-name":"` + strings.Repeat("a", 2<<20) + `"}`)

	req := httptest.NewRequest(http.MethodPost, "/.well-known/smtp-tlsrpt", bytes.NewReader(oversized))
	rr := httptest.NewRecorder()

	callReceive(t, h, rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: status = %d, want %d (must be rejected, not truncated-and-stored)",
			rr.Code, http.StatusRequestEntityTooLarge)
	}
}

// TestReceiveReport_TooManyPoliciesRejected proves the storage write-amplification
// vector is closed: a report carrying far more policy entries than the cap must be
// rejected as a whole (413) BEFORE any row is created, rather than fanning out into
// one stored row per entry. The handler is given a nil DB so reaching the storage
// loop is itself a failure.
func TestReceiveReport_TooManyPoliciesRejected(t *testing.T) {
	h := NewTLSReportHandler(nil)

	body := buildTLSRPTBody(t, 500) // far above any legitimate report

	req := httptest.NewRequest(http.MethodPost, "/.well-known/smtp-tlsrpt", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	callReceive(t, h, rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap policy count: status = %d, want %d (report must be rejected before storage)",
			rr.Code, http.StatusRequestEntityTooLarge)
	}
}

// callReceive invokes the handler and converts a panic (e.g. the pre-fix code
// dereferencing the nil DB while trying to store) into a test failure, so the
// assertion is always made against a real HTTP status.
func callReceive(t *testing.T, h *TLSReportHandler, rr *httptest.ResponseRecorder, req *http.Request) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("handler reached storage instead of rejecting the report (panic: %v)", p)
		}
	}()
	h.ReceiveReport(rr, req)
}

// makeReport builds a tlsRPTReport value with n policy entries for the given
// policy-domain, used by the pure validation tests.
func makeReport(policyDomain string, n int) *tlsRPTReport {
	policies := make([]tlsRPTPolicy, n)
	for i := range policies {
		policies[i] = tlsRPTPolicy{Policy: tlsRPTPolicyDesc{PolicyType: "no-policy-found", PolicyDomain: policyDomain}}
	}
	return &tlsRPTReport{
		OrganizationName: "Reporter Inc",
		DateRange:        tlsRPTRange{StartDatetime: "2026-01-01T00:00:00Z", EndDatetime: "2026-01-02T00:00:00Z"},
		Policies:         policies,
	}
}

// TestValidateTLSRPTReport exercises the pure validator: required fields, date
// parsing, the non-empty rule, and — the write-amplification guard — the policy
// count cap at its exact boundary.
func TestValidateTLSRPTReport(t *testing.T) {
	t.Run("valid report passes and returns the parsed range", func(t *testing.T) {
		start, end, verr := validateTLSRPTReport(makeReport("example.test", 3))
		if verr != nil {
			t.Fatalf("valid report rejected: %+v", verr)
		}
		if start.IsZero() || end.IsZero() || !end.After(start) {
			t.Fatalf("date range = %v..%v, want parsed start<end", start, end)
		}
	})

	t.Run("missing organization-name is a field error", func(t *testing.T) {
		r := makeReport("example.test", 1)
		r.OrganizationName = ""
		_, _, verr := validateTLSRPTReport(r)
		if verr == nil || verr.fields["organization-name"] == "" {
			t.Fatalf("verr = %+v, want organization-name field error", verr)
		}
	})

	t.Run("invalid start datetime is a field error", func(t *testing.T) {
		r := makeReport("example.test", 1)
		r.DateRange.StartDatetime = "not-a-date"
		_, _, verr := validateTLSRPTReport(r)
		if verr == nil || verr.fields["date-range.start-datetime"] == "" {
			t.Fatalf("verr = %+v, want start-datetime field error", verr)
		}
	})

	t.Run("empty policies is a field error", func(t *testing.T) {
		_, _, verr := validateTLSRPTReport(makeReport("example.test", 0))
		if verr == nil || verr.fields["policies"] == "" {
			t.Fatalf("verr = %+v, want policies field error", verr)
		}
	})

	t.Run("exactly the cap is accepted", func(t *testing.T) {
		if _, _, verr := validateTLSRPTReport(makeReport("example.test", tlsRPTMaxPolicies)); verr != nil {
			t.Fatalf("report at the cap rejected: %+v", verr)
		}
	})

	t.Run("one over the cap is rejected as too_many_policies", func(t *testing.T) {
		_, _, verr := validateTLSRPTReport(makeReport("example.test", tlsRPTMaxPolicies+1))
		if verr == nil {
			t.Fatal("over-cap report accepted, want rejection")
		}
		if verr.status != http.StatusRequestEntityTooLarge || verr.code != "too_many_policies" {
			t.Fatalf("verr = %+v, want status 413 code too_many_policies", verr)
		}
	})
}

// buildTLSRPTBodyForDomain marshals a valid report whose policy entries all name
// policyDomain, so the storage path (domain lookup + row creation) is exercised.
func buildTLSRPTBodyForDomain(t *testing.T, policyDomain string, numPolicies int) []byte {
	t.Helper()
	b, err := json.Marshal(makeReport(policyDomain, numPolicies))
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return b
}

// openTLSRPTTestDB connects to the unit-test Postgres and migrates the tables the
// ingestion path touches. It skips (never fails) when no database is reachable,
// matching the repo's depless-local / DB-in-CI convention.
func openTLSRPTTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: envIntOr("DB_PORT", 5432),
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("TLS-RPT DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.TLSReport{}); err != nil {
		t.Skipf("TLS-RPT DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestReceiveReport_Storage proves the end-to-end storage guarantees against a real
// database: reports for unknown domains store nothing, an over-cap report is
// rejected without adding a single row (no write amplification), and a well-formed
// report for a hosted domain stores exactly its policy entries.
func TestReceiveReport_Storage(t *testing.T) {
	gdb := openTLSRPTTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	suffix := time.Now().UnixNano()
	known := fmt.Sprintf("known-%d.example", suffix)
	unknown := fmt.Sprintf("unknown-%d.example", suffix)

	dom := models.Domain{Name: known, Active: true}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}

	countRows := func() int64 {
		var n int64
		tx.Model(&models.TLSReport{}).Where("domain_id = ?", dom.ID).Count(&n)
		return n
	}

	h := NewTLSReportHandler(tx)

	post := func(body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/.well-known/smtp-tlsrpt", bytes.NewReader(body))
		req.RemoteAddr = "198.51.100.7:5555"
		rr := httptest.NewRecorder()
		h.ReceiveReport(rr, req)
		return rr
	}

	// Unknown domain: nothing is stored, and the caller is told none matched.
	if rr := post(buildTLSRPTBodyForDomain(t, unknown, 3)); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown-domain report: status = %d, want 422", rr.Code)
	}
	if n := countRows(); n != 0 {
		t.Fatalf("unknown-domain report stored %d rows, want 0", n)
	}

	// Over-cap report for a hosted domain: rejected outright, zero rows created —
	// the write-amplification vector is closed even for a domain we DO host.
	if rr := post(buildTLSRPTBodyForDomain(t, known, tlsRPTMaxPolicies+50)); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap report: status = %d, want 413", rr.Code)
	}
	if n := countRows(); n != 0 {
		t.Fatalf("over-cap report stored %d rows, want 0 (must reject before storage)", n)
	}

	// Well-formed report for a hosted domain: stores exactly its policy entries.
	if rr := post(buildTLSRPTBodyForDomain(t, known, 3)); rr.Code != http.StatusCreated {
		t.Fatalf("valid report: status = %d, want 201", rr.Code)
	}
	if n := countRows(); n != 3 {
		t.Fatalf("valid report stored %d rows, want 3", n)
	}
}

// TestReceiveReport_RateLimited proves the per-client-IP throttle wired onto the
// ingestion route (built exactly as routes.go builds it) caps an unauthenticated
// flood: past the burst, further posts from one source are answered 429 rather
// than stored.
func TestReceiveReport_RateLimited(t *testing.T) {
	gdb := openTLSRPTTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	dom := models.Domain{Name: fmt.Sprintf("rl-%d.example", time.Now().UnixNano()), Active: true}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}

	h := NewTLSReportHandler(tx)
	const burst = 3
	throttle := middleware.RateLimit(middleware.RateLimitConfig{RPS: 1, Burst: burst})
	handler := throttle(http.HandlerFunc(h.ReceiveReport))

	body := buildTLSRPTBodyForDomain(t, dom.Name, 1)
	var accepted, throttled int
	for i := 0; i < burst+5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/.well-known/smtp-tlsrpt", bytes.NewReader(body))
		req.RemoteAddr = "203.0.113.200:6000"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		switch rr.Code {
		case http.StatusTooManyRequests:
			throttled++
		default:
			accepted++
		}
	}
	if throttled == 0 {
		t.Fatalf("flood of %d posts from one IP was never throttled (accepted=%d)", burst+5, accepted)
	}
	if accepted > burst {
		t.Fatalf("accepted=%d exceeds burst=%d; throttle not bounding the flood", accepted, burst)
	}
}
