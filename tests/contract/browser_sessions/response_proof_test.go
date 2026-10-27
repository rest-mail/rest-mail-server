package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserResponseFraming(t *testing.T) {
	for _, tc := range []struct {
		name, media, body string
		status            int
		method            string
		wantError         bool
	}{
		{name: "object", media: "application/json; charset=utf-8", body: `{"data":{"id":7}}`, status: 200},
		{name: "suffix", media: "application/problem+json", body: `{"error":{"code":"refused"}}`, status: 400},
		{name: "whitespace", media: "application/json", body: "{\"data\":{}}\n\t", status: 200},
		{name: "truncated", media: "application/json", body: `{"data":`, status: 200, wantError: true},
		{name: "trailing object", media: "application/json", body: `{"data":{}}{"access_token":"response-secret"}`, status: 200, wantError: true},
		{name: "trailing garbage", media: "application/json", body: `{"data":{}}response-secret`, status: 200, wantError: true},
		{name: "empty JSON", media: "application/json", status: 200, wantError: true},
		{name: "null", media: "application/json", body: `null`, status: 200, wantError: true},
		{name: "array", media: "application/json", body: `[]`, status: 200, wantError: true},
		{name: "too large", media: "application/json", body: `{"data":"` + strings.Repeat("x", 1<<20) + `"}`, status: 200, wantError: true},
		{name: "invalid media", media: "application/json; response-secret", body: `{}`, status: 200, wantError: true},
		{name: "plain error", media: "text/plain", body: "Not found", status: 404},
		{name: "empty no content", media: "application/json", status: 204},
		{name: "empty HEAD", media: "application/json", status: 200, method: http.MethodHead},
		{name: "empty not modified", media: "application/json", status: 304},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.media}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: &http.Request{Method: tc.method}}
			_, err := readResponse(response)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, want error=%t", err, tc.wantError)
			}
			if err != nil && strings.Contains(err.Error(), "response-secret") {
				t.Fatal("response value leaked")
			}
		})
	}
}

type responseReadFailure struct{ closed bool }

func (*responseReadFailure) Read([]byte) (int, error) { return 0, errors.New("response-secret") }
func (r *responseReadFailure) Close() error           { r.closed = true; return nil }
func TestBrowserResponseReadFailureClosesBody(t *testing.T) {
	body := &responseReadFailure{}
	_, err := readResponse(&http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: body})
	if err == nil || !body.closed || strings.Contains(err.Error(), "response-secret") {
		t.Fatal("failed response must fail and close")
	}
}

// A safe request must ignore every invalid CSRF variant, not only an absent header.
func TestBrowserSafeCSRFFaultsFailTheGate(t *testing.T) {
	for _, fault := range []string{"mismatched", "missing cookie"} {
		t.Run(fault, func(t *testing.T) {
			base := browserFixture(t, true, true)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/api/v1/messages/contract-invalid-id" {
					c, e := r.Cookie("restmail_csrf")
					h := r.Header.Get("X-CSRF-Token")
					if (fault == "mismatched" && e == nil && h != "" && c.Value != h) || (fault == "missing cookie" && e != nil && h != "") {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(403)
						_, _ = io.WriteString(w, `{"error":{"code":"csrf_failed"}}`)
						return
					}
				}
				base.Config.Handler.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			result, err := prove(server.URL, fixtureDescription(), principal{Address: "admin@restmail.test", Password: "fixture"})
			if err == nil || result.Passed {
				t.Fatal("safe-method CSRF fault was accepted")
			}
			if !strings.Contains(err.Error(), "getMessage") {
				t.Fatalf("failed outside safe route: %v", err)
			}
		})
	}
}

func TestBrowserResponseFaultsFailTheGate(t *testing.T) {
	for _, body := range []string{`{"data":[]}garbage`, `{"data":[]} {"access_token":"response-secret"}`, `{"data":"` + strings.Repeat("x", 1<<20) + `"}`} {
		base := browserFixture(t, true, true)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v1/messages/contract-invalid-id" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
				return
			}
			base.Config.Handler.ServeHTTP(w, r)
		}))
		t.Cleanup(server.Close)
		result, err := prove(server.URL, fixtureDescription(), principal{Address: "admin@restmail.test", Password: "fixture"})
		if err == nil || result.Passed {
			t.Fatal("invalid response framing was accepted")
		}
		if strings.Contains(err.Error(), "response-secret") {
			t.Fatal("response value leaked")
		}
	}
}

func TestBrowserErrorCodeCannotLeakResponseSecret(t *testing.T) {
	base := browserFixture(t, true, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/accounts/7/folders" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "response-secret"}})
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	result, err := prove(server.URL, fixtureDescription(), principal{Address: "admin@restmail.test", Password: "fixture"})
	if err == nil || result.Passed {
		t.Fatal("wrong status accepted")
	}
	if strings.Contains(err.Error(), "response-secret") {
		t.Fatal("response value leaked through error code")
	}
}

func TestBrowserResponseBoundaryAndEventStream(t *testing.T) {
	const prefix, suffix = `{"data":"`, `"}`
	body := prefix + strings.Repeat("x", (1<<20)-len(prefix)-len(suffix)) + suffix
	response := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	if _, err := readResponse(response); err != nil {
		t.Fatalf("exact response allowance refused: %v", err)
	}
	stream := &responseReadFailure{}
	if _, err := readResponse(&http.Response{Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, Body: stream}); err != nil || !stream.closed {
		t.Fatal("stream must close without reading")
	}
}

func TestBrowserSafeErrorCodeCannotLeakResponseSecret(t *testing.T) {
	base := browserFixture(t, true, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/messages/contract-invalid-id" {
			code := "response-secret-control"
			if r.Header.Get("X-CSRF-Token") == "" {
				code = "response-secret-variant"
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	result, err := prove(server.URL, fixtureDescription(), principal{Address: "admin@restmail.test", Password: "fixture"})
	if err == nil || result.Passed {
		t.Fatal("different safe error codes accepted")
	}
	if strings.Contains(err.Error(), "response-secret") {
		t.Fatal("safe comparison leaked response code")
	}
}
