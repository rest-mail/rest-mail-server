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

	"github.com/rest-mail/go-sieve/pkg/sieve"
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
	if m["vacation_seconds"] != "604800" || len(m["vacation_handle"]) != 32 {
		t.Errorf("expected vacation_seconds=604800 (7 days) and a handle, got %q and %q", m["vacation_seconds"], m["vacation_handle"])
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
	dedupKey := vacationDedupKey("sender@example.com", "")
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

// go-sieve v0.4.0 replies to the envelope sender only: with no reverse-path
// (a bounce) there is no reply, and the From header is never used instead.
func TestSieve_Vacation_NoEnvelopeSenderNoReply(t *testing.T) {
	script := `require "vacation";
vacation :subject "Out" "Away.";`
	email := sieveEmail()
	email.Envelope.MailFrom = ""
	result := runSieve(t, script, email)
	if got, ok := result.Message.Metadata["vacation_reply_to"]; ok {
		t.Errorf("expected no vacation reply without an envelope sender, got vacation_reply_to=%q", got)
	}
}

// List mail draws no vacation reply (RFC 5230 §5.6), enforced by go-sieve.
func TestSieve_Vacation_ListMailNoReply(t *testing.T) {
	script := `require "vacation";
vacation :subject "Out" "Away.";`
	email := sieveEmail()
	email.Headers.Raw = map[string][]string{"List-Id": {"<announce.example.com>"}}
	result := runSieve(t, script, email)
	if got, ok := result.Message.Metadata["vacation_reply_to"]; ok {
		t.Errorf("expected no vacation reply to list mail, got vacation_reply_to=%q", got)
	}
}

// The :handle is part of the reply-tracking identity (RFC 5230 §4.2), and the
// :from and :mime arguments reach the reply metadata.
func TestSieve_Vacation_HandleFromMIME(t *testing.T) {
	script := "require \"vacation\";\r\n" +
		"vacation :handle \"trip\" :from \"me@example.com\" :mime \"Content-Type: text/plain\r\n\r\nAway.\";"
	result := runSieve(t, script, sieveEmail())
	m := result.Message.Metadata
	if m["vacation_reply_from"] != "me@example.com" {
		t.Errorf("vacation_reply_from = %q, want me@example.com", m["vacation_reply_from"])
	}
	if m["vacation_reply_mime"] != "true" {
		t.Errorf("vacation_reply_mime = %q, want true", m["vacation_reply_mime"])
	}
	if _, ok := m["vacation_reply_subject"]; ok {
		t.Errorf("no :subject given, so vacation_reply_subject must be unset, got %q", m["vacation_reply_subject"])
	}
	if m["vacation_last_sent_"+vacationDedupKey("sender@example.com", "trip")] != "pending" {
		t.Error("expected the dedup key to include the :handle")
	}
	if vacationDedupKey("sender@example.com", "trip") == vacationDedupKey("sender@example.com", "") {
		t.Error("different handles must give different dedup keys")
	}
}

func TestSieve_Ereject(t *testing.T) {
	script := `require "ereject";
ereject "Go away";`
	result := runSieve(t, script, sieveEmail())
	if result.Action != pipeline.ActionReject {
		t.Fatalf("expected ActionReject, got %q", result.Action)
	}
	if result.RejectMsg != "550 Go away" {
		t.Errorf("RejectMsg = %q, want %q", result.RejectMsg, "550 Go away")
	}
	if result.Log.Detail != "sieve ereject: Go away" {
		t.Errorf("Log.Detail = %q, want %q", result.Log.Detail, "sieve ereject: Go away")
	}
}

func TestValidateSieve_V040Rules(t *testing.T) {
	for _, script := range []string{
		`keep "x";`,
		`require "reject"; reject;`,
		`require ["vacation", "reject"]; reject "no"; vacation "Away.";`,
		`require "vacation"; vacation :days 7;`,
	} {
		if err := ValidateSieve(script); err == nil {
			t.Errorf("ValidateSieve(%q) = nil, want an error", script)
		}
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
	r := sieveResult(outcome, nil, msg)
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
	// The interval is kept exactly, in seconds (RFC 5230 :days, RFC 6131 :seconds, #748).
	if got := r.Message.Metadata["vacation_seconds"]; got != "259200" {
		t.Errorf("expected vacation_seconds=259200 from :days 3, got %q", got)
	}
}

// ── vacationDedupKey tests ───────────────────────────────────────────

func TestVacationDedupKey_Deterministic(t *testing.T) {
	k1 := vacationDedupKey("sender@example.com", "")
	k2 := vacationDedupKey("sender@example.com", "")
	if k1 != k2 {
		t.Errorf("expected deterministic dedup key, got %q and %q", k1, k2)
	}
}

func TestVacationDedupKey_CaseInsensitive(t *testing.T) {
	k1 := vacationDedupKey("Sender@Example.COM", "")
	k2 := vacationDedupKey("sender@example.com", "")
	if k1 != k2 {
		t.Errorf("expected case-insensitive dedup key, got %q and %q", k1, k2)
	}
}

func TestVacationDedupKey_Different(t *testing.T) {
	k1 := vacationDedupKey("alice@example.com", "")
	k2 := vacationDedupKey("bob@example.com", "")
	if k1 == k2 {
		t.Error("expected different dedup keys for different senders")
	}
}
