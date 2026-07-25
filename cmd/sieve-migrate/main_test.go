package main

import (
	"bytes"
	"strings"
	"testing"
)

// A script that uses fileinto without declaring it — the canonical broken case.
const missingFileintoScript = `if header :contains "subject" "sale" {
    fileinto "Junk";
}
`

func TestAnalyze_MissingFileinto_FailsThenRepairs(t *testing.T) {
	// 1. It must fail validation as-is (proves the bug the scanner exists to find).
	if err := validateScript(missingFileintoScript); err == nil {
		t.Fatalf("expected script missing require \"fileinto\" to fail validation, got nil")
	}

	a := analyze(missingFileintoScript)
	if a.OK {
		t.Fatalf("analyze reported OK for a script missing require \"fileinto\"")
	}
	if a.Err == nil {
		t.Fatalf("analyze did not surface the original validation error")
	}
	if !a.Repairable {
		t.Fatalf("expected the script to be auto-repairable")
	}

	// 2. The derived missing set must name fileinto.
	if got := strings.Join(a.Missing, ","); got != "fileinto" {
		t.Fatalf("missing extensions = %q, want %q", got, "fileinto")
	}

	// 3. The repaired script must add the require AND now parse.
	if !strings.Contains(a.Repaired, `require ["fileinto"]`) {
		t.Fatalf("repaired script does not contain the fileinto require:\n%s", a.Repaired)
	}
	if err := validateScript(a.Repaired); err != nil {
		t.Fatalf("repaired script still fails validation: %v\n%s", err, a.Repaired)
	}
}

func TestAnalyze_ValidScript_OK(t *testing.T) {
	valid := `require ["fileinto"];
if header :contains "subject" "sale" {
    fileinto "Junk";
}
`
	a := analyze(valid)
	if !a.OK {
		t.Fatalf("analyze reported not-OK for a valid script: %v", a.Err)
	}
	if a.Err != nil {
		t.Fatalf("valid script produced an error: %v", a.Err)
	}
}

func TestAnalyze_MultipleMissingExtensions_Repairs(t *testing.T) {
	// Uses fileinto AND imap4flags (setflag), neither declared.
	script := `if header :contains "from" "boss@example.com" {
    setflag "\\Flagged";
    fileinto "Important";
}
`
	a := analyze(script)
	if a.OK || !a.Repairable {
		t.Fatalf("expected repairable failure, got OK=%v Repairable=%v err=%v", a.OK, a.Repairable, a.Err)
	}
	joined := strings.Join(a.Missing, ",")
	if !strings.Contains(joined, "fileinto") || !strings.Contains(joined, "imap4flags") {
		t.Fatalf("missing set %q must contain both fileinto and imap4flags", joined)
	}
	if err := validateScript(a.Repaired); err != nil {
		t.Fatalf("repaired multi-extension script still fails: %v\n%s", err, a.Repaired)
	}
}

func TestAnalyze_GenuineSyntaxError_Unrepairable(t *testing.T) {
	// "kep" is not a command and is not an extension feature — adding a require
	// cannot fix it, so it must be reported as unrepairable, never silently
	// mangled.
	a := analyze("kep;\n")
	if a.OK {
		t.Fatalf("expected a genuine syntax error to fail validation")
	}
	if a.Repairable {
		t.Fatalf("a genuine syntax error must not be auto-repairable; got Repaired:\n%s", a.Repaired)
	}
}

func TestExtractMissingCap(t *testing.T) {
	cases := map[string]string{
		`sieve: line 2: the fileinto action requires "fileinto" to be declared with require`: "fileinto",
		`the i;ascii-numeric comparator requires "comparator-i;ascii-numeric" to be declared with require`: "comparator-i;ascii-numeric",
		`sieve: line 1: unknown command "kep" (extensions must be declared with require)`:                  "",
		`require of unsupported extension "spamtest"`:                                                       "",
	}
	for in, want := range cases {
		if got := extractMissingCap(errString(in)); got != want {
			t.Errorf("extractMissingCap(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── run() against a fake store ───────────────────────────────────────

type fakeStore struct {
	recs    []scriptRecord
	updates map[uint]string
}

func (f *fakeStore) List() ([]scriptRecord, error) { return f.recs, nil }
func (f *fakeStore) UpdateScript(id uint, s string) error {
	if f.updates == nil {
		f.updates = map[uint]string{}
	}
	f.updates[id] = s
	return nil
}

func newFakeStore() *fakeStore {
	return &fakeStore{recs: []scriptRecord{
		{ScriptID: 1, MailboxID: 10, Address: "alice@example.com", Script: `require ["fileinto"];` + "\nfileinto \"Ok\";\n"},
		{ScriptID: 2, MailboxID: 20, Address: "bob@example.com", Script: missingFileintoScript},
		{ScriptID: 3, MailboxID: 30, Address: "carol@example.com", Script: "kep;\n"},
	}}
}

func TestRun_DryRun_DoesNotWrite(t *testing.T) {
	store := newFakeStore()
	var out bytes.Buffer
	sum, err := run(store, false, &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.updates) != 0 {
		t.Fatalf("dry-run must not write; got %d updates", len(store.updates))
	}
	if sum.Total != 3 || sum.OK != 1 || sum.Failing != 2 || sum.Unrepairable != 1 || sum.Repaired != 0 {
		t.Fatalf("unexpected summary: %+v", sum)
	}
	if exitCode(sum, false) != 2 {
		t.Fatalf("dry-run with failures should exit 2, got %d", exitCode(sum, false))
	}
}

func TestRun_Repair_WritesRepairableOnly(t *testing.T) {
	store := newFakeStore()
	var out bytes.Buffer
	sum, err := run(store, true, &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, wrote := store.updates[3]; wrote {
		t.Fatalf("unrepairable script (id=3) must not be written")
	}
	repaired, wrote := store.updates[2]
	if !wrote {
		t.Fatalf("repairable script (id=2) was not written")
	}
	if err := validateScript(repaired); err != nil {
		t.Fatalf("written repair for id=2 does not validate: %v", err)
	}
	if sum.Repaired != 1 || sum.Unrepairable != 1 {
		t.Fatalf("unexpected summary: %+v", sum)
	}
	if exitCode(sum, true) != 2 {
		t.Fatalf("repair with an unrepairable remaining should exit 2, got %d", exitCode(sum, true))
	}
}

// errString adapts a plain string to an error for extractMissingCap tests.
type errString string

func (e errString) Error() string { return string(e) }
