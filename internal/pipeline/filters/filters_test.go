package filters

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// ── Helper ──────────────────────────────────────────────────────────

// validEmail returns a minimal EmailJSON with valid From and Date headers.
func validEmail() *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{
			MailFrom: "sender@example.com",
			RcptTo:   []string{"recipient@example.com"},
		},
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Address: "sender@example.com"}},
			To:        []pipeline.Address{{Address: "recipient@example.com"}},
			Date:      "Mon, 17 Feb 2026 10:00:00 +0000",
			MessageID: "<abc123@example.com>",
			Subject:   "Test message",
		},
		Body: pipeline.Body{
			ContentType: "text/plain",
			Content:     "Hello, world!",
		},
	}
}

// ── HeaderValidate Tests ────────────────────────────────────────────

func TestHeaderValidate_ValidHeaders(t *testing.T) {
	f, err := NewHeaderValidate(nil)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	result, err := f.Execute(context.Background(), validEmail())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
}

func TestHeaderValidate_MissingFrom(t *testing.T) {
	f, err := NewHeaderValidate(nil)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := validEmail()
	email.Headers.From = nil

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject for missing From, got %q", result.Action)
	}
	if result.RejectMsg == "" {
		t.Error("expected non-empty reject message")
	}
}

func TestHeaderValidate_MissingDate(t *testing.T) {
	f, err := NewHeaderValidate(nil)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := validEmail()
	email.Headers.Date = ""

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject for missing Date, got %q", result.Action)
	}
}

func TestHeaderValidate_HeaderInjection(t *testing.T) {
	f, err := NewHeaderValidate(nil)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := validEmail()
	email.Headers.Raw = map[string][]string{
		"X-Evil": {"innocent\r\nBcc: attacker@evil.com"},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject for header injection, got %q", result.Action)
	}
}

func TestHeaderValidate_CustomConfig(t *testing.T) {
	// Disable require_from so that a missing From header is allowed.
	cfg := json.RawMessage(`{"require_from": false, "require_date": true, "reject_injection": true}`)
	f, err := NewHeaderValidate(cfg)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := validEmail()
	email.Headers.From = nil // Would normally be rejected

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue (require_from disabled), got %q", result.Action)
	}
}

// ── HeaderCleanup Tests ─────────────────────────────────────────────

func TestHeaderCleanup_RemovesBcc(t *testing.T) {
	f, err := NewHeaderCleanup(nil)
	if err != nil {
		t.Fatalf("NewHeaderCleanup: %v", err)
	}

	email := validEmail()
	email.Headers.Bcc = []pipeline.Address{{Address: "secret@example.com"}}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
	if result.Type != pipeline.FilterTypeTransform {
		t.Errorf("expected FilterTypeTransform, got %q", result.Type)
	}
	if result.Message == nil {
		t.Fatal("expected non-nil message from transform filter")
	}
	if len(result.Message.Headers.Bcc) != 0 {
		t.Errorf("expected Bcc to be cleared, got %v", result.Message.Headers.Bcc)
	}
}

func TestHeaderCleanup_RemovesInternalHeaders(t *testing.T) {
	f, err := NewHeaderCleanup(nil)
	if err != nil {
		t.Fatalf("NewHeaderCleanup: %v", err)
	}

	email := validEmail()
	email.Headers.Raw = map[string][]string{
		"X-RestMail-Internal": {"debug-info"},
		"X-Pipeline-ID":      {"pipe-123"},
		"X-Queue-ID":         {"queue-456"},
		"X-Mailer":           {"RestMail/1.0"},
		"Bcc":                {"hidden@example.com"},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Message == nil {
		t.Fatal("expected non-nil message from transform filter")
	}

	raw := result.Message.Headers.Raw
	for _, header := range []string{"X-RestMail-Internal", "X-Pipeline-ID", "X-Queue-ID", "Bcc"} {
		if _, exists := raw[header]; exists {
			t.Errorf("expected header %q to be removed, but it still exists", header)
		}
	}

	// X-Mailer should still be present (it is not an internal header).
	if _, exists := raw["X-Mailer"]; !exists {
		t.Error("expected X-Mailer to remain in raw headers")
	}
}

// ── SizeCheck Tests ─────────────────────────────────────────────────

func TestSizeCheck_UnderLimit(t *testing.T) {
	f, err := NewSizeCheck(nil) // default 25 MB
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}

	email := validEmail()

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
}

func TestSizeCheck_OverLimit(t *testing.T) {
	// Set a very small limit (100 bytes) so our test email exceeds it.
	cfg := json.RawMessage(`{"max_size_bytes": 100}`)
	f, err := NewSizeCheck(cfg)
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}

	email := validEmail()
	// Add enough body content to exceed 100 bytes.
	largeBody := make([]byte, 200)
	for i := range largeBody {
		largeBody[i] = 'A'
	}
	email.Body.Content = string(largeBody)

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject for oversized message, got %q", result.Action)
	}
	if result.RejectMsg == "" {
		t.Error("expected non-empty reject message")
	}
}

