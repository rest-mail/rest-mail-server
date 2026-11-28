package server

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/go-pop3/pkg/pop3/internal/wire"
)

// syncBuffer is a goroutine-safe log sink. The session logs from its own
// goroutine while the test reads from the test goroutine, so the buffer must
// serialize access to stay clean under `go test -race`.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestPOP3_PassNotLoggedInCleartext is the regression guard for issue #10. The
// per-command debug log recorded the full raw line, so `PASS <password>` wrote
// the cleartext password to the logs whenever debug logging was enabled.
//
// Against the old code this test fails: the recv log contains the mock's
// password "s3cret". Against the fix, the PASS argument is redacted before
// logging, so the secret never reaches the sink while the command keyword still
// does (for debuggability).
func TestPOP3_PassNotLoggedInCleartext(t *testing.T) {
	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	m := newMockBackend()
	seedThree(m)
	// The harness cleanup (registered after ours) closes the connection and
	// joins the session goroutine first, so no logging races our slog restore.
	h := newPOP3Harness(t, m)
	h.login() // USER + PASS with the mock's valid password ("s3cret").

	// A subsequent reply guarantees the PASS recv-log write already happened in
	// the session goroutine and is visible here.
	if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("NOOP = %q", got)
	}

	logs := buf.String()
	if strings.Contains(logs, m.pass) {
		t.Errorf("cleartext password %q leaked into logs:\n%s", m.pass, logs)
	}
	// The command keyword must still be logged, redacted, for debuggability.
	if !strings.Contains(logs, "PASS <redacted>") {
		t.Errorf("expected redacted PASS marker in logs, got:\n%s", logs)
	}
}

