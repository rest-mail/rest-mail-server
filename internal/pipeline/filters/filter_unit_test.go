package filters

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// ── Shared helpers ──────────────────────────────────────────────────

// unitEmail returns a well-formed EmailJSON for unit tests. It includes
// all the headers that header_validate requires by default so that
// individual tests only need to remove/change what they want to break.
func unitEmail() *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{
			MailFrom:  "alice@example.com",
			RcptTo:    []string{"bob@example.com"},
			Direction: "inbound",
		},
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Name: "Alice", Address: "alice@example.com"}},
			To:        []pipeline.Address{{Address: "bob@example.com"}},
			Date:      "Tue, 18 Feb 2026 09:00:00 +0000",
			MessageID: "<unit-test-1@example.com>",
			Subject:   "Unit test message",
		},
		Body: pipeline.Body{
			ContentType: "text/plain",
			Content:     "This is a unit test email body.",
		},
	}
}

// ═════════════════════════════════════════════════════════════════════
// SizeCheck filter
// ═════════════════════════════════════════════════════════════════════

func TestUnitSizeCheck_DefaultLimitPassesSmallMessage(t *testing.T) {
	f, err := NewSizeCheck(nil)
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}

	result, err := f.Execute(context.Background(), unitEmail())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
	if result.Log.Filter != "size_check" {
		t.Errorf("expected log filter 'size_check', got %q", result.Log.Filter)
	}
	if result.Log.Result != "pass" {
		t.Errorf("expected log result 'pass', got %q", result.Log.Result)
	}
}

func TestUnitSizeCheck_RejectsOversizedBody(t *testing.T) {
	cfg := json.RawMessage(`{"max_size_bytes": 50}`)
	f, err := NewSizeCheck(cfg)
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}

	email := unitEmail()
	email.Body.Content = strings.Repeat("X", 200)

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject, got %q", result.Action)
	}
	if !strings.Contains(result.RejectMsg, "552") {
		t.Errorf("reject message should contain 552 code, got %q", result.RejectMsg)
	}
	if result.Log.Result != "reject" {
		t.Errorf("expected log result 'reject', got %q", result.Log.Result)
	}
}

func TestUnitSizeCheck_RejectsOversizedAttachment(t *testing.T) {
	cfg := json.RawMessage(`{"max_size_bytes": 100}`)
	f, err := NewSizeCheck(cfg)
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}

	email := unitEmail()
	email.Body.Content = "tiny"
	// Create a base64-encoded attachment that decodes to 200 bytes.
	rawData := strings.Repeat("B", 200)
	email.Attachments = []pipeline.Attachment{
		{
			Filename:    "big.bin",
			ContentType: "application/octet-stream",
			Content:     base64.StdEncoding.EncodeToString([]byte(rawData)),
		},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject for large attachment, got %q", result.Action)
	}
}

func TestUnitSizeCheck_AttachmentUsesSize_WhenNoContent(t *testing.T) {
	cfg := json.RawMessage(`{"max_size_bytes": 100}`)
	f, err := NewSizeCheck(cfg)
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}

	email := unitEmail()
	email.Body.Content = "tiny"
	// Attachment has no Content field, but Size is set (already extracted).
	email.Attachments = []pipeline.Attachment{
		{
			Filename:    "big.bin",
			ContentType: "application/octet-stream",
			Size:        500,
			Storage:     "filesystem",
			Ref:         "/var/mail/attachments/abc123",
		},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject using Size field, got %q", result.Action)
	}
}

func TestUnitSizeCheck_ExactlyAtLimitPasses(t *testing.T) {
	// EstimateSize includes headers (From name+addr+20 + Subject+20) + body.
	// Build an email where the estimated size equals the limit exactly.
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:    []pipeline.Address{{Address: "a@b.c"}}, // len("a@b.c")+0+20 = 25
			Subject: "",                                      // 0+20 = 20
		},
		Body: pipeline.Body{Content: ""},
	}
	// EstimateSize = 25 (from) + 20 (subject) + 0 (body) = 45
	cfg := json.RawMessage(`{"max_size_bytes": 45}`)
	f, err := NewSizeCheck(cfg)
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue at exact limit, got %q (detail: %s)", result.Action, result.Log.Detail)
	}
}

func TestUnitSizeCheck_InvalidConfig(t *testing.T) {
	_, err := NewSizeCheck([]byte(`{invalid json`))
	if err == nil {
		t.Error("expected error for invalid JSON config")
	}
}

