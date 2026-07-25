package filters

// The Sieve parser and interpreter live in github.com/rest-mail/go-sieve, together
// with the full evaluation-semantics test suite. The tests here cover what this
// filter owns: the pipeline wiring (NewSieve/Execute, FilterResult mapping for
// discard/reject), the metadata mapping of each action, and the app-specific
// vacation de-duplication.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rest-mail/go-sieve"
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

// folderOf returns the fileinto target recorded on a filter result, or "".
func folderOf(r *pipeline.FilterResult) string {
	if r == nil || r.Message == nil {
		return ""
	}
	return r.Message.Metadata["deliver_to_folder"]
}

// ── Filter wiring ────────────────────────────────────────────────────

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
	// go-sieve v0.2.0 enforces RFC 5228 require: the reject action (RFC 5429)
	// must be declared before use, otherwise the script is a parse error.
	script := `require "reject";
if header :is "Subject" "Test message" {
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
	if got := decodeRedirectTargets(t, result.Message.Metadata["redirect_to"]); len(got) != 1 || got[0] != "other@example.com" {
		t.Errorf("expected redirect_to=[other@example.com], got %v", got)
	}
}

// decodeRedirectTargets parses the JSON-array redirect_to metadata the sieve
// filter records for the delivery path.
func decodeRedirectTargets(t *testing.T, raw string) []string {
	t.Helper()
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode redirect_to %q: %v", raw, err)
	}
	return out
}

// ── fileinto :create ─────────────────────────────────────────────────

func TestSieve_FileintoCreate(t *testing.T) {
	script := `require ["fileinto", "mailbox"];
if header :contains "Subject" "test" { fileinto :create "Archive/2026"; }`
	r := runSieve(t, script, sieveEmail())
	if folderOf(r) != "Archive/2026" {
		t.Errorf("expected folder Archive/2026, got %q", folderOf(r))
	}
	if r.Message.Metadata["deliver_to_folder_create"] != "true" {
		t.Errorf("expected deliver_to_folder_create=true, got %q", r.Message.Metadata["deliver_to_folder_create"])
	}
}

// ── imap4flags ───────────────────────────────────────────────────────

func TestSieve_SetAndAddFlag(t *testing.T) {
	script := `require "imap4flags";
if true {
  setflag "work";
  addflag "urgent";
  addflag "work";
}`
	r := runSieve(t, script, sieveEmail())
	if got := r.Message.Metadata["imap_flags"]; got != "work urgent" {
		t.Errorf("expected flags 'work urgent' (deduped), got %q", got)
	}
}

func TestSieve_RemoveFlag(t *testing.T) {
	script := `require "imap4flags";
if true {
  setflag ["a", "b", "c"];
  removeflag "b";
}`
	r := runSieve(t, script, sieveEmail())
	if got := r.Message.Metadata["imap_flags"]; got != "a c" {
		t.Errorf("expected flags 'a c' after removeflag, got %q", got)
	}
}

func TestSieve_SetFlagEscaped(t *testing.T) {
	// "\\Seen" in source is the IMAP system flag \Seen.
	script := `require "imap4flags";