func TestSizeCheck_CustomLimit(t *testing.T) {
	// Set a custom limit of 500 bytes.
	cfg := json.RawMessage(`{"max_size_bytes": 500}`)
	f, err := NewSizeCheck(cfg)
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}

	email := validEmail()
	// Body is small enough to fit within 500 bytes.
	email.Body.Content = "Short message."

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue for message under custom limit, got %q", result.Action)
	}
}

// ── RateLimit Tests ─────────────────────────────────────────────────

func TestRateLimit_UnderLimit(t *testing.T) {
	f, err := NewRateLimit()(nil) // default: 20/min, 100/hour
	if err != nil {
		t.Fatalf("NewRateLimit: %v", err)
	}

	email := validEmail()

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
}

func TestRateLimit_ExceedsMinuteLimit(t *testing.T) {
	// Set a per-minute limit of 2 so we can trigger it with 3 messages.
	cfg := json.RawMessage(`{"max_per_minute": 2, "max_per_hour": 100}`)
	// A single factory hands out per-message instances that share one store, so
	// three sends against instances from the same factory accumulate.
	factory := NewRateLimit()
	f, err := factory(cfg)
	if err != nil {
		t.Fatalf("NewRateLimit: %v", err)
	}

	email := validEmail()

	// First two messages should pass.
	for i := 0; i < 2; i++ {
		result, err := f.Execute(context.Background(), email)
		if err != nil {
			t.Fatalf("Execute (msg %d): %v", i+1, err)
		}
		if result.Action != pipeline.ActionContinue {
			t.Fatalf("msg %d: expected ActionContinue, got %q", i+1, result.Action)
		}
	}

	// Third message should be deferred.
	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute (msg 3): %v", err)
	}
	if result.Action != pipeline.ActionDefer {
		t.Errorf("msg 3: expected ActionDefer, got %q", result.Action)
	}
	if result.RejectMsg == "" {
		t.Error("expected non-empty reject message on rate limit")
	}
}

// TestRateLimit_PersistsAcrossMessages proves the limiter accumulates across the
// fresh filter instance the engine builds for every message. The engine calls
// registry.Create once per message (engine.go), so a limiter whose counter lives
// in the per-message instance resets to a count of 1 each time and can never
// trip. This drives the real registration path (DefaultRegistry, populated by
// the package init) exactly as the engine does — a fresh Create per message —
// and requires the configured per-minute limit to be enforced across them.
func TestRateLimit_PersistsAcrossMessages(t *testing.T) {
	cfg := json.RawMessage(`{"max_per_minute": 2, "max_per_hour": 100}`)

	email := validEmail()
	// Unique sender so this test's counts never collide with any other test that
	// shares the process-global DefaultRegistry limiter store.
	email.Envelope.MailFrom = "persist-across-messages@example.com"
	email.Headers.From = []pipeline.Address{{Address: "persist-across-messages@example.com"}}

	var last pipeline.Action
	for i := 0; i < 3; i++ {
		f, err := pipeline.DefaultRegistry.Create("rate_limit", cfg)
		if err != nil {
			t.Fatalf("Create (msg %d): %v", i+1, err)
		}
		res, err := f.Execute(context.Background(), email)
		if err != nil {
			t.Fatalf("Execute (msg %d): %v", i+1, err)
		}
		last = res.Action
	}

	if last != pipeline.ActionDefer {
		t.Fatalf("expected ActionDefer on the 3rd message (max_per_minute=2), got %q; "+
			"limiter state did not persist across per-message filter instances", last)
	}
}

// TestRateLimit_TemplateConfigKeyMatches guards the default outbound template's
// rate_limit config against the struct tags the filter actually reads. A
// mismatched key (e.g. per_sender_per_hour vs max_per_hour) is silently dropped
// by encoding/json, so the configured limit would never take effect.
func TestRateLimit_TemplateConfigKeyMatches(t *testing.T) {
	var rlConfig json.RawMessage
	for _, fc := range pipeline.DefaultOutboundPipeline(0).Filters {
		if fc.Name == "rate_limit" {
			rlConfig = fc.Config
		}
	}
	if len(rlConfig) == 0 {
		t.Fatal("default outbound pipeline has no rate_limit filter")
	}
	var parsed struct {
		MaxPerHour   int `json:"max_per_hour"`
		MaxPerMinute int `json:"max_per_minute"`
	}
	if err := json.Unmarshal(rlConfig, &parsed); err != nil {
		t.Fatalf("unmarshal template config: %v", err)
	}
	if parsed.MaxPerHour == 0 {
		t.Fatalf("template rate_limit config %s does not populate max_per_hour "+
			"(config key mismatch: the configured limit is silently ignored)", rlConfig)
	}
}