func TestUnitSizeCheck_EmptyConfig_UsesDefault(t *testing.T) {
	f, err := NewSizeCheck([]byte(`{}`))
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}
	if f.Name() != "size_check" {
		t.Errorf("expected name 'size_check', got %q", f.Name())
	}
	if f.Type() != pipeline.FilterTypeAction {
		t.Errorf("expected type 'action', got %q", f.Type())
	}
}

func TestUnitSizeCheck_MultipartBodyParts(t *testing.T) {
	cfg := json.RawMessage(`{"max_size_bytes": 100}`)
	f, err := NewSizeCheck(cfg)
	if err != nil {
		t.Fatalf("NewSizeCheck: %v", err)
	}

	email := unitEmail()
	email.Body.Content = ""
	email.Body.Parts = []pipeline.Body{
		{ContentType: "text/plain", Content: strings.Repeat("A", 60)},
		{ContentType: "text/html", Content: strings.Repeat("B", 60)},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// 60+60 body parts + header overhead => should exceed 100
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject for multipart body exceeding limit, got %q (detail: %s)", result.Action, result.Log.Detail)
	}
}

// ═════════════════════════════════════════════════════════════════════
// HeaderValidate filter
// ═════════════════════════════════════════════════════════════════════

func TestUnitHeaderValidate_AllHeadersPresent(t *testing.T) {
	f, err := NewHeaderValidate(nil)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	result, err := f.Execute(context.Background(), unitEmail())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
	if result.Log.Detail != "all headers valid" {
		t.Errorf("expected detail 'all headers valid', got %q", result.Log.Detail)
	}
}

func TestUnitHeaderValidate_MissingFromRejects(t *testing.T) {
	f, err := NewHeaderValidate(nil) // require_from defaults to true
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := unitEmail()
	email.Headers.From = nil

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject, got %q", result.Action)
	}
	if !strings.Contains(result.RejectMsg, "From") {
		t.Errorf("reject message should mention From, got %q", result.RejectMsg)
	}
}

func TestUnitHeaderValidate_MissingDateRejects(t *testing.T) {
	f, err := NewHeaderValidate(nil) // require_date defaults to true
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := unitEmail()
	email.Headers.Date = ""

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject, got %q", result.Action)
	}
	if !strings.Contains(result.RejectMsg, "Date") {
		t.Errorf("reject message should mention Date, got %q", result.RejectMsg)
	}
}

func TestUnitHeaderValidate_MissingMessageID_DefaultAllowed(t *testing.T) {
	// By default require_message_id is false, so a missing Message-ID is OK.
	f, err := NewHeaderValidate(nil)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := unitEmail()
	email.Headers.MessageID = ""

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue (message-id not required by default), got %q", result.Action)
	}
}

func TestUnitHeaderValidate_MissingMessageID_WhenRequired(t *testing.T) {
	cfg := json.RawMessage(`{"require_message_id": true}`)
	f, err := NewHeaderValidate(cfg)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := unitEmail()
	email.Headers.MessageID = ""

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject for missing Message-ID when required, got %q", result.Action)
	}
	if !strings.Contains(result.RejectMsg, "Message-ID") {
		t.Errorf("reject message should mention Message-ID, got %q", result.RejectMsg)
	}
}

func TestUnitHeaderValidate_HeaderInjectionCRLF(t *testing.T) {
	f, err := NewHeaderValidate(nil) // reject_injection defaults to true
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := unitEmail()
	email.Headers.Raw = map[string][]string{
		"X-Injected": {"value\r\nBcc: attacker@evil.com"},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject for CRLF injection, got %q", result.Action)
	}
	if !strings.Contains(result.RejectMsg, "injection") {
		t.Errorf("reject message should mention injection, got %q", result.RejectMsg)
	}
}

func TestUnitHeaderValidate_HeaderInjectionLF(t *testing.T) {
	f, err := NewHeaderValidate(nil)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := unitEmail()
	email.Headers.Raw = map[string][]string{
		"X-Sneaky": {"value\nAnother-Header: injected"},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject for LF injection, got %q", result.Action)
	}
}

func TestUnitHeaderValidate_InjectionCheckDisabled(t *testing.T) {
	cfg := json.RawMessage(`{"reject_injection": false}`)
	f, err := NewHeaderValidate(cfg)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := unitEmail()
	email.Headers.Raw = map[string][]string{
		"X-Injected": {"value\r\nBcc: attacker@evil.com"},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue when injection check disabled, got %q", result.Action)
	}
}

