package connlimiter

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// refusalLines runs fn with the default logger captured and answers its "connection
// refused" lines.
func refusalLines(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previous)
	fn()
	var lines []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if entry["msg"] == "connection refused" {
			lines = append(lines, entry)
		}
	}
	return lines
}

// Each limit has a different remedy, and a refused client only sees a closed
// connection, so the log has to say which limit it was. A load run logged 1240 refusals
// that could not be told apart (#1583).
func TestARefusalNamesTheLimitThatRefused(t *testing.T) {
	tests := []struct {
		name     string
		limiter  func() *Limiter
		fill     []string
		ip       string
		limit    string
		wantOpen float64
		wantMax  float64
	}{
		{
			name:     "one address at its limit",
			limiter:  func() *Limiter { return New(Config{MaxPerIP: 2, MaxGlobal: 10}) },
			fill:     []string{"10.0.0.1", "10.0.0.1"},
			ip:       "10.0.0.1",
			limit:    "per_ip",
			wantOpen: 2,
			wantMax:  2,
		},
		{
			name:     "the gateway at its total",
			limiter:  func() *Limiter { return New(Config{MaxPerIP: 2, MaxGlobal: 3}) },
			fill:     []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"},
			ip:       "10.0.0.4",
			limit:    "global",
			wantOpen: 3,
			wantMax:  3,
		},
		{
			name: "a banned address",
			limiter: func() *Limiter {
				limiter := New(Config{})
				limiter.SetBanChecker(func(ip, _ string) bool { return ip == "10.0.0.9" }, "smtp")
				return limiter
			},
			ip:    "10.0.0.9",
			limit: "ban",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter := tt.limiter()
			for _, ip := range tt.fill {
				if !limiter.Accept(ip) {
					t.Fatalf("filling the limit: %s refused", ip)
				}
			}
			lines := refusalLines(t, func() {
				if limiter.Accept(tt.ip) {
					t.Fatalf("%s should be refused", tt.ip)
				}
			})
			if len(lines) != 1 {
				t.Fatalf("got %d refusal lines, want 1: %v", len(lines), lines)
			}
			line := lines[0]
			if line["limit"] != tt.limit || line["ip"] != tt.ip {
				t.Errorf("limit = %v, ip = %v, want %s and %s", line["limit"], line["ip"], tt.limit, tt.ip)
			}
			if tt.wantMax != 0 && (line["open"] != tt.wantOpen || line["max"] != tt.wantMax) {
				t.Errorf("open = %v, max = %v, want %v and %v", line["open"], line["max"], tt.wantOpen, tt.wantMax)
			}
		})
	}
}

func TestAnAdmittedConnectionLogsNoRefusal(t *testing.T) {
	limiter := New(Config{MaxPerIP: 1, MaxGlobal: 1})
	if lines := refusalLines(t, func() {
		if !limiter.Accept("10.0.0.1") {
			t.Fatal("the first connection should be admitted")
		}
	}); len(lines) != 0 {
		t.Fatalf("admitting a connection logged %v", lines)
	}
}
