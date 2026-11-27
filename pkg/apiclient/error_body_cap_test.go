package apiclient

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

func TestErrorResponseBodyPreserved(t *testing.T) {
	const refusal = `{"error":{"code":"access_restricted","message":"This server is not accepting sign-ins for this account right now"}}` + "\n"
	for _, tc := range []struct {
		name string
		body string
	}{
		{"normal", refusal},
		{"exactly 64 KiB", refusal + strings.Repeat(" ", (64<<10)-len(refusal))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, mux := newTestServer(t)
			mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, tc.body)
			})

			_, err := New(srv.URL).Login("user@example.test", "secret", "")
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected *APIError, got %T", err)
			}
			if apiErr.StatusCode != http.StatusForbidden || apiErr.Body != tc.body {
				t.Fatalf("expected unchanged 403 response with %d body bytes, got status %d and %d body bytes", len(tc.body), apiErr.StatusCode, len(apiErr.Body))
			}
			if err.Error() != "API error 403: "+tc.body {
				t.Fatal("error text changed")
			}
			if IsAuthRejection(err) {
				t.Fatal("access_restricted must not count as a credential rejection")
			}
		})
	}
}

func TestOversizedErrorResponseBodyAllocation(t *testing.T) {
	const bodySize = 64 << 20
	const allocationBudget = 4 << 20
	// Stream the response from a small buffer so server allocations do not
	// dominate the process-wide measurement. The response has no Content-Length.
	chunk := bytes.Repeat([]byte("x"), 32<<10)
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		for range bodySize / len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	client := New(srv.URL)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := client.Login("user@example.test", "secret", "")
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("64 MiB error response allocated %d bytes", allocated)
	if allocated >= allocationBudget {
		t.Fatalf("allocated %d bytes, want less than %d", allocated, allocationBudget)
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected *APIError with status 502, got %T", err)
	}
	if !strings.HasPrefix(err.Error(), "API error 502: ") || !strings.HasSuffix(apiErr.Body, "[response body truncated]") {
		t.Fatal("expected error text to include the status and a truncation notice")
	}
	if prefix, _ := strings.CutSuffix(apiErr.Body, "\n[response body truncated]"); len(prefix) != 64<<10 {
		t.Fatalf("retained %d body bytes, want 64 KiB", len(prefix))
	}
}