func TestUnitHeaderValidate_MultipleIssues(t *testing.T) {
	// All requirements enabled: missing From, Date, and Message-ID.
	cfg := json.RawMessage(`{"require_from":true,"require_date":true,"require_message_id":true}`)
	f, err := NewHeaderValidate(cfg)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := unitEmail()
	email.Headers.From = nil
	email.Headers.Date = ""
	email.Headers.MessageID = ""

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject, got %q", result.Action)
	}
	// All three issues should be mentioned.
	for _, kw := range []string{"From", "Date", "Message-ID"} {
		if !strings.Contains(result.RejectMsg, kw) {
			t.Errorf("reject message should mention %q, got %q", kw, result.RejectMsg)
		}
	}
}

func TestUnitHeaderValidate_CleanRawHeaders(t *testing.T) {
	f, err := NewHeaderValidate(nil)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}

	email := unitEmail()
	email.Headers.Raw = map[string][]string{
		"X-Custom":      {"safe value"},
		"X-Another":     {"also safe"},
		"X-Multi-Value": {"val1", "val2"},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue for clean raw headers, got %q", result.Action)
	}
}

func TestUnitHeaderValidate_NameAndType(t *testing.T) {
	f, err := NewHeaderValidate(nil)
	if err != nil {
		t.Fatalf("NewHeaderValidate: %v", err)
	}
	if f.Name() != "header_validate" {
		t.Errorf("expected name 'header_validate', got %q", f.Name())
	}
	if f.Type() != pipeline.FilterTypeAction {
		t.Errorf("expected type 'action', got %q", f.Type())
	}
}

// ═════════════════════════════════════════════════════════════════════
// HeaderCleanup filter
// ═════════════════════════════════════════════════════════════════════

func TestUnitHeaderCleanup_RemovesBccStructured(t *testing.T) {
	f, err := NewHeaderCleanup(nil)
	if err != nil {
		t.Fatalf("NewHeaderCleanup: %v", err)
	}

	email := unitEmail()
	email.Headers.Bcc = []pipeline.Address{
		{Address: "hidden1@example.com"},
		{Address: "hidden2@example.com"},
	}

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
		t.Fatal("expected non-nil transformed message")
	}
	if len(result.Message.Headers.Bcc) != 0 {
		t.Errorf("expected Bcc cleared, got %d entries", len(result.Message.Headers.Bcc))
	}
}

