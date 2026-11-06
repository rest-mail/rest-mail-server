package imap

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/requestid"
)

// A session's log line and its call to the API carry the same ID (#1580).
func TestAuthenticate_LogsAndSendsTheSessionsID(t *testing.T) {
	sent := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent <- r.Header.Get(requestid.Header)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if _, err := NewBackend(apiclient.New(srv.URL), nil).Authenticate("alice@example.com", "wrong"); err == nil {
		t.Fatal("expected auth to fail against a 401 server")
	}

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("auth-failure line: %v (%q)", err, buf.String())
	}
	id, _ := line[requestid.Attr].(string)
	if !requestid.Valid(id) {
		t.Fatalf("auth-failure line has no valid request_id: %v", line)
	}
	if got := <-sent; got != id {
		t.Fatalf("API call sent X-Request-Id %q, the log line has %q", got, id)
	}
}
