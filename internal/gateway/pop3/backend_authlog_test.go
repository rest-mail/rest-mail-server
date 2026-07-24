package pop3

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// TestAuthenticate_LogsMaskedFailure verifies a failed POP3 login emits a
// structured pop3_auth_failed event with the attempted username masked (never
// written in the clear), matching the SMTP gateway's smtp_auth_failed logging.
func TestAuthenticate_LogsMaskedFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	b := NewBackend(apiclient.New(srv.URL))
	if _, err := b.Authenticate("alice@example.com", "wrong"); err == nil {
		t.Fatal("expected auth to fail against a 401 server")
	}

	out := buf.String()
	if !strings.Contains(out, "pop3_auth_failed") {
		t.Errorf("expected pop3_auth_failed event in log, got: %s", out)
	}
	if strings.Contains(out, "alice@example.com") {
		t.Errorf("auth-failure log must not contain the clear local-part, got: %s", out)
	}
	if !strings.Contains(out, "a***@example.com") {
		t.Errorf("expected masked user a***@example.com in log, got: %s", out)
	}
}
