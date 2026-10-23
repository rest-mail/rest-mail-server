package filters

// Tests for the install-time validation gate (ValidateSieveForInstall + the
// dry-run) and the delivery-time hash trust check in newSieveFilter. These run
// without a database — they exercise only the filter package's own logic.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// ── HashSieveScript ──────────────────────────────────────────────────

func TestHashSieveScript_MatchesSha256(t *testing.T) {
	script := `require "fileinto"; if header :contains "Subject" "x" { fileinto "X"; }`
	want := sha256.Sum256([]byte(script))
	got := HashSieveScript(script)
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("HashSieveScript = %q, want %q", got, hex.EncodeToString(want[:]))
	}
	if len(got) != 64 {
		t.Fatalf("expected 64 hex chars, got %d (%q)", len(got), got)
	}
	// Deterministic and input-sensitive.
	if HashSieveScript(script) != got {
		t.Error("HashSieveScript is not deterministic")
	}
	if HashSieveScript(script+" ") == got {
		t.Error("HashSieveScript should change when the bytes change")
	}
}

// ── ValidateSieveForInstall: parse stage ─────────────────────────────

// TestValidateSieveForInstall_MissingRequire proves an install of a script that
// uses fileinto WITHOUT declaring it is rejected, and that the error names the
// missing extension so the message is actionable.
func TestValidateSieveForInstall_MissingRequire(t *testing.T) {
	// fileinto is an extension action; using it without `require "fileinto"` is a
	// parse error under go-sieve's RFC 5228 require enforcement.
	err := ValidateSieveForInstall(`if header :contains "Subject" "x" { fileinto "Spam"; }`)
	if err == nil {
		t.Fatal("expected fileinto-without-require to be rejected at install, got nil")
	}
	var se *SieveInstallError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SieveInstallError, got %T (%v)", err, err)
	}
	if se.Stage != "parse" {
		t.Errorf("expected Stage=parse, got %q", se.Stage)
	}
	if se.MissingRequire != "fileinto" {
		t.Errorf("expected MissingRequire=fileinto, got %q", se.MissingRequire)
	}
	if !strings.Contains(err.Error(), "fileinto") || !strings.Contains(err.Error(), "require") {
		t.Errorf("error message not actionable: %q", err.Error())
	}
}

// ── ValidateSieveForInstall: dry-run stage ───────────────────────────

// TestValidateSieveForInstall_RedirectWithoutDomain proves a redirect to an
// address with no domain is refused at install. go-sieve (v0.5.0 on) refuses
// it when parsing (RFC 5228 §2.4.2.3), so it no longer reaches the dry run.
func TestValidateSieveForInstall_RedirectWithoutDomain(t *testing.T) {
	err := ValidateSieveForInstall(`redirect "bogus";`)
	if err == nil {
		t.Fatal("expected a redirect with no domain to be refused, got nil")
	}
	var se *SieveInstallError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SieveInstallError, got %T (%v)", err, err)
	}
	if se.Stage != "parse" {
		t.Errorf("expected Stage=parse, got %q", se.Stage)
	}
}

// TestDryRunExecutor_RedirectWithoutDomain keeps the dry run's own check
// proven now that the parser stops such a script first: the recording
// executor turns a redirect with no domain into the dry-run error.
func TestDryRunExecutor_RedirectWithoutDomain(t *testing.T) {
	exec := &recordingExecutor{}
	exec.Redirect("bogus", false)
	if exec.err == nil {
		t.Fatal("dry-run executor accepted a redirect with no domain")
	}
	exec = &recordingExecutor{}
	exec.Redirect("ops@example.com", false)
	if exec.err != nil {
		t.Fatalf("dry-run executor refused a valid redirect: %v", exec.err)
	}
}

func TestValidateSieveForInstall_Valid(t *testing.T) {
	valid := []string{
		"",
		`keep;`,
		`require "fileinto"; if header :contains "Subject" "x" { fileinto "X"; }`,
		`if header :contains "Subject" "x" { redirect "ops@example.com"; }`,
	}
	for _, s := range valid {
		if err := ValidateSieveForInstall(s); err != nil {
			t.Errorf("ValidateSieveForInstall rejected a valid script: %v\nscript: %q", err, s)
		}
	}
}

// ── Delivery-time hash trust ─────────────────────────────────────────

// newSieveFromConfig builds the filter the way the delivery path does: from a
// JSON config that may carry the validated script_hash + mailbox_id.
func newSieveFromConfig(t *testing.T, cfg sieveConfig) (pipeline.Filter, error) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return NewSieve(raw)
}

// TestSieveDelivery_HashMatchProceeds is delivery test (d): a stored script whose
// hash matches its bytes is trusted and runs.
func TestSieveDelivery_HashMatchProceeds(t *testing.T) {
	script := `keep;`
	cfg := sieveConfig{Script: script, ScriptHash: HashSieveScript(script), MailboxID: 7}
	f, err := newSieveFromConfig(t, cfg)
	if err != nil {
		t.Fatalf("matching hash should build the filter, got error: %v", err)
	}
	res, err := f.Execute(context.Background(), sieveEmail())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionContinue {
		t.Errorf("expected ActionContinue, got %q", res.Action)
	}
}

// TestSieveDelivery_HashMismatchFailsClosed is delivery test (e): a stored script
// mutated out-of-band so its bytes no longer match the recorded hash must fail
// closed (the filter refuses to build → the engine defers) AND log a clear
// warning naming the mailbox, rather than silently deferring.
func TestSieveDelivery_HashMismatchFailsClosed(t *testing.T) {
	original := `keep;`
	mutated := `discard;` // different bytes → different hash
	cfg := sieveConfig{
		Script:     mutated,
		ScriptHash: HashSieveScript(original), // stale hash from before the mutation
		MailboxID:  42,
	}

	// Capture the warning the filter emits via the package-global slog logger.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	f, err := newSieveFromConfig(t, cfg)
	if err == nil {
		t.Fatal("hash mismatch must fail closed (return an error), got nil")
	}
	if f != nil {
		t.Error("expected a nil filter on fail-closed")
	}

	logged := buf.String()
	if !strings.Contains(logged, "mailbox_id=42") {
		t.Errorf("warning should identify the mailbox, got: %q", logged)
	}
	if !strings.Contains(logged, "validated hash") {
		t.Errorf("warning should explain the hash no longer matches, got: %q", logged)
	}
	if !strings.Contains(strings.ToLower(logged), "warn") {
		t.Errorf("expected a WARN-level line, got: %q", logged)
	}
}

// TestSieveDelivery_NoHashSkipsCheck proves a domain-level config (empty
// script_hash) is unaffected: it parses and runs without a hash check.
func TestSieveDelivery_NoHashSkipsCheck(t *testing.T) {
	cfg := sieveConfig{Script: `keep;`} // no ScriptHash
	f, err := newSieveFromConfig(t, cfg)
	if err != nil {
		t.Fatalf("empty-hash config should build, got error: %v", err)
	}
	if _, err := f.Execute(context.Background(), sieveEmail()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}
