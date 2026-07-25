package apiclient

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// TestListMessages_LogsOnTruncation proves that when a folder holds more
// messages than the gateway cap, the loader stops at the cap AND emits a warning
// so the silent-truncation (oldest messages unreachable, EXISTS/STAT under-
// reporting per RFC 3501 §6.4.1 / RFC 1939 §5) is at least observable to an
// operator rather than invisible.
func TestListMessages_LogsOnTruncation(t *testing.T) {
	orig := maxGatewayMessages
	maxGatewayMessages = 2
	defer func() { maxGatewayMessages = orig }()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/accounts/9/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		// Every page reports more available, so the loader keeps going until it
		// trips the cap.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": 3, "subject": "c"},
				{"id": 2, "subject": "b"},
			},
			"pagination": map[string]any{"cursor": "next", "has_more": true, "total": 100},
		})
	})

	resp, err := New(srv.URL).ListMessages("tok", 9, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) < maxGatewayMessages {
		t.Fatalf("expected at least the cap (%d) messages loaded, got %d", maxGatewayMessages, len(resp.Data))
	}

	logged := buf.String()
	if !strings.Contains(logged, "truncat") {
		t.Errorf("expected a truncation warning to be logged, got: %q", logged)
	}
}