// TestRedactCommand covers the log-safe rendering of raw command lines directly:
// credential-bearing verbs have their arguments stripped, other known verbs are
// passed through verbatim, and any other line is masked in full.
func TestRedactCommand(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"pass redacted", "PASS s3cret", "PASS <redacted>"},
		{"pass lowercase redacted", "pass s3cret", "PASS <redacted>"},
		{"pass with spaces in secret", "PASS my pass phrase", "PASS <redacted>"},
		{"apop digest redacted", "APOP alice c4c9334bac560ecc979e58001b3e22fb", "APOP <redacted>"},
		{"sasl auth response redacted", "AUTH PLAIN dGVzdAB0ZXN0AHMzY3JldA==", "AUTH <redacted>"},
		{"pass no arg passthrough", "PASS", "PASS"},
		{"user not redacted", "USER alice@example.com", "USER alice@example.com"},
		{"retr not redacted", "RETR 1", "RETR 1"},
		{"quit not redacted", "QUIT", "QUIT"},
		{"lowercase user not redacted", "user alice", "user alice"},
		{"pass trailing space passthrough", "PASS ", "PASS "},
		{"pass leading space masked", " PASS s3cret", "<unknown command redacted>"},
		{"pass tab separator masked", "PASS\ts3cret", "<unknown command redacted>"},
		{"pass long s masked", "paſs s3cret", "<unknown command redacted>"},
		{"pass Cyrillic A masked", "PАSS s3cret", "<unknown command redacted>"},
		{"pass invalid UTF-8 masked", "PASS\xff s3cret", "<unknown command redacted>"},
		{"apop tab separator masked", "APOP\talice c4c9334bac560ecc979e58001b3e22fb", "<unknown command redacted>"},
		{"auth leading space masked", " AUTH PLAIN dGVzdAB0ZXN0AHMzY3JldA==", "<unknown command redacted>"},
		{"bare password masked", "s3cret", "<unknown command redacted>"},
		{"sasl continuation masked", "dGVzdAB0ZXN0AHMzY3JldA==", "<unknown command redacted>"},
		{"unknown verb masked", "XTND XMIT", "<unknown command redacted>"},
		{"lookalike list masked", "lıst 1", "<unknown command redacted>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wire.RedactCommand(tc.line); got != tc.want {
				t.Errorf("redactCommand(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// TestPOP3_MalformedCredentialLinesNotLogged drives real sessions with debug
// logging on and sends credentials in lines the parser rejects: a leading
// space, a TAB instead of the SPACE, Unicode lookalike keywords, invalid UTF-8,
// and lines with no keyword at all (a password typed alone, or a SASL response
// sent as if AUTH had been accepted). Each line carries its own synthetic
// secret, and none may reach the log. The protocol replies are unchanged: a
// valid ASCII login still succeeds and every malformed line is still an unknown
// command.
func TestPOP3_MalformedCredentialLinesNotLogged(t *testing.T) {
	const okLogin = "+OK"
	tests := []struct {
		name   string
		line   string // %s is the case's synthetic secret
		secret string
		want   string // reply prefix
	}{
		{"valid PASS", "PASS %s", "synth-valid-pass-7f3a", okLogin},
		{"leading space PASS", " PASS %s", "synth-lead-space-91c2", "-ERR Unknown command"},
		{"leading tab PASS", "\tPASS %s", "synth-lead-tab-04de", "-ERR Unknown command"},
		{"tab-separated PASS", "PASS\t%s", "synth-tab-sep-5b17", "-ERR Unknown command"},
		{"long s lookalike", "paſs %s", "synth-long-s-c8e0", "-ERR Unknown command"},
		{"Cyrillic A lookalike", "PАSS %s", "synth-cyrillic-a-2d94", "-ERR Unknown command"},
		{"invalid UTF-8 keyword", "PASS\x80 %s", "synth-bad-utf8-6a31", "-ERR Unknown command"},
		{"bare password line", "%s", "synth-bare-line-e5f9", "-ERR Unknown command"},
		{"valid APOP", "APOP alice %s", "synth-apop-digest-1c4b", "-ERR Unknown command"},
		{"tab-separated APOP", "APOP\talice %s", "synth-apop-tab-8e26", "-ERR Unknown command"},
		{"valid AUTH initial response", "AUTH PLAIN %s", "synth-sasl-initial-3f70", "-ERR Unknown command"},
		{"leading space AUTH", " AUTH PLAIN %s", "synth-sasl-space-a9d1", "-ERR Unknown command"},
		{"dotless i AUTH lookalike", "AUTHı PLAIN %s", "synth-sasl-dotless-47bc", "-ERR Unknown command"},
		{"SASL continuation line", "%s", "c3ludGgtc2FzbC1jb250LTBlODM=", "-ERR Unknown command"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf syncBuffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			m := newMockBackend()
			m.pass = tc.secret
			seedThree(m)
			h := newPOP3Harness(t, m)
			if r := h.cmd("USER %s", m.user); !strings.HasPrefix(r, "+OK") {
				t.Fatalf("USER = %q", r)
			}
			if got := h.cmd(tc.line, tc.secret); !strings.HasPrefix(got, tc.want) {
				t.Fatalf("reply to %q = %q, want prefix %q", tc.line, got, tc.want)
			}
			// A later reply means the recv log of the line above was written.
			if got := h.cmd("NOOP"); tc.want == okLogin && !strings.HasPrefix(got, "+OK") {
				t.Fatalf("NOOP after login = %q", got)
			}

			logs := buf.String()
			if !strings.Contains(logs, "pop3: recv") {
				t.Fatalf("no recv log captured; the test proves nothing:\n%s", logs)
			}
			if strings.Contains(logs, tc.secret) {
				t.Errorf("synthetic secret %q leaked into logs:\n%s", tc.secret, logs)
			}
			// Valid diagnostics survive: the USER line is still logged verbatim.
			if !strings.Contains(logs, "USER "+m.user) {
				t.Errorf("valid USER line missing from logs:\n%s", logs)
			}
			t.Logf("captured logs:\n%s", logs)
		})
	}
}
