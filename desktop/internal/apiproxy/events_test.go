package apiproxy

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
)

type eventManager struct {
	request func(context.Context, *http.Request) (*http.Response, error)
}

func (manager eventManager) Status() session.SessionStatus {
	return session.SessionStatus{SignedIn: true, Server: "http://localhost", Email: "admin@example.test"}
}

func (manager eventManager) DoRequest(ctx context.Context, request *http.Request) (*http.Response, error) {
	return manager.request(ctx, request)
}

func (manager eventManager) Redact(data []byte) []byte {
	return []byte(strings.ReplaceAll(string(data), "private-value", "[redacted]"))
}

func TestReadMailEvents(t *testing.T) {
	input := ": keepalive\n\nid: 41\nevent: new_message\ndata: {\ndata: \"message_id\":7,\"sender\":\"private-value\",\"access_token\":\"hidden\"}\n\nid: 42\nevent: folder_update\ndata: {\"unread_count\":3}\n\nevent: unknown\ndata: {}\n\nevent: new_message\ndata: invalid\n\nevent: new_message\ndata: null\n\n"
	var received []MailEvent
	lastID := ""
	readMailEvents(context.Background(), strings.NewReader(input), eventManager{}, &lastID, func(event MailEvent) { received = append(received, event) })
	if lastID != "42" || len(received) != 2 || received[0].Data["sender"] != "[redacted]" || received[1].Data["unread_count"] != float64(3) {
		t.Fatalf("unexpected events: %#v, last ID %q", received, lastID)
	}
	if _, present := received[0].Data["access_token"]; present {
		t.Fatal("credential escaped sanitization")
	}
}

func TestMailEventsStreamsBeforeEOFAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	manager := eventManager{request: func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/accounts/7/events" || request.Header.Get("Accept") != "text/event-stream" {
			t.Error("invalid stream request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	}}
	received := make(chan MailEvent, 2)
	done := make(chan struct{})
	go func() {
		MailEvents(ctx, manager, 7, func(event MailEvent) { received <- event })
		close(done)
	}()
	go func() { _, _ = io.WriteString(writer, "id: 1\nevent: new_message\ndata: {\"message_id\":7}\n\n") }()
	for _, expected := range []string{"stream_ready", "new_message"} {
		select {
		case event := <-received:
			if event.Type != expected {
				t.Fatalf("got %q, want %q", event.Type, expected)
			}
		case <-time.After(time.Second):
			t.Fatal("event buffered until EOF")
		}
	}
	cancel()
	writer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop")
	}
}

func TestMailEventsReconnectsWithLastEventID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ids []string
	manager := eventManager{request: func(_ context.Context, request *http.Request) (*http.Response, error) {
		ids = append(ids, request.Header.Get("Last-Event-ID"))
		if len(ids) == 2 {
			cancel()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("id: 73\nevent: folder_update\ndata: {}\n\n"))}, nil
	}}
	MailEvents(ctx, manager, 7, func(MailEvent) {})
	if !reflect.DeepEqual(ids, []string{"", "73"}) {
		t.Fatalf("replay cursors: %#v", ids)
	}
}
