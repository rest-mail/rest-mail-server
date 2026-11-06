package endpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A caller that hangs up cancels the request's context, and the handler then returns
// that error from whatever it was reading. A load run logged 724 of these as ERROR, 530
// of them in the one second a gateway stopped (#1583). They are not failures of the
// API, though what is left over must still be reported as one.
func TestACancelledRequestIsNotLoggedAsAHandlerFailure(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantMsg   string
		wantLevel string
	}{
		{"the caller went away", fmt.Errorf("count messages: %w", context.Canceled), "api: request cancelled", "INFO"},
		{"a deadline ran out", fmt.Errorf("count messages: %w", context.DeadlineExceeded), "api: handler failed", "ERROR"},
		{"anything else", errors.New("count messages: connection refused"), "api: handler failed", "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			got := httptest.NewRecorder()
			writeError(got, tt.err)

			if got.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", got.Code)
			}
			var entry struct {
				Level string `json:"level"`
				Msg   string `json:"msg"`
			}
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatalf("log %q: %v", logs.String(), err)
			}
			if entry.Msg != tt.wantMsg || entry.Level != tt.wantLevel {
				t.Fatalf("logged %s %q, want %s %q", entry.Level, entry.Msg, tt.wantLevel, tt.wantMsg)
			}
		})
	}
}
