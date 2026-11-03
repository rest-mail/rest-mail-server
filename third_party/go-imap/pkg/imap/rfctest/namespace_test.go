package rfctest

import (
	"strings"
	"testing"
)

// TestNamespace: RFC 2342's NAMESPACE, advertised in CAPABILITY, answers one
// untagged NAMESPACE with the personal namespace, prefix "" and LIST's
// hierarchy delimiter "/", and NIL for the other users' and shared classes
// this server does not have, as in the RFC's example 5.1, in the authenticated
// and the selected state. It takes no arguments, and before authentication
// there is no namespace to report.
//
// checklist: IMAP-NAMESPACE-01 IMAP-NAMESPACE-02
// checklist: IMAP2-CORE-32 IMAP2-CORE-33
// checklist: IMAP2-RESP-188
func TestNamespace(t *testing.T) {
	c := dial(t)
	c.line()
	requireCaps(t, c.capabilities("C1"), []string{"NAMESPACE"}, nil)
	if lines, tagged := c.command("A0", "NAMESPACE"); !strings.HasPrefix(tagged, "A0 NO ") || len(lines) != 0 {
		t.Fatalf("NAMESPACE before LOGIN: %q %q", lines, tagged)
	}
	c.login()
	for _, state := range []string{"authenticated", "selected"} {
		if state == "selected" {
			if _, tagged := c.command("S1", "SELECT INBOX"); !strings.HasPrefix(tagged, "S1 OK") {
				t.Fatalf("SELECT: %q", tagged)
			}
		}
		lines, tagged := c.command("A001", "namespace")
		requireOK(t, tagged, "A001")
		requireExactly(t, lines, `* NAMESPACE (("" "/")) NIL NIL`)
		if lines, tagged := c.command("A002", "NAMESPACE x"); tagged != "A002 BAD NAMESPACE takes no arguments" || len(lines) != 0 {
			t.Fatalf("%s: NAMESPACE with an argument: %q %q", state, lines, tagged)
		}
	}
	lines, tagged := c.command("L1", `LIST "" ""`)
	requireOK(t, tagged, "L1")
	requireExactly(t, lines, `* LIST (\Noselect) "/" ""`)
}
