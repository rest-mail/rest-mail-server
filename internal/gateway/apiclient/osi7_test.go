package apiclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// OSI-7 — the API client's message-carrying calls must be bounded by a
// size-aware deadline, not a fixed 30s that strands a large-but-permitted
// message, while never being disabled (no infinite wait → no slowloris).

// TestWithMessageDeadline_AppliesToBothClients verifies the option overrides the
// timeout on BOTH the public client (GetRawMessage downloads) and the internal
// client (DeliverMessage uploads), regardless of option order relative to
// WithInternalMTLS.
func TestWithMessageDeadline_AppliesToBothClients(t *testing.T) {
	deadline := 158 * time.Second

	t.Run("no mTLS: shared client gets the deadline", func(t *testing.T) {
		c := New("http://api:8080", WithMessageDeadline(deadline))
		if c.httpClient.Timeout != deadline {
			t.Errorf("public client Timeout = %v, want %v", c.httpClient.Timeout, deadline)
		}
		if c.internalHTTPClient.Timeout != deadline {
			t.Errorf("internal client Timeout = %v, want %v", c.internalHTTPClient.Timeout, deadline)
		}
	})

	t.Run("with mTLS: separate internal client also gets the deadline", func(t *testing.T) {
		// Option order deliberately puts WithMessageDeadline BEFORE WithInternalMTLS
		// to prove New applies the deadline after all options run.
		c := New("http://api:8080",
			WithMessageDeadline(deadline),
			WithInternalMTLS("https://api:8443", &tls.Config{}),
		)
		if c.httpClient.Timeout != deadline {
			t.Errorf("public client Timeout = %v, want %v", c.httpClient.Timeout, deadline)
		}
		if c.internalHTTPClient.Timeout != deadline {
			t.Errorf("internal client Timeout = %v, want %v", c.internalHTTPClient.Timeout, deadline)
		}
		if c.internalHTTPClient == c.httpClient {
			t.Error("expected a distinct internal client under mTLS")
		}
	})
}

// TestWithMessageDeadline_DefaultAndGuard verifies the default (no option) is the
// historical fixed timeout, and a non-positive deadline is ignored so the timeout
// can never be disabled.
func TestWithMessageDeadline_DefaultAndGuard(t *testing.T) {
	def := New("http://api:8080")
	if def.httpClient.Timeout != defaultRequestTimeout {
		t.Errorf("default Timeout = %v, want %v", def.httpClient.Timeout, defaultRequestTimeout)
	}

	// A zero/negative deadline must be ignored (guard against an infinite wait).
	guarded := New("http://api:8080", WithMessageDeadline(0), WithMessageDeadline(-5*time.Second))
	if guarded.httpClient.Timeout != defaultRequestTimeout {
		t.Errorf("non-positive deadline should be ignored, got Timeout = %v", guarded.httpClient.Timeout)
	}
	if guarded.httpClient.Timeout <= 0 {
		t.Error("timeout must never be disabled")
	}
}

// TestDeliverMessage_SizeAwareDeadlineGoverns proves the configured message
// deadline actually bounds a delivery: a slow API times out under a tiny
// deadline, and the same delivery succeeds under a generous one — i.e. a large
// message is no longer stranded by too-short a timeout.
func TestDeliverMessage_SizeAwareDeadlineGoverns(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	releaseAPI := sync.OnceFunc(func() { close(release) })
	defer releaseAPI()
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
			return
		case <-release:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":1,"mailbox_id":2,"subject":"ok"}}`))
	})

	t.Run("too-short deadline times out", func(t *testing.T) {
		c := New(srv.URL, WithMessageDeadline(20*time.Millisecond))
		if _, err := c.DeliverMessage(&DeliverRequest{Address: "a@local", RawMessage: []byte("hi")}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want a deadline exceeded while the API is blocked", err)
		}
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("the request did not reach the blocked API")
		}
	})

	t.Run("generous deadline succeeds", func(t *testing.T) {
		c := New(srv.URL, WithMessageDeadline(30*time.Second))
		go func() {
			select {
			case <-entered:
				releaseAPI()
			case <-t.Context().Done():
			}
		}()
		resp, err := c.DeliverMessage(&DeliverRequest{Address: "a@local", RawMessage: []byte("hi")})
		if err != nil {
			t.Fatalf("expected success under a 30s deadline, got %v", err)
		}
		if resp.Data.ID != 1 {
			t.Errorf("unexpected response id %d", resp.Data.ID)
		}
	})
}
