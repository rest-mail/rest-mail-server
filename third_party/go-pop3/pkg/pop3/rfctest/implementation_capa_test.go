package rfctest

import (
	"strings"
	"testing"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// implementationBackend announces IMPLEMENTATION with a fixed text.
type implementationBackend struct {
	commandErrorMailbox
	text            string
	transactionOnly bool
}

func (b *implementationBackend) Implementation() (string, bool) { return b.text, b.transactionOnly }

var _ pop3.ImplementationBackend = (*implementationBackend)(nil)

// RFC 2449 §6.9: IMPLEMENTATION carries server identification in both states,
// or optionally in TRANSACTION state only. Its text follows the §9 grammar,
// capa-tag *(SP param) with param = 1*pchar (printable ASCII except space),
// within the 512-octet response line of §4; text outside it omits the tag.
// checklist: POP3-CAPA-10
func TestCapaImplementation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		text            string
		transactionOnly bool
		authorization   string
		transaction     string
	}{
		{"both-states", "Example-POP3-v1.2", false, "IMPLEMENTATION Example-POP3-v1.2", "IMPLEMENTATION Example-POP3-v1.2"},
		{"several-params", "go-pop3 build 7", false, "IMPLEMENTATION go-pop3 build 7", "IMPLEMENTATION go-pop3 build 7"},
		{"transaction-only", "Example-POP3-v1.2", true, "", "IMPLEMENTATION Example-POP3-v1.2"},
		{"longest-line", strings.Repeat("x", 512-len("IMPLEMENTATION \r\n")), false,
			"IMPLEMENTATION " + strings.Repeat("x", 512-len("IMPLEMENTATION \r\n")),
			"IMPLEMENTATION " + strings.Repeat("x", 512-len("IMPLEMENTATION \r\n"))},
		{"empty", "", false, "", ""},
		{"leading-space", " v1", false, "", ""},
		{"trailing-space", "v1 ", false, "", ""},
		{"double-space", "go-pop3  v1", false, "", ""},
		{"tab", "go-pop3\tv1", false, "", ""},
		{"control", "go-pop3\x00", false, "", ""},
		{"crlf", "v1\r\n+OK", false, "", ""},
		{"delete", "v1\x7f", false, "", ""},
		{"non-ascii", "pöp3", false, "", ""},
		{"too-long", strings.Repeat("x", 512-len("IMPLEMENTATION \r\n")+1), false, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &implementationBackend{text: tc.text, transactionOnly: tc.transactionOnly}
			w := connect(t, b)
			expect(t, implementationCapa(t, w), tc.authorization)
			// A failed login stays in AUTHORIZATION state.
			expect(t, w.cmd("USER alice"), "+OK")
			expect(t, w.cmd("PASS wrong"), "-ERR [AUTH] Authentication failed")
			expect(t, implementationCapa(t, w), tc.authorization)
			w.login("secret")
			expect(t, implementationCapa(t, w), tc.transaction)
			expect(t, w.cmd("STAT"), "+OK 2 59")
			quit(t, w, "+OK")
			b.calls(t, 2, 1, 0)
		})
	}
}

// A Backend without ImplementationBackend never announces the tag.
// checklist: POP3-CAPA-10
func TestCapaImplementationRequiresBackend(t *testing.T) {
	w := connect(t, &commandErrorMailbox{})
	expect(t, implementationCapa(t, w), "")
	w.login("secret")
	expect(t, implementationCapa(t, w), "")
	quit(t, w, "+OK")
}

// implementationCapa reads a whole CAPA response and returns the
// IMPLEMENTATION line, or "" when it is absent. The rest of the list is pinned
// so the tag adds a line rather than replacing one.
func implementationCapa(t *testing.T, w *wire) string {
	t.Helper()
	expect(t, w.cmd("CAPA"), "+OK Capability list follows")
	for _, line := range []string{"USER", "TOP", "UIDL", "RESP-CODES", "AUTH-RESP-CODE", "PIPELINING"} {
		expect(t, w.line(), line)
	}
	tag := ""
	line := w.line()
	if line != "." {
		tag = line
		expect(t, w.line(), ".")
	}
	return tag
}
