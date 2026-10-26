package handlers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
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
// vector is closed: a report carrying far more policy entries than the cap (the
// report type's maxItems) must be refused as a whole (422) BEFORE any row is
// created, rather than fanning out into one stored row per entry. The handler is
// given a nil DB so reaching the storage loop is itself a failure.
func TestReceiveReport_TooManyPoliciesRejected(t *testing.T) {
	h := NewTLSReportHandler(nil)

	body := buildTLSRPTBody(t, 500) // far above any legitimate report

	req := httptest.NewRequest(http.MethodPost, "/.well-known/smtp-tlsrpt", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	callReceive(t, h, rr, req)

	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"policies"`) {
		t.Fatalf("over-cap policy count: %d %s, want 422 on policies (report must be rejected before storage)",
			rr.Code, rr.Body)
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
	tlsReportRoutes(h, passThrough).ServeHTTP(rr, req)
}

// tlsReportRoutes is the ingestion route as routes.go registers it, behind throttle.
func tlsReportRoutes(h *TLSReportHandler, throttle endpoint.Middleware) http.Handler {
	r := chi.NewRouter()
	h.RegisterReceive(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, throttle)
	return r
}

// makeReport builds a report with n policy entries for the given policy-domain.
func makeReport(policyDomain string, n int) *TLSRPTReport {
	policies := make([]TLSRPTPolicy, n)
	for i := range policies {
		policies[i] = TLSRPTPolicy{Policy: TLSRPTPolicyDesc{PolicyType: "no-policy-found", PolicyDomain: policyDomain}}
	}
	return &TLSRPTReport{
		OrganizationName: "Reporter Inc",
		DateRange:        TLSRPTRange{StartDatetime: "2026-01-01T00:00:00Z", EndDatetime: "2026-01-02T00:00:00Z"},
		Policies:         policies,
	}
}

// reportWith is a report of two policy entries with no policy-domain, so a report
// that passes every check is answered 422 no_matching_domains without a database,
// with change applied to it as JSON.
func reportWith(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal(buildTLSRPTBodyForDomain(t, "", 2), &report); err != nil {
		t.Fatal(err)
	}
	change(report)
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestReceiveReport_Refused exercises the report type's rules through the route:
// required fields, date parsing, the non-empty rule, the shape of failure-details
// and — the write-amplification guard — the policy count cap at its exact boundary.
// A fresh scratch database permits the atomic ingestion transaction. Reports
// without policy domains still answer no_matching_domains without storing rows.
func TestReceiveReport_Refused(t *testing.T) {
	h := NewTLSReportHandler(openAliasUpdateScratchDB(t))
	post := func(body []byte) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		callReceive(t, h, rr, httptest.NewRequest(http.MethodPost, "/.well-known/smtp-tlsrpt", bytes.NewReader(body)))
		return rr
	}
	passed := func(t *testing.T, rr *httptest.ResponseRecorder) {
		t.Helper()
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "no_matching_domains") {
			t.Fatalf("got %d %s, want the report to pass its checks", rr.Code, rr.Body)
		}
	}
	refused := func(t *testing.T, rr *httptest.ResponseRecorder, field, message string) {
		t.Helper()
		var got struct {
			Error struct {
				Code    string `json:"code"`
				Details struct {
					Fields map[string]string `json:"fields"`
				} `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &got)
		if rr.Code != http.StatusUnprocessableEntity || got.Error.Code != "validation_failed" ||
			(message != "" && got.Error.Details.Fields[field] != message) || (message == "" && got.Error.Details.Fields[field] == "") {
			t.Fatalf("got %d %s, want 422 on %s %q", rr.Code, rr.Body, field, message)
		}
	}

	t.Run("a valid report passes", func(t *testing.T) { passed(t, post(reportWith(t, func(map[string]any) {}))) })
	t.Run("a field of the reporter's own is kept, not refused", func(t *testing.T) {
		passed(t, post(reportWith(t, func(r map[string]any) { r["x-reporter"] = map[string]any{"v": 1} })))
	})
	t.Run("missing organization-name", func(t *testing.T) {
		refused(t, post(reportWith(t, func(r map[string]any) { delete(r, "organization-name") })), "organization-name", "Required field")
	})
	t.Run("empty organization-name", func(t *testing.T) {
		refused(t, post(reportWith(t, func(r map[string]any) { r["organization-name"] = "" })), "organization-name", "Required field")
	})
	t.Run("invalid start datetime", func(t *testing.T) {
		refused(t, post(reportWith(t, func(r map[string]any) {
			r["date-range"].(map[string]any)["start-datetime"] = "not-a-date"
		})), "date-range.start-datetime", "Invalid RFC3339 datetime")
	})
	t.Run("empty policies", func(t *testing.T) {
		refused(t, post(reportWith(t, func(r map[string]any) { r["policies"] = []any{} })), "policies", "Must hold 1 to 100 policy entries")
	})
	t.Run("failure-details must be a list of objects", func(t *testing.T) {
		for _, raw := range []string{`"vertrag-not-an-array"`, `{}`, `[1]`, `["x"]`, `[null]`} {
			body := reportWith(t, func(r map[string]any) {
				r["policies"].([]any)[1].(map[string]any)["failure-details"] = json.RawMessage(raw)
			})
			rr := post(body)
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "policies[1].failure-details") {
				t.Errorf("failure-details %s: got %d %s, want 422 on policies[1].failure-details", raw, rr.Code, rr.Body)
			}
		}
		for _, raw := range []string{`[]`, `[{"result-type":"certificate-expired","failed-session-count":1}]`} {
			passed(t, post(reportWith(t, func(r map[string]any) {
				r["policies"].([]any)[0].(map[string]any)["failure-details"] = json.RawMessage(raw)
			})))
		}
	})
	t.Run("exactly the cap passes", func(t *testing.T) { passed(t, post(buildTLSRPTBody(t, 100))) })
	t.Run("one over the cap is refused", func(t *testing.T) {
		refused(t, post(buildTLSRPTBody(t, 101)), "policies", "Must hold 1 to 100 policy entries")
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
	gdb, err := connectTestDB()
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
		tlsReportRoutes(h, passThrough).ServeHTTP(rr, req)
		return rr
	}

	// Unknown domain: nothing is stored, and the caller is told none matched.
	if rr := post(buildTLSRPTBodyForDomain(t, unknown, 3)); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown-domain report: status = %d, want 422", rr.Code)
	}
	if n := countRows(); n != 0 {
		t.Fatalf("unknown-domain report stored %d rows, want 0", n)
	}

	// Over-cap report for a hosted domain: refused outright, zero rows created —
	// the write-amplification vector is closed even for a domain we DO host.
	if rr := post(buildTLSRPTBodyForDomain(t, known, 150)); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over-cap report: status = %d, want 422", rr.Code)
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

	// The same report gzip-compressed, as RFC 8460 asks reporters to send it, is
	// stored the same way, its raw report kept as the JSON it inflates to; so is a
	// report under application/tlsrpt+gzip, and a field of the reporter's own is
	// kept in the raw report.
	plain := buildTLSRPTBodyForDomain(t, known, 2)
	if rr := post(gzipped(t, plain)); rr.Code != http.StatusCreated {
		t.Fatalf("gzip report: status = %d, want 201 (%s)", rr.Code, rr.Body)
	}
	if n := countRows(); n != 5 {
		t.Fatalf("gzip report stored %d rows in all, want 5", n)
	}
	var last models.TLSReport
	if err := tx.Where("domain_id = ?", dom.ID).Order("id DESC").First(&last).Error; err != nil || last.RawReport != string(plain) {
		t.Fatalf("gzip report kept %q (%v), want the inflated JSON", last.RawReport, err)
	}
	extended := bytes.Replace(plain, []byte(`{"organization-name"`), []byte(`{"x-reporter":{"build":7},"organization-name"`), 1)
	req := httptest.NewRequest(http.MethodPost, "/.well-known/smtp-tlsrpt", bytes.NewReader(gzipped(t, extended)))
	req.Header.Set("Content-Type", "application/tlsrpt+gzip")
	rr := httptest.NewRecorder()
	tlsReportRoutes(h, passThrough).ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("application/tlsrpt+gzip report: %d %s", rr.Code, rr.Body)
	}
	var kept models.TLSReport
	if err := tx.Where("domain_id = ?", dom.ID).Order("id DESC").First(&kept).Error; err != nil || kept.RawReport != string(extended) {
		t.Fatalf("the reporter's own field was not kept: %q (%v)", kept.RawReport, err)
	}
}

