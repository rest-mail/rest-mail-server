package apiclient

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/requestid"
)

// Every call a connection's client makes carries the connection's ID, on the
// internal (machine and session) routes and the public ones alike (#1580).
func TestWithRequestID_SendsTheHeaderOnEveryCall(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = r.Header.Get(requestid.Header)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	base := New(srv.URL)
	c := base.WithRequestID("conn-42")
	_, _ = c.CheckMailbox("someone@example.test")
	_, _ = c.GetQuota("rmgs_token", 1)
	_ = c.CloseSession("rmgs_token")
	_ = c.SendMessage("jwt-token", &SendRequest{})

	if len(seen) != 4 {
		t.Fatalf("calls seen = %v, want 4", seen)
	}
	for call, id := range seen {
		if id != "conn-42" {
			t.Errorf("%s sent X-Request-Id %q, want conn-42", call, id)
		}
	}

	// The client it was copied from sends none.
	seen = map[string]string{}
	_, _ = base.CheckMailbox("someone@example.test")
	for call, id := range seen {
		if id != "" {
			t.Errorf("%s from the shared client sent X-Request-Id %q", call, id)
		}
	}
}
