package server

import (
	"strings"
	"testing"
)

// TestPOP3_NoopRsetRejectedPreAuth is the regression guard for issue #11. RFC
// 1939 places NOOP and RSET in the TRANSACTION state (§5/§6), reachable only
// after a successful USER/PASS (or APOP) exchange. Issuing either command while
// the session is still in AUTHORIZATION must be answered -ERR, not +OK. Against
// the old code both commands fell straight through to their handlers and replied
// +OK before any authentication, so this test fails; the fixed dispatch gates
// them on s.auth.authenticated like every other TRANSACTION command.
func TestPOP3_NoopRsetRejectedPreAuth(t *testing.T) {
	for _, cmd := range []string{"NOOP", "RSET"} {
		t.Run(cmd, func(t *testing.T) {
			m := newMockBackend()
			seedThree(m)
			h := newPOP3Harness(t, m) // no login: still in AUTHORIZATION

			if got := h.cmd(cmd); !strings.HasPrefix(got, "-ERR") {
				t.Errorf("%s in AUTHORIZATION state = %q, want -ERR (TRANSACTION-only command)", cmd, got)
			}

			// The rejection must leave the session usable: USER/PASS still work and
			// the same command succeeds once authenticated.
			h.login()
			if got := h.cmd(cmd); !strings.HasPrefix(got, "+OK") {
				t.Errorf("%s after authentication = %q, want +OK", cmd, got)
			}
		})
	}
}