// gzipped is body compressed as a reporter compresses it.
func gzipped(t *testing.T, body []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// A gzip-compressed report is inflated before it is checked, however it is labelled, and
// held to the same cap inflated; one that only claims to be compressed is refused. The
// handler has no database: each is answered before any storage.
func TestReceiveReport_GzipCompressed(t *testing.T) {
	h := NewTLSReportHandler(nil)
	tooMany := gzipped(t, buildTLSRPTBody(t, 101))
	bomb := gzipped(t, []byte(`{"organization-name":"`+strings.Repeat("a", int(tlsRPTMaxBodyBytes))+`"}`))
	if int64(len(bomb)) >= tlsRPTMaxBodyBytes {
		t.Fatalf("the compressed body is %d bytes: it must be under the cap to test the inflated one", len(bomb))
	}
	for _, tc := range []struct {
		name, contentType, encoding string
		body                        []byte
		status                      int
		code                        string
	}{
		{"labelled tlsrpt+gzip", "application/tlsrpt+gzip", "", tooMany, http.StatusUnprocessableEntity, "validation_failed"},
		{"labelled by encoding", "application/tlsrpt+json", "gzip", tooMany, http.StatusUnprocessableEntity, "validation_failed"},
		{"unlabelled", "application/json", "", tooMany, http.StatusUnprocessableEntity, "validation_failed"},
		{"no media type", "", "", tooMany, http.StatusUnprocessableEntity, "validation_failed"},
		{"inflates past the cap", "application/tlsrpt+gzip", "", bomb, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"inflates past the cap, by encoding", "application/json", "gzip", bomb, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"claims gzip, is not", "application/tlsrpt+gzip", "", buildTLSRPTBody(t, 1), http.StatusBadRequest, "bad_request"},
		{"claims gzip by encoding, is not", "application/json", "gzip", buildTLSRPTBody(t, 1), http.StatusBadRequest, "bad_request"},
		{"another media type", "text/plain", "", buildTLSRPTBody(t, 1), http.StatusUnsupportedMediaType, "unsupported_media_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/.well-known/smtp-tlsrpt", bytes.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			if tc.encoding != "" {
				req.Header.Set("Content-Encoding", tc.encoding)
			}
			rr := httptest.NewRecorder()
			callReceive(t, h, rr, req)
			if rr.Code != tc.status || !strings.Contains(rr.Body.String(), `"`+tc.code+`"`) {
				t.Fatalf("want %d %s, got %d %s", tc.status, tc.code, rr.Code, rr.Body)
			}
		})
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
	handler := tlsReportRoutes(h, endpoint.Middleware{Wrap: throttle})

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
