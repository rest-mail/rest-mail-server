package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// OSI-7 — the delivery body limit must ACCEPT a body up to the configured
// ceiling (a legitimate max-size message) and REJECT one beyond it (no unbounded
// buffering), enforcing on actual bytes read rather than a trusted header.

// readAllHandler reads the whole request body and reports whether the read hit
// the MaxBytesReader limit.
func readAllHandler(sawErr *bool, gotLen *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		*gotLen = len(b)
		if err != nil {
			*sawErr = true
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestMaxBodyBytes_AcceptsUpToLimit(t *testing.T) {
	const limit = 1024
	var sawErr bool
	var gotLen int
	h := MaxBodyBytes(limit)(readAllHandler(&sawErr, &gotLen))

	body := strings.Repeat("x", limit) // exactly at the limit
	req := httptest.NewRequest(http.MethodPost, "/deliver", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if sawErr {
		t.Fatalf("a body at the limit (%d bytes) must be accepted, but read errored", limit)
	}
	if gotLen != limit {
		t.Errorf("read %d bytes, want the full %d", gotLen, limit)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestMaxBodyBytes_RejectsOverLimit(t *testing.T) {
	const limit = 1024
	var sawErr bool
	var gotLen int
	h := MaxBodyBytes(limit)(readAllHandler(&sawErr, &gotLen))

	// One byte over the limit. A lying (short) Content-Length cannot help: the
	// cap is on actual bytes read.
	body := strings.Repeat("x", limit+1)
	req := httptest.NewRequest(http.MethodPost, "/deliver", strings.NewReader(body))
	req.ContentLength = 1 // deliberately understate — MaxBytesReader ignores it
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !sawErr {
		t.Fatal("a body over the limit must fail the read (unbounded buffering prevented)")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

// TestMaxBodyBytes_ZeroDisables verifies limit <= 0 leaves the body untouched
// (the middleware is a no-op), so it can be safely wired unconditionally.
func TestMaxBodyBytes_ZeroDisables(t *testing.T) {
	var sawErr bool
	var gotLen int
	h := MaxBodyBytes(0)(readAllHandler(&sawErr, &gotLen))

	body := strings.Repeat("x", 100_000)
	req := httptest.NewRequest(http.MethodPost, "/deliver", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if sawErr || gotLen != len(body) {
		t.Fatalf("limit<=0 must not cap the body; sawErr=%v gotLen=%d", sawErr, gotLen)
	}
}
