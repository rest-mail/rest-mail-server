package filters

// Item 2 (issue #201): a Sieve script may select actions the delivery path never
// executes — they were recorded as metadata and silently no-op'd. Rather than
// accept a script we won't honour, reject those actions at install time so the
// user gets immediate, actionable feedback (fail-fast) instead of a silent
// no-op. `vacation` parses and installs today (then does nothing); `notify` is
// already rejected by the parser (enotify/notify unsupported), and this pins
// that both are refused at install.
//
// `redirect` is deliberately NOT rejected here: it is install-valid, governed by
// the OSI-13 redirect policy, and records redirect_to for a delivery consumer —
// see TestValidateSieveForInstall_Valid, which must keep passing.

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSieveForInstall_RejectsUnsupportedActions(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{"vacation", `require "vacation"; vacation "I am away";`},
		{"notify", `require "enotify"; notify "mailto:ops@example.com";`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSieveForInstall(tc.script)
			if err == nil {
				t.Fatalf("expected %s script to be rejected at install, got nil", tc.name)
			}
			var se *SieveInstallError
			if !errors.As(err, &se) {
				t.Fatalf("expected *SieveInstallError, got %T (%v)", err, err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.name) {
				t.Errorf("error should name the unsupported action %q: %q", tc.name, err.Error())
			}
		})
	}
}

// TestValidateSieveForInstall_SupportedActionsStillValid guards against
// over-rejection: the actions the pipeline DOES honour must keep installing.
func TestValidateSieveForInstall_SupportedActionsStillValid(t *testing.T) {
	valid := []string{
		`keep;`,
		`require "fileinto"; fileinto "Archive";`,
		`if header :contains "Subject" "x" { discard; }`,
		`redirect "ops@example.com";`,
		`require "imap4flags"; setflag "\\Seen";`,
	}
	for _, s := range valid {
		if err := ValidateSieveForInstall(s); err != nil {
			t.Errorf("supported script rejected at install: %v\nscript: %q", err, s)
		}
	}
}
