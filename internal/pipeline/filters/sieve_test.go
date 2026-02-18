package filters

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// ── Helpers ──────────────────────────────────────────────────────────

func sieveEmail() *pipeline.EmailJSON {
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
			Content:     "Hello, this is a test message body.",
		},
	}
}

func runSieve(t *testing.T, script string, email *pipeline.EmailJSON) *pipeline.FilterResult {
	t.Helper()
	cfg, _ := json.Marshal(sieveConfig{Script: script})
	f, err := NewSieve(cfg)
	if err != nil {
		t.Fatalf("NewSieve: %v", err)
	}
	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return result
}

// ── Existing feature regression tests ────────────────────────────────

func TestSieve_NoScript(t *testing.T) {
	f, err := NewSieve(nil)
	if err != nil {
		t.Fatalf("NewSieve: %v", err)
	}
	result, err := f.Execute(context.Background(), sieveEmail())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", result.Action)
	}
}

func TestSieve_HeaderContains(t *testing.T) {
	script := `require "fileinto";
if header :contains "Subject" "Test" {
  fileinto "TestFolder";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Action != pipeline.ActionContinue {
		t.Fatalf("expected ActionContinue, got %q", result.Action)
	}
	if result.Message == nil {
		t.Fatal("expected non-nil message")
	}
	if result.Message.Metadata["deliver_to_folder"] != "TestFolder" {
		t.Errorf("expected deliver_to_folder=TestFolder, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_Discard(t *testing.T) {
	script := `if header :is "Subject" "Test message" {
  discard;
}`
	result := runSieve(t, script, sieveEmail())
	if result.Action != pipeline.ActionDiscard {
		t.Errorf("expected ActionDiscard, got %q", result.Action)
	}
}

func TestSieve_Reject(t *testing.T) {
	script := `if header :is "Subject" "Test message" {
  reject "Not accepted";
}`
	result := runSieve(t, script, sieveEmail())
	if result.Action != pipeline.ActionReject {
		t.Errorf("expected ActionReject, got %q", result.Action)
	}
	if result.RejectMsg != "550 Not accepted" {
		t.Errorf("expected reject msg '550 Not accepted', got %q", result.RejectMsg)
	}
}

func TestSieve_Redirect(t *testing.T) {
	script := `if header :contains "Subject" "Test" {
  redirect "other@example.com";
}`
	result := runSieve(t, script, sieveEmail())
	if result.Action != pipeline.ActionContinue {
		t.Fatalf("expected ActionContinue, got %q", result.Action)
	}
	if result.Message.Metadata["redirect_to"] != "other@example.com" {
		t.Errorf("expected redirect_to=other@example.com, got %q", result.Message.Metadata["redirect_to"])
	}
}

// ── Body extension tests ─────────────────────────────────────────────

func TestSieve_BodyContains(t *testing.T) {
	script := `require "body";
if body :contains "test message" {
  fileinto "BodyMatch";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Action != pipeline.ActionContinue {
		t.Fatalf("expected ActionContinue, got %q", result.Action)
	}
	if result.Message.Metadata["deliver_to_folder"] != "BodyMatch" {
		t.Errorf("expected deliver_to_folder=BodyMatch, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_BodyContains_NoMatch(t *testing.T) {
	script := `require "body";
if body :contains "nonexistent phrase" {
  fileinto "ShouldNotMatch";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message != nil && result.Message.Metadata["deliver_to_folder"] == "ShouldNotMatch" {
		t.Error("body :contains should not have matched")
	}
}

func TestSieve_BodyContains_HTMLStripped(t *testing.T) {
	script := `require "body";
if body :contains "important" {
  fileinto "HTMLMatch";
}`
	email := sieveEmail()
	email.Body.ContentType = "text/html"
	email.Body.Content = "<html><body><b>This is important</b> content.</body></html>"

	result := runSieve(t, script, email)
	if result.Action != pipeline.ActionContinue {
		t.Fatalf("expected ActionContinue, got %q", result.Action)
	}
	if result.Message.Metadata["deliver_to_folder"] != "HTMLMatch" {
		t.Errorf("expected deliver_to_folder=HTMLMatch, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_BodyContains_MultipartPlainPreferred(t *testing.T) {
	script := `require "body";
if body :contains "plain text content" {
  fileinto "PlainMatch";
}`
	email := sieveEmail()
	email.Body.ContentType = "multipart/alternative"
	email.Body.Content = ""
	email.Body.Parts = []pipeline.Body{
		{ContentType: "text/plain", Content: "This is plain text content."},
		{ContentType: "text/html", Content: "<p>This is HTML content.</p>"},
	}

	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "PlainMatch" {
		t.Errorf("expected body match on text/plain part, got folder=%q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_BodyIs(t *testing.T) {
	script := `require "body";
if body :is "Hello, this is a test message body." {
  fileinto "ExactMatch";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "ExactMatch" {
		t.Errorf("expected deliver_to_folder=ExactMatch, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

// ── Regex extension tests ────────────────────────────────────────────

func TestSieve_HeaderRegex(t *testing.T) {
	script := `require "regex";
if header :regex "Subject" ".*[Tt]est.*" {
  fileinto "RegexMatch";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "RegexMatch" {
		t.Errorf("expected deliver_to_folder=RegexMatch, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_HeaderRegex_NoMatch(t *testing.T) {
	script := `require "regex";
if header :regex "Subject" "^URGENT:.*" {
  fileinto "UrgentFolder";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message != nil && result.Message.Metadata["deliver_to_folder"] == "UrgentFolder" {
		t.Error("regex should not have matched")
	}
}

func TestSieve_HeaderRegex_CaseInsensitive(t *testing.T) {
	script := `require "regex";
if header :regex "Subject" "^test message$" {
  fileinto "CaseInsensitive";
}`
	email := sieveEmail()
	email.Headers.Subject = "Test Message"
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "CaseInsensitive" {
		t.Errorf("expected case-insensitive regex match, got folder=%q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_BodyRegex(t *testing.T) {
	script := `require ["body", "regex"];
if body :regex "test.*body" {
  fileinto "BodyRegex";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "BodyRegex" {
		t.Errorf("expected deliver_to_folder=BodyRegex, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_AddressRegex(t *testing.T) {
	script := `require "regex";
if address :regex "From" "sender@.*\.com" {
  fileinto "AddressRegex";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "AddressRegex" {
		t.Errorf("expected deliver_to_folder=AddressRegex, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_InvalidRegex_Skipped(t *testing.T) {
	script := `require "regex";
if header :regex "Subject" "[invalid" {
  fileinto "ShouldNotMatch";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	// Invalid regex should not cause an error, just not match.
	if result.Message != nil && result.Message.Metadata["deliver_to_folder"] == "ShouldNotMatch" {
		t.Error("invalid regex should not match")
	}
}

// ── Envelope extension tests ─────────────────────────────────────────

func TestSieve_EnvelopeFrom_Is(t *testing.T) {
	script := `require "envelope";
if envelope :is "from" "sender@example.com" {
  fileinto "EnvelopeMatch";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "EnvelopeMatch" {
		t.Errorf("expected deliver_to_folder=EnvelopeMatch, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_EnvelopeTo_Is(t *testing.T) {
	script := `require "envelope";
if envelope :is "to" "recipient@example.com" {
  fileinto "EnvelopeTo";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "EnvelopeTo" {
		t.Errorf("expected deliver_to_folder=EnvelopeTo, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_EnvelopeFrom_Contains(t *testing.T) {
	script := `require "envelope";
if envelope :contains "from" "example.com" {
  fileinto "EnvelopeDomain";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "EnvelopeDomain" {
		t.Errorf("expected deliver_to_folder=EnvelopeDomain, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_EnvelopeFrom_NoMatch(t *testing.T) {
	script := `require "envelope";
if envelope :is "from" "other@example.com" {
  fileinto "ShouldNotMatch";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message != nil && result.Message.Metadata["deliver_to_folder"] == "ShouldNotMatch" {
		t.Error("envelope :is should not have matched")
	}
}

func TestSieve_EnvelopeFrom_Metadata(t *testing.T) {
	script := `require "envelope";
if envelope :is "from" "gateway-sender@example.com" {
  fileinto "MetadataEnvelope";
}`
	email := sieveEmail()
	email.Metadata = map[string]string{
		"envelope_from": "gateway-sender@example.com",
	}
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "MetadataEnvelope" {
		t.Errorf("expected deliver_to_folder=MetadataEnvelope, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_EnvelopeTo_Metadata(t *testing.T) {
	script := `require "envelope";
if envelope :is "to" "special-rcpt@example.com" {
  fileinto "MetadataEnvelopeTo";
}`
	email := sieveEmail()
	email.Metadata = map[string]string{
		"envelope_to": "special-rcpt@example.com",
	}
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "MetadataEnvelopeTo" {
		t.Errorf("expected deliver_to_folder=MetadataEnvelopeTo, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

func TestSieve_EnvelopeRegex(t *testing.T) {
	script := `require ["envelope", "regex"];
if envelope :regex "from" ".*@example\.com" {
  fileinto "EnvelopeRegex";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "EnvelopeRegex" {
		t.Errorf("expected deliver_to_folder=EnvelopeRegex, got %q", result.Message.Metadata["deliver_to_folder"])
	}
}

// ── Vacation extension tests ─────────────────────────────────────────

func TestSieve_Vacation_Basic(t *testing.T) {
	script := `require "vacation";
vacation :days 7 :subject "Out of Office" "I am currently on vacation.";`
	email := sieveEmail()
	result := runSieve(t, script, email)

	if result.Action != pipeline.ActionContinue {
		t.Fatalf("expected ActionContinue, got %q", result.Action)
	}
	if result.Message == nil {
		t.Fatal("expected non-nil message")
	}
	m := result.Message.Metadata
	if m["vacation_reply_to"] != "sender@example.com" {
		t.Errorf("expected vacation_reply_to=sender@example.com, got %q", m["vacation_reply_to"])
	}
	if m["vacation_reply_subject"] != "Out of Office" {
		t.Errorf("expected vacation_reply_subject='Out of Office', got %q", m["vacation_reply_subject"])
	}
	if m["vacation_reply_body"] != "I am currently on vacation." {
		t.Errorf("expected vacation_reply_body='I am currently on vacation.', got %q", m["vacation_reply_body"])
	}
	if m["vacation_days"] != "7" {
		t.Errorf("expected vacation_days=7, got %q", m["vacation_days"])
	}
}

func TestSieve_Vacation_DefaultDays(t *testing.T) {
	script := `require "vacation";
vacation :subject "Away" "Gone fishing.";`
	email := sieveEmail()
	result := runSieve(t, script, email)
	m := result.Message.Metadata
	if m["vacation_days"] != "7" {
		t.Errorf("expected default vacation_days=7, got %q", m["vacation_days"])
	}
}

func TestSieve_Vacation_Conditional(t *testing.T) {
	script := `require "vacation";
if header :contains "Subject" "Test" {
  vacation :days 3 :subject "Auto-reply" "Got your test message.";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)

	if result.Message == nil {
		t.Fatal("expected non-nil message")
	}
	m := result.Message.Metadata
	if m["vacation_reply_body"] != "Got your test message." {
		t.Errorf("expected vacation body, got %q", m["vacation_reply_body"])
	}
	if m["vacation_days"] != "3" {
		t.Errorf("expected vacation_days=3, got %q", m["vacation_days"])
	}
}

func TestSieve_Vacation_DedupKey(t *testing.T) {
	script := `require "vacation";
vacation :days 7 :subject "OOO" "Away.";`
	email := sieveEmail()

	// First run: should set vacation metadata.
	result := runSieve(t, script, email)
	if result.Message.Metadata["vacation_reply_to"] != "sender@example.com" {
		t.Errorf("first run: expected vacation_reply_to set, got %q", result.Message.Metadata["vacation_reply_to"])
	}

	// Verify the dedup key was set.
	dedupKey := vacationDedupKey("sender@example.com")
	lastSentKey := "vacation_last_sent_" + dedupKey
	if result.Message.Metadata[lastSentKey] != "pending" {
		t.Errorf("expected dedup key %q=pending, got %q", lastSentKey, result.Message.Metadata[lastSentKey])
	}
}

func TestSieve_Vacation_UsesEnvelopeSender(t *testing.T) {
	script := `require "vacation";
vacation :subject "Out" "Away.";`
	email := sieveEmail()
	email.Envelope.MailFrom = "envelope-sender@test.com"
	result := runSieve(t, script, email)
	if result.Message.Metadata["vacation_reply_to"] != "envelope-sender@test.com" {
		t.Errorf("expected vacation_reply_to from envelope, got %q", result.Message.Metadata["vacation_reply_to"])
	}
}

func TestSieve_Vacation_MultiLine(t *testing.T) {
	script := `require "vacation";
vacation :days 14
  :subject "On Leave"
  "I will be out of office until March 1st.";`
	email := sieveEmail()
	result := runSieve(t, script, email)
	m := result.Message.Metadata
	if m["vacation_reply_subject"] != "On Leave" {
		t.Errorf("expected subject 'On Leave', got %q", m["vacation_reply_subject"])
	}
	if m["vacation_reply_body"] != "I will be out of office until March 1st." {
		t.Errorf("expected body about March, got %q", m["vacation_reply_body"])
	}
	if m["vacation_days"] != "14" {
		t.Errorf("expected vacation_days=14, got %q", m["vacation_days"])
	}
}

// ── Notify extension tests ───────────────────────────────────────────

func TestSieve_Notify_Basic(t *testing.T) {
	script := `require "notify";
notify :method "mailto:admin@example.com" :message "New mail received";`
	email := sieveEmail()
	result := runSieve(t, script, email)

	if result.Action != pipeline.ActionContinue {
		t.Fatalf("expected ActionContinue, got %q", result.Action)
	}
	if result.Message == nil {
		t.Fatal("expected non-nil message")
	}
	m := result.Message.Metadata
	if m["notify_method"] != "mailto:admin@example.com" {
		t.Errorf("expected notify_method=mailto:admin@example.com, got %q", m["notify_method"])
	}
	if m["notify_message"] != "New mail received" {
		t.Errorf("expected notify_message='New mail received', got %q", m["notify_message"])
	}
}

func TestSieve_Notify_Conditional(t *testing.T) {
	script := `require "notify";
if header :contains "Subject" "URGENT" {
  notify :method "mailto:oncall@example.com" :message "Urgent mail arrived";
}`
	email := sieveEmail()
	email.Headers.Subject = "URGENT: Server down"
	result := runSieve(t, script, email)

	m := result.Message.Metadata
	if m["notify_method"] != "mailto:oncall@example.com" {
		t.Errorf("expected notify for urgent, got %q", m["notify_method"])
	}
}

func TestSieve_Notify_NoMatch(t *testing.T) {
	script := `require "notify";
if header :contains "Subject" "URGENT" {
  notify :method "mailto:oncall@example.com" :message "Urgent mail arrived";
}`
	email := sieveEmail() // Subject is "Test message", no match
	result := runSieve(t, script, email)

	if result.Message != nil && result.Message.Metadata["notify_method"] != "" {
		t.Error("notify should not have been set for non-matching condition")
	}
}

func TestSieve_Notify_MultiLine(t *testing.T) {
	script := `require "notify";
notify :method "mailto:alerts@example.com"
  :message "Alert: new message arrived";`
	email := sieveEmail()
	result := runSieve(t, script, email)
	m := result.Message.Metadata
	if m["notify_method"] != "mailto:alerts@example.com" {
		t.Errorf("expected notify_method from multi-line, got %q", m["notify_method"])
	}
	if m["notify_message"] != "Alert: new message arrived" {
		t.Errorf("expected notify_message from multi-line, got %q", m["notify_message"])
	}
}

// ── Combined extension tests ─────────────────────────────────────────

func TestSieve_CombinedExtensions(t *testing.T) {
	script := `require ["vacation", "notify", "body", "envelope"];
if body :contains "urgent" {
  notify :method "mailto:admin@example.com" :message "Urgent body detected";
}
if envelope :is "from" "vip@important.com" {
  fileinto "VIP";
}
vacation :days 5 :subject "Away" "On vacation.";`

	email := sieveEmail()
	email.Body.Content = "This is an urgent request."
	email.Envelope.MailFrom = "vip@important.com"

	result := runSieve(t, script, email)
	m := result.Message.Metadata

	// Notify should be set from body match.
	if m["notify_method"] != "mailto:admin@example.com" {
		t.Errorf("expected notify from body match, got %q", m["notify_method"])
	}
	// Fileinto should be set from envelope match.
	if m["deliver_to_folder"] != "VIP" {
		t.Errorf("expected fileinto VIP from envelope, got %q", m["deliver_to_folder"])
	}
	// Vacation should be set.
	if m["vacation_reply_to"] != "vip@important.com" {
		t.Errorf("expected vacation_reply_to, got %q", m["vacation_reply_to"])
	}
}

func TestSieve_Stop(t *testing.T) {
	script := `require "body";
if body :contains "test" {
  fileinto "First";
  stop;
}
if body :contains "test" {
  fileinto "Second";
}`
	email := sieveEmail()
	result := runSieve(t, script, email)
	if result.Message.Metadata["deliver_to_folder"] != "First" {
		t.Errorf("expected stop to prevent second rule, got folder=%q", result.Message.Metadata["deliver_to_folder"])
	}
}

// ── extractBodyText tests ────────────────────────────────────────────

func TestExtractBodyText_PlainText(t *testing.T) {
	email := &pipeline.EmailJSON{
		Body: pipeline.Body{
			ContentType: "text/plain",
			Content:     "Hello world",
		},
	}
	got := extractBodyText(email)
	if got != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", got)
	}
}

func TestExtractBodyText_HTML(t *testing.T) {
	email := &pipeline.EmailJSON{
		Body: pipeline.Body{
			ContentType: "text/html",
			Content:     "<p>Hello <b>world</b></p>",
		},
	}
	got := extractBodyText(email)
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "world") {
		t.Errorf("expected stripped HTML to contain 'Hello' and 'world', got %q", got)
	}
}

func TestExtractBodyText_Multipart(t *testing.T) {
	email := &pipeline.EmailJSON{
		Body: pipeline.Body{
			ContentType: "multipart/alternative",
			Parts: []pipeline.Body{
				{ContentType: "text/plain", Content: "Plain version"},
				{ContentType: "text/html", Content: "<p>HTML version</p>"},
			},
		},
	}
	got := extractBodyText(email)
	if got != "Plain version" {
		t.Errorf("expected 'Plain version' (prefer text/plain), got %q", got)
	}
}

func TestExtractBodyText_MultipartHTMLOnly(t *testing.T) {
	email := &pipeline.EmailJSON{
		Body: pipeline.Body{
			ContentType: "multipart/alternative",
			Parts: []pipeline.Body{
				{ContentType: "text/html", Content: "<p>Only HTML</p>"},
			},
		},
	}
	got := extractBodyText(email)
	if !strings.Contains(got, "Only HTML") {
		t.Errorf("expected stripped HTML content, got %q", got)
	}
}

// ── stripHTMLTags tests ──────────────────────────────────────────────

func TestStripHTMLTags(t *testing.T) {
	tests := []struct {
		input    string
		contains string
	}{
		{"<p>Hello</p>", "Hello"},
		{"<b>Bold</b> and <i>italic</i>", "Bold"},
		{"No tags at all", "No tags at all"},
		{"<div><span>Nested</span></div>", "Nested"},
	}
	for _, tc := range tests {
		got := stripHTMLTags(tc.input)
		if !strings.Contains(got, tc.contains) {
			t.Errorf("stripHTMLTags(%q) = %q, expected to contain %q", tc.input, got, tc.contains)
		}
	}
}

// ── Validation tests ─────────────────────────────────────────────────

func TestValidateSieve_Valid(t *testing.T) {
	scripts := []string{
		`require "fileinto";
if header :contains "Subject" "test" {
  fileinto "Test";
}`,
		`require "vacation";
vacation :days 7 :subject "OOO" "Away.";`,
		`require "notify";
notify :method "mailto:a@b.com" :message "msg";`,
		`require ["body", "regex"];
if body :regex "test.*" {
  keep;
}`,
		`require "envelope";
if envelope :is "from" "a@b.com" {
  discard;
}`,
	}
	for _, s := range scripts {
		if err := ValidateSieve(s); err != nil {
			t.Errorf("ValidateSieve should accept valid script, got error: %v\nScript: %s", err, s)
		}
	}
}

// ── vacationDedupKey tests ───────────────────────────────────────────

func TestVacationDedupKey_Deterministic(t *testing.T) {
	k1 := vacationDedupKey("sender@example.com")
	k2 := vacationDedupKey("sender@example.com")
	if k1 != k2 {
		t.Errorf("expected deterministic dedup key, got %q and %q", k1, k2)
	}
}

func TestVacationDedupKey_CaseInsensitive(t *testing.T) {
	k1 := vacationDedupKey("Sender@Example.COM")
	k2 := vacationDedupKey("sender@example.com")
	if k1 != k2 {
		t.Errorf("expected case-insensitive dedup key, got %q and %q", k1, k2)
	}
}

func TestVacationDedupKey_Different(t *testing.T) {
	k1 := vacationDedupKey("alice@example.com")
	k2 := vacationDedupKey("bob@example.com")
	if k1 == k2 {
		t.Error("expected different dedup keys for different senders")
	}
}

