package smtp

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// refusingDelivery answers every delivery with err.
type refusingDelivery struct {
	*mockBackend
	err error
}

func (backend *refusingDelivery) DeliverMessageFrom(*apiclient.DeliverRequest, apiclient.DeliverContent) (*apiclient.DeliverResponse, error) {
	return nil, backend.err
}

// lockedBuffer lets the test read what the session's goroutine logged.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// levelsOf answers the level of each line in logs with one of the delivery messages.
func (b *lockedBuffer) levelsOf(t *testing.T) map[string]string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	levels := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		var entry struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if entry.Msg == "smtp: delivery refused" || entry.Msg == "smtp: delivery failed" {
			levels[entry.Msg] = entry.Level
		}
	}
	return levels
}

// The default pipeline greylists every new sender, so a working install answers 451 all
// day. A load run logged 280 of those as ERROR among the timeouts that were real
// failures (#1583). What the API refuses is an outcome; what stops it answering is the
// fault. The reply to the sender doesn't depend on which it was.
func TestARefusedDeliveryIsNotLoggedAsAFailure(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		reply     string
		wantMsg   string
		wantLevel string
	}{
		{"greylisted", &apiclient.APIError{StatusCode: 503, Body: `{"error":{"code":"deferred"}}`}, "451", "smtp: delivery refused", "INFO"},
		{"rejected by a filter", &apiclient.APIError{StatusCode: 403, Body: `{"error":{"code":"rejected"}}`}, "550", "smtp: delivery refused", "INFO"},
		{"legacy SMTP deferral", &apiclient.APIError{StatusCode: 451, Body: `{"error":{"code":"deferred"}}`}, "451", "smtp: delivery failed", "ERROR"},
		{"legacy SMTP rejection", &apiclient.APIError{StatusCode: 550, Body: `{"error":{"code":"rejected"}}`}, "451", "smtp: delivery failed", "ERROR"},
		{"no such mailbox", &apiclient.APIError{StatusCode: 404, Body: `{"error":{"code":"mailbox_not_found"}}`}, "550", "smtp: delivery refused", "INFO"},
		{"mailbox full", &apiclient.APIError{StatusCode: 422, Body: `{"error":{"code":"mailbox_full"}}`}, "452", "smtp: delivery refused", "INFO"},
		{"the API broke", &apiclient.APIError{StatusCode: 500, Body: `{"error":{"code":"internal_error"}}`}, "451", "smtp: delivery failed", "ERROR"},
		{"the API is unavailable", &apiclient.APIError{StatusCode: 503, Body: `unavailable`}, "451", "smtp: delivery failed", "ERROR"},
		{"the gateway was forbidden", &apiclient.APIError{StatusCode: 403, Body: `forbidden`}, "550", "smtp: delivery failed", "ERROR"},
		{"no answer in time", errors.New("POST /api/v1/messages/deliver: context deadline exceeded"), "451", "smtp: delivery failed", "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs lockedBuffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			backend := newMockBackend()
			backend.local["mailbox@local.test"] = true
			harness := newSMTPHarness(t, backend, newMockStore(), false, func(server *Server) {
				server.api = &refusingDelivery{mockBackend: backend, err: tt.err}
			})
			harness.ehlo()
			for _, command := range []string{"MAIL FROM:<sender@remote.test>", "RCPT TO:<mailbox@local.test>"} {
				if reply := harness.cmd(command); replyCode(reply) != "250" {
					t.Fatal(reply)
				}
			}
			if reply := harness.dataBody(testBody); replyCode(reply) != tt.reply {
				t.Fatalf("reply = %s, want %s", reply, tt.reply)
			}

			levels := logs.levelsOf(t)
			if len(levels) != 1 || levels[tt.wantMsg] != tt.wantLevel {
				t.Fatalf("logged %v, want only %q at %s", levels, tt.wantMsg, tt.wantLevel)
			}
		})
	}
}
