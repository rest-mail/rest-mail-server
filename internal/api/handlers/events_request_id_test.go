package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/requestid"
)

// captureLogs sends the default logger's lines, request_id added as the
// binaries do, to a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(requestid.NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &out
}

func logLines(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

// An event published for a request is logged with that request's ID (#1580);
// the event a subscriber receives is the one published, with no ID added.
func TestPublish_LogLineCarriesTheRequestID(t *testing.T) {
	out := captureLogs(t)
	broker := NewSSEBroker()
	ch := broker.Subscribe(7)
	defer broker.Unsubscribe(7, ch)

	ctx := requestid.NewContext(context.Background(), "gw-1580-publish")
	broker.Publish(ctx, 7, SSEEvent{Type: "new_message", Data: map[string]any{"message_id": 42}})

	got := <-ch
	if data, _ := json.Marshal(got.Event.Data); string(data) != `{"message_id":42}` {
		t.Fatalf("event data %s, want it unchanged", data)
	}
	lines := logLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("lines %v, want one", lines)
	}
	line := lines[0]
	if line["msg"] != "sse: event published" || line[requestid.Attr] != "gw-1580-publish" ||
		line["event"] != "new_message" || line["subscribers"] != float64(1) || line["level"] != "DEBUG" {
		t.Fatalf("line %v, want the publish with request_id gw-1580-publish", line)
	}
}

// An event a slow subscriber misses is a warning, still with the request's ID.
func TestPublish_DroppedEventWarnsWithTheRequestID(t *testing.T) {
	broker := NewSSEBroker()
	ch := broker.Subscribe(7)
	defer broker.Unsubscribe(7, ch)
	for i := 0; i < cap(ch); i++ {
		broker.Publish(context.Background(), 7, SSEEvent{Type: "folder_update"})
	}

	out := captureLogs(t)
	broker.Publish(requestid.NewContext(context.Background(), "gw-1580-dropped"), 7, SSEEvent{Type: "folder_update"})
	lines := logLines(t, out)
	if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["dropped"] != float64(1) ||
		lines[0][requestid.Attr] != "gw-1580-dropped" {
		t.Fatalf("lines %v, want one warning of a dropped event with request_id gw-1580-dropped", lines)
	}
}