if true { setflag "\\Seen"; }`
	r := runSieve(t, script, sieveEmail())
	if got := r.Message.Metadata["imap_flags"]; got != `\Seen` {
		t.Errorf(`expected flag '\Seen', got %q`, got)
	}
}

// ── Vacation metadata mapping and de-duplication ─────────────────────

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

// ── Notify metadata mapping ──────────────────────────────────────────

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

// ── Validation wrapper ───────────────────────────────────────────────

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

func TestValidateSieve_Invalid(t *testing.T) {
	if err := ValidateSieve(`if true { keep;`); err == nil {
		t.Error("expected ValidateSieve to reject an unterminated block")
	}
}

// ── RFC 5228 delivery outcome (go-sieve v0.3.0 Outcome contract) ─────
//
// go-sieve v0.3.0 returns an Outcome carrying the RFC 5228 §2.10.2 implicit
// keep (ImplicitKeep) and a §2.10.6 fail-safe error (Error). These tests pin
// the four delivery cases the host must honour: implicit-keep → INBOX,
// discard → dropped, fileinto → folder, and runtime error → kept.

// TestSieve_ImplicitKeep_DeliversToInbox: a script that matches nothing (takes
// no delivering action) leaves the implicit keep in effect, so the message is
// delivered to the default mailbox — ActionContinue with no folder override.
func TestSieve_ImplicitKeep_DeliversToInbox(t *testing.T) {
	script := `require "fileinto";
if header :contains "Subject" "ZZZ-NO-SUCH-SUBJECT" { fileinto "Junk"; }`
	r := runSieve(t, script, sieveEmail())
	if r.Action != pipeline.ActionContinue {
		t.Fatalf("expected ActionContinue (implicit keep), got %q", r.Action)
	}
	if folderOf(r) != "" {
		t.Errorf("implicit keep must deliver to INBOX, but a folder override was set: %q", folderOf(r))
	}
	if r.Log.Detail != "implicit keep -> INBOX" {
		t.Errorf("expected implicit-keep detail, got %q", r.Log.Detail)
	}
}

// TestSieve_NonDeliveringActionStillImplicitKeeps: a non-delivering action
// (addflag) does NOT cancel the implicit keep — the message still lands in
// INBOX (no folder override), it just carries the flag.
func TestSieve_NonDeliveringActionStillImplicitKeeps(t *testing.T) {
	script := `require "imap4flags";
if true { addflag "\\Seen"; }`
	r := runSieve(t, script, sieveEmail())
	if r.Action != pipeline.ActionContinue {
		t.Fatalf("expected ActionContinue, got %q", r.Action)
	}
	if folderOf(r) != "" {
		t.Errorf("addflag must not divert delivery; got folder %q", folderOf(r))
	}
	if got := r.Message.Metadata["imap_flags"]; got != `\Seen` {
		t.Errorf("expected imap_flags=\\Seen, got %q", got)
	}
}

// TestSieve_BareDiscard_Drops: a bare `discard` cancels the implicit keep and
// drops the message.
func TestSieve_BareDiscard_Drops(t *testing.T) {
	r := runSieve(t, `discard;`, sieveEmail())
	if r.Action != pipeline.ActionDiscard {
		t.Errorf("expected ActionDiscard for bare discard, got %q", r.Action)
	}
}

// TestSieve_EvaluationError_KeepsFailSafe: a runtime evaluation error (reported
// by go-sieve as Continue+ImplicitKeep+Error) must fail safe to a keep — the
// message is delivered, never lost. sieveResult is exercised directly because a
// runtime error requires an Executor callback to panic inside the library,
// which the filter's internal executor never does under normal input.
func TestSieve_EvaluationError_KeepsFailSafe(t *testing.T) {
	msg := sieveEmail()
	outcome := sieve.Outcome{
		Disposition:  sieve.Continue,
		ImplicitKeep: true,
		Error:        errors.New("executor callback panicked"),
	}
	r := sieveResult(outcome, nil, false, msg)
	if r.Action != pipeline.ActionContinue {
		t.Fatalf("runtime error must fail safe to keep (ActionContinue), got %q", r.Action)
	}
	if r.Message != msg {
		t.Error("kept message must be delivered (Message should be set)")
	}
	if folderOf(r) != "" {
		t.Errorf("fail-safe keep goes to INBOX, not a folder; got %q", folderOf(r))
	}
	if r.Log.Result != "kept" {
		t.Errorf("expected Result=kept on fail-safe, got %q", r.Log.Result)
	}
}

// TestSieve_Vacation_IntervalDays: the go-sieve v0.3.0 Vacation.Interval
// duration (:days 3 → 72h) is mapped back to whole days in metadata.
func TestSieve_Vacation_IntervalDays(t *testing.T) {
	script := `require "vacation";
vacation :days 3 :subject "OOO" "Away.";`
	r := runSieve(t, script, sieveEmail())
	if got := r.Message.Metadata["vacation_days"]; got != "3" {
		t.Errorf("expected vacation_days=3 from :days 3 interval, got %q", got)
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