func TestUnitHeaderCleanup_RemovesAllInternalRawHeaders(t *testing.T) {
	f, err := NewHeaderCleanup(nil)
	if err != nil {
		t.Fatalf("NewHeaderCleanup: %v", err)
	}

	email := unitEmail()
	email.Headers.Raw = map[string][]string{
		"X-RestMail-Internal": {"secret-data"},
		"X-Pipeline-ID":      {"pipe-001"},
		"X-Queue-ID":         {"queue-999"},
		"Bcc":                {"bcc-raw@example.com"},
		"X-Mailer":           {"RestMail/1.0"},
		"Received":           {"from mx.example.com"},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Message == nil {
		t.Fatal("expected non-nil transformed message")
	}

	raw := result.Message.Headers.Raw
	internalHeaders := []string{"X-RestMail-Internal", "X-Pipeline-ID", "X-Queue-ID", "Bcc"}
	for _, h := range internalHeaders {
		if _, exists := raw[h]; exists {
			t.Errorf("expected internal header %q to be removed", h)
		}
	}

	// Non-internal headers must survive.
	preservedHeaders := []string{"X-Mailer", "Received"}
	for _, h := range preservedHeaders {
		if _, exists := raw[h]; !exists {
			t.Errorf("expected non-internal header %q to be preserved", h)
		}
	}
}

func TestUnitHeaderCleanup_NilRawHeaders(t *testing.T) {
	f, err := NewHeaderCleanup(nil)
	if err != nil {
		t.Fatalf("NewHeaderCleanup: %v", err)
	}

	email := unitEmail()
	email.Headers.Raw = nil

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
	if result.Message == nil {
		t.Fatal("expected non-nil transformed message")
	}
}

func TestUnitHeaderCleanup_PreservesOtherFields(t *testing.T) {
	f, err := NewHeaderCleanup(nil)
	if err != nil {
		t.Fatalf("NewHeaderCleanup: %v", err)
	}

	email := unitEmail()
	email.Headers.Bcc = []pipeline.Address{{Address: "bcc@example.com"}}
	email.Headers.Raw = map[string][]string{
		"X-RestMail-Internal": {"val"},
	}

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	msg := result.Message
	if msg == nil {
		t.Fatal("expected non-nil transformed message")
	}

	// Other header fields should be unchanged.
	if msg.Headers.Subject != email.Headers.Subject {
		t.Errorf("Subject changed: %q -> %q", email.Headers.Subject, msg.Headers.Subject)
	}
	if msg.Headers.Date != email.Headers.Date {
		t.Errorf("Date changed: %q -> %q", email.Headers.Date, msg.Headers.Date)
	}
	if msg.Headers.MessageID != email.Headers.MessageID {
		t.Errorf("MessageID changed: %q -> %q", email.Headers.MessageID, msg.Headers.MessageID)
	}
	if len(msg.Headers.From) != len(email.Headers.From) {
		t.Errorf("From length changed: %d -> %d", len(email.Headers.From), len(msg.Headers.From))
	}
}

func TestUnitHeaderCleanup_NameAndType(t *testing.T) {
	f, err := NewHeaderCleanup(nil)
	if err != nil {
		t.Fatalf("NewHeaderCleanup: %v", err)
	}
	if f.Name() != "header_cleanup" {
		t.Errorf("expected name 'header_cleanup', got %q", f.Name())
	}
	if f.Type() != pipeline.FilterTypeTransform {
		t.Errorf("expected type 'transform', got %q", f.Type())
	}
}

func TestUnitHeaderCleanup_LogDetail(t *testing.T) {
	f, err := NewHeaderCleanup(nil)
	if err != nil {
		t.Fatalf("NewHeaderCleanup: %v", err)
	}

	result, err := f.Execute(context.Background(), unitEmail())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Log.Filter != "header_cleanup" {
		t.Errorf("expected log filter 'header_cleanup', got %q", result.Log.Filter)
	}
	if result.Log.Result != "transformed" {
		t.Errorf("expected log result 'transformed', got %q", result.Log.Result)
	}
}

// ═════════════════════════════════════════════════════════════════════
// Duplicate filter
// ═════════════════════════════════════════════════════════════════════

func TestUnitDuplicate_RequiresConfig(t *testing.T) {
	// No config at all should fail (need at least webhook_url or queue_recipient).
	_, err := NewDuplicate(nil)
	if err == nil {
		t.Error("expected error when neither webhook_url nor queue_recipient is set")
	}
}

func TestUnitDuplicate_EmptyConfigFails(t *testing.T) {
	_, err := NewDuplicate([]byte(`{}`))
	if err == nil {
		t.Error("expected error for empty config")
	}
}

func TestUnitDuplicate_InvalidJSON(t *testing.T) {
	_, err := NewDuplicate([]byte(`{bad json}`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestUnitDuplicate_QueueRecipient_SetsMetadata(t *testing.T) {
	cfg := json.RawMessage(`{"queue_recipient": "archive@example.com"}`)
	f, err := NewDuplicate(cfg)
	if err != nil {
		t.Fatalf("NewDuplicate: %v", err)
	}

	email := unitEmail()
	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}

	// The filter should set metadata on the email for queue processing.
	if email.Metadata == nil {
		t.Fatal("expected Metadata to be set on email")
	}
	if email.Metadata["duplicate_queue_recipient"] != "archive@example.com" {
		t.Errorf("expected duplicate_queue_recipient='archive@example.com', got %q",
			email.Metadata["duplicate_queue_recipient"])
	}
}

func TestUnitDuplicate_QueueRecipient_LogsDestination(t *testing.T) {
	cfg := json.RawMessage(`{"queue_recipient": "copy@example.com"}`)
	f, err := NewDuplicate(cfg)
	if err != nil {
		t.Fatalf("NewDuplicate: %v", err)
	}

	result, err := f.Execute(context.Background(), unitEmail())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result.Log.Detail, "queue:copy@example.com") {
		t.Errorf("expected log detail to contain queue destination, got %q", result.Log.Detail)
	}
}

func TestUnitDuplicate_WebhookOnly_LogsWebhookDestination(t *testing.T) {
	// Use a non-routable URL so no actual HTTP call succeeds (the goroutine will
	// fail silently, which is fine for this test).
	cfg := json.RawMessage(`{"webhook_url": "http://192.0.2.1:9999/hook"}`)
	f, err := NewDuplicate(cfg)
	if err != nil {
		t.Fatalf("NewDuplicate: %v", err)
	}

	result, err := f.Execute(context.Background(), unitEmail())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
	if !strings.Contains(result.Log.Detail, "webhook:") {
		t.Errorf("expected log detail to mention webhook, got %q", result.Log.Detail)
	}
}

func TestUnitDuplicate_BothWebhookAndQueue(t *testing.T) {
	cfg := json.RawMessage(`{
		"webhook_url": "http://192.0.2.1:9999/hook",
		"queue_recipient": "backup@example.com"
	}`)
	f, err := NewDuplicate(cfg)
	if err != nil {
		t.Fatalf("NewDuplicate: %v", err)
	}

	email := unitEmail()
	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
	if !strings.Contains(result.Log.Detail, "webhook:") {
		t.Errorf("expected webhook in log detail, got %q", result.Log.Detail)
	}
	if !strings.Contains(result.Log.Detail, "queue:") {
		t.Errorf("expected queue in log detail, got %q", result.Log.Detail)
	}
	if email.Metadata["duplicate_queue_recipient"] != "backup@example.com" {
		t.Errorf("expected metadata set for queue recipient")
	}
}

func TestUnitDuplicate_NameAndType(t *testing.T) {
	cfg := json.RawMessage(`{"queue_recipient": "x@y.com"}`)
	f, err := NewDuplicate(cfg)
	if err != nil {
		t.Fatalf("NewDuplicate: %v", err)
	}
	if f.Name() != "duplicate" {
		t.Errorf("expected name 'duplicate', got %q", f.Name())
	}
	if f.Type() != pipeline.FilterTypeAction {
		t.Errorf("expected type 'action', got %q", f.Type())
	}
}

func TestUnitDuplicate_DefaultMethod(t *testing.T) {
	cfg := json.RawMessage(`{"webhook_url": "http://192.0.2.1:9999/hook"}`)
	f, err := NewDuplicate(cfg)
	if err != nil {
		t.Fatalf("NewDuplicate: %v", err)
	}

	// We can't inspect the private field directly, but the filter should
	// be created successfully with the default POST method.
	if f.Name() != "duplicate" {
		t.Errorf("filter should be created with default method")
	}
}

// ═════════════════════════════════════════════════════════════════════
// DomainAllowlist filter -- requires DB, tested minimally
// ═════════════════════════════════════════════════════════════════════

func TestUnitDomainAllowlist_FactoryCreation(t *testing.T) {
	// Verify the factory function works without a DB (passes nil).
	// The filter is created; it will fail at Execute time when DB calls are made.
	factory := NewDomainAllowlist(nil)
	f, err := factory(nil)
	if err != nil {
		t.Fatalf("NewDomainAllowlist factory: %v", err)
	}
	if f.Name() != "domain_allowlist" {
		t.Errorf("expected name 'domain_allowlist', got %q", f.Name())
	}
	if f.Type() != pipeline.FilterTypeAction {
		t.Errorf("expected type 'action', got %q", f.Type())
	}
}

func TestUnitDomainAllowlist_NoSender_Continues(t *testing.T) {
	// When no sender is present, the filter should continue (no rules to check).
	factory := NewDomainAllowlist(nil)
	f, err := factory(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	email := unitEmail()
	email.Envelope.MailFrom = ""
	email.Headers.From = nil

	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue for no sender, got %q", result.Action)
	}
	if result.Log.Detail != "no sender" {
		t.Errorf("expected detail 'no sender', got %q", result.Log.Detail)
	}
}

func TestUnitDomainAllowlist_CustomConfig(t *testing.T) {
	cfg := json.RawMessage(`{
		"on_allow": "continue_skip_spam",
		"on_block": "reject",
		"reject_message": "550 Custom block message"
	}`)
	factory := NewDomainAllowlist(nil)
	f, err := factory(cfg)
	if err != nil {
		t.Fatalf("factory with config: %v", err)
	}
	if f.Name() != "domain_allowlist" {
		t.Errorf("expected name 'domain_allowlist', got %q", f.Name())
	}
}

// ═════════════════════════════════════════════════════════════════════
// RecipientCheck filter -- requires DB, tested minimally
// ═════════════════════════════════════════════════════════════════════

func TestUnitRecipientCheck_FactoryCreation(t *testing.T) {
	factory := NewRecipientCheck(nil)
	f, err := factory(nil)
	if err != nil {
		t.Fatalf("NewRecipientCheck factory: %v", err)
	}
	if f.Name() != "recipient_check" {
		t.Errorf("expected name 'recipient_check', got %q", f.Name())
	}
	if f.Type() != pipeline.FilterTypeAction {
		t.Errorf("expected type 'action', got %q", f.Type())
	}
}
