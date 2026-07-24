package queue

import (
	"errors"
	"fmt"
	"testing"
)

// TestSMTPErrorFrom_UnwrapsPermanentFailure guards the bug where deliver()
// returns the SMTP error wrapped (fmt.Errorf("all MX hosts failed: %w", …)) and
// the caller recovered it with a concrete type assertion — which never matches
// through a %w wrap, so a permanent 5xx was treated as transient and retried for
// the whole budget instead of bouncing. smtpErrorFrom must unwrap the chain.
func TestSMTPErrorFrom_UnwrapsPermanentFailure(t *testing.T) {
	perm := &SMTPError{Code: 550, Enhanced: "5.1.1", Message: "user unknown"}

	// The exact wrap shape produced by Worker.deliver on the final MX failure.
	wrapped := fmt.Errorf("all MX hosts failed: %w", error(perm))

	// A plain type assertion — the old, buggy recovery — must NOT see it, proving
	// the wrap really does hide the concrete type from an assertion.
	if _, ok := wrapped.(*SMTPError); ok { //nolint:errorlint // asserting the bug
		t.Fatal("precondition: wrapped error should not satisfy a direct type assertion")
	}

	se := smtpErrorFrom(wrapped)
	if se == nil {
		t.Fatal("smtpErrorFrom returned nil for a wrapped *SMTPError — permanent failure would be retried, not bounced")
	}
	if !se.IsPermanent() {
		t.Fatalf("recovered SMTPError not permanent: code=%d", se.Code)
	}
	if !errors.Is(wrapped, error(se)) {
		t.Fatalf("recovered error is not the one in the chain")
	}
}

// TestSMTPErrorFrom_NoCode returns nil when the failure carried no SMTP reply
// (e.g. a connect/DNS/TLS error), so the caller falls back to a code-less DSN.
func TestSMTPErrorFrom_NoCode(t *testing.T) {
	if se := smtpErrorFrom(errors.New("connect: connection refused")); se != nil {
		t.Fatalf("expected nil for non-SMTP error, got %+v", se)
	}
	if se := smtpErrorFrom(nil); se != nil {
		t.Fatalf("expected nil for nil error, got %+v", se)
	}
}

// TestDSNStatusCode maps a failure to its RFC 3463 enhanced status. A retry
// budget exhausted on repeated 4xx must report a persistent-transient 4.x.y, not
// a fabricated permanent 5.0.0.
func TestDSNStatusCode(t *testing.T) {
	cases := []struct {
		name string
		in   *SMTPError
		want string
	}{
		{"nil (no reply)", nil, "5.0.0"},
		{"explicit enhanced", &SMTPError{Code: 451, Enhanced: "4.4.1"}, "4.4.1"},
		{"4xx derived", &SMTPError{Code: 451}, "4.0.0"},
		{"5xx derived", &SMTPError{Code: 550}, "5.0.0"},
		{"code-less", &SMTPError{Code: 0, Message: "all MX hosts failed"}, "5.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dsnStatusCode(tc.in); got != tc.want {
				t.Fatalf("dsnStatusCode(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
