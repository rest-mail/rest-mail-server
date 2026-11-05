package rfctest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

func literalTypeClient(t *testing.T, selection string) (*client, *fetchMailbox) {
	t.Helper()
	box := newFetchMailbox()
	box.messages = []imap.Message{{UID: 10, Subject: "ON", From: imap.Address{Email: "sender@example.com"}, To: "recipient@example.com", Date: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}}
	box.raw = map[uint32]string{10: "Subject: ON\r\nFrom: sender@example.com\r\nTo: recipient@example.com\r\nCc: copy@example.com\r\nBcc: hidden@example.com\r\nON: needle\r\nX-Probe: a\"b\\c\r\n\r\nbody needle\r\n"}
	return fetchClient(t, box, selection), box
}

func rejectTypedLiteral(t *testing.T, c *client, prefix, value string, sync bool) {
	t.Helper()
	wire := fmt.Sprintf("X1 %s{%d}\r\n", prefix, len(value))
	if !sync {
		wire = fmt.Sprintf("X1 %s{%d+}\r\n%s\r\nN1 NOOP\r\n", prefix, len(value), value)
	}
	writeLiteralWire(t, c, wire)
	if got := c.line(); !strings.HasPrefix(got, "X1 BAD ") {
		t.Fatalf("typed operand was invited or accepted: %q", got)
	}
	if sync {
		_, tagged := c.command("N1", "NOOP")
		requireOK(t, tagged, "N1")
	} else {
		if got := c.line(); !strings.HasPrefix(got, "* BYE ") {
			t.Fatalf("unread non-synchronizing literal: %q", got)
		}
		if !c.closed() {
			t.Fatal("literal payload or following command was dispatched")
		}
	}
}

// TestSearchLiteralDatesAreNotQuotedDates exercises the source type, not just
// the decoded value: all six date keys permit a bare or quoted date but never
// a literal, including RETURN, Boolean/group and UID SEARCH forms.
//
// checklist: IMAP-SEARCH-41
func TestSearchLiteralDatesAreNotQuotedDates(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, command := range []string{"SEARCH", "UID SEARCH"} {
			for _, key := range []string{"ON", "BEFORE", "SINCE", "SENTON", "SENTBEFORE", "SENTSINCE"} {
				for _, form := range []string{key + " ", "OR ALL " + key + " ", "NOT (" + key + " ", "RETURN (ALL) " + key + " "} {
					for _, sync := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/%s/%t", selection, command, form, sync), func(t *testing.T) {
							c, box := literalTypeClient(t, selection)
							rejectTypedLiteral(t, c, command+" "+form, "1-Jan-2025", sync)
							if len(box.fetches) != 0 || len(box.updates) != 0 {
								t.Fatalf("invalid date read or changed messages: %v %v", box.fetches, box.updates)
							}
						})
					}
				}
			}
		}
	}
}

// TestSearchLiteralAtomsAndNumbersRejectBeforeContinuation keeps literal
// substitution out of criterion names, flag-keyword atoms, numbers and UID
// sets. These may already fail after materialization; no bytes need inviting.
//
// checklist: IMAP-SEARCH-41
func TestSearchLiteralAtomsAndNumbersRejectBeforeContinuation(t *testing.T) {
	for _, tc := range []struct{ prefix, value string }{
		{"", "ALL"}, {"KEYWORD ", "custom"}, {"UNKEYWORD ", "custom"},
		{"LARGER ", "1"}, {"SMALLER ", "100"}, {"UID ", "10"},
		{"OR ALL ", "SEEN"}, {"RETURN (SAVE) UID ", "10"},
		{"RETURN (", "ALL"}, {`HEADER "X"garbage `, "needle"},
	} {
		for _, sync := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", tc.prefix, sync), func(t *testing.T) {
				c, box := literalTypeClient(t, "SELECT")
				rejectTypedLiteral(t, c, "SEARCH "+tc.prefix, tc.value, sync)
				if len(box.fetches) != 0 || len(box.updates) != 0 {
					t.Fatal("invalid operand reached message data")
				}
			})
		}
	}
}

// TestSearchLiteralAstringsRetainTheirRole keeps ON and SUBJECT legal as
// HEADER field names/values and quotes or backslashes inside literal data.
// An unfinished Boolean or group after a legal literal is completed by the
// continuation instead of being rejected by an approximate second grammar.
//
// checklist: IMAP-SEARCH-42
func TestSearchLiteralAstringsRetainTheirRole(t *testing.T) {
	for _, tc := range []struct{ prefix, value, after string }{
		{"SUBJECT ", "ON", ""}, {"SUBJECT ", "", ""},
		{"FROM ", "sender", ""}, {"TO ", "recipient", ""},
		{"CC ", "copy", ""}, {"BCC ", "hidden", ""},
		{"BODY ", "needle", ""}, {"TEXT ", "needle", ""},
		{"HEADER ON ", "needle", ""}, {"HEADER ", "ON", " needle"},
		{"HEADER ", "Subject", " ON"}, {"HEADER X-Probe ", `a"b\c`, ""},
		{"OR HEADER ON ", "needle", " ALL"},
		{"NOT (NOT SUBJECT ", "ON", ")"},
		{"(OR SUBJECT ", "ON", " HEADER ON needle)"},
		{"CHARSET ", "US-ASCII", " ALL"},
	} {
		for _, sync := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%q/%t", tc.prefix, tc.value, sync), func(t *testing.T) {
				c, _ := literalTypeClient(t, "SELECT")
				prefix := fmt.Sprintf("X1 SEARCH %s{%d+}\r\n", tc.prefix, len(tc.value))
				if sync {
					writeLiteralWire(t, c, fmt.Sprintf("X1 SEARCH %s{%d}\r\n", tc.prefix, len(tc.value)))
					if got := c.line(); !strings.HasPrefix(got, "+ ") {
						t.Fatalf("legal astring was not invited: %q", got)
					}
					prefix = ""
				}
				writeLiteralWire(t, c, prefix+tc.value+tc.after+"\r\nN1 NOOP\r\n")
				if got := c.line(); got != "* SEARCH 1" {
					t.Fatalf("literal value: %q", got)
				}
				requireOK(t, c.line(), "X1")
				requireOK(t, c.line(), "N1")
			})
		}
	}
}

// TestSearchMultipleLiteralRoles distinguishes each literal after earlier
// values were materialized. The final ON is a date key, whereas an earlier ON
// was a HEADER field name. Every successful prefix remains ordinary data.
//
// checklist: IMAP-SEARCH-42
func TestSearchMultipleLiteralRoles(t *testing.T) {
	for _, uid := range []bool{false, true} {
		for _, rejectDate := range []bool{false, true} {
			t.Run(fmt.Sprintf("uid=%t/reject=%t", uid, rejectDate), func(t *testing.T) {
				c, box := literalTypeClient(t, "EXAMINE")
				command := "SEARCH RETURN (ALL) HEADER "
				if uid {
					command = "UID " + command
				}
				writeLiteralWire(t, c, "X1 "+command+"{2}\r\n")
				if got := c.line(); !strings.HasPrefix(got, "+ ") {
					t.Fatalf("first HEADER astring: %q", got)
				}
				writeLiteralWire(t, c, "ON {6}\r\n")
				if got := c.line(); !strings.HasPrefix(got, "+ ") {
					t.Fatalf("second HEADER astring: %q", got)
				}
				if rejectDate {
					writeLiteralWire(t, c, "needle ON {10}\r\n")
					if got := c.line(); !strings.HasPrefix(got, "X1 BAD ") {
						t.Fatalf("date literal after strings: %q", got)
					}
				} else {
					writeLiteralWire(t, c, "needle ON \"1-Jan-2025\"\r\n")
					want := `* ESEARCH (TAG "X1") ALL 1`
					if uid {
						want = `* ESEARCH (TAG "X1") UID ALL 10`
					}
					if got := c.line(); got != want {
						t.Fatalf("composed literal roles: %q, want %q", got, want)
					}
					requireOK(t, c.line(), "X1")
				}
				_, tagged := c.command("N1", "NOOP")
				requireOK(t, tagged, "N1")
				if len(box.updates) != 0 {
					t.Fatal("SEARCH changed message flags")
				}
			})
		}
	}
}

// TestAtomOnlyCommandsDoNotSolicitLiterals refuses a literal argument before
// continuation for commands with no string slots before authentication. No
// AUTHENTICATE credentials or ENABLE side effects are solicited.
//
// checklist: IMAP-LITERAL-12
func TestAtomOnlyCommandsDoNotSolicitLiterals(t *testing.T) {
	for _, command := range []string{"CAPABILITY", "NOOP", "LOGOUT", "STARTTLS", "NAMESPACE", "CHECK", "CLOSE", "EXPUNGE", "UNSELECT", "IDLE", "ENABLE UTF8=ACCEPT", "AUTHENTICATE PLAIN", "UID"} {
		for _, sync := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", command, sync), func(t *testing.T) {
				c := dial(t)
				c.line()
				rejectTypedLiteral(t, c, command+" ", "x", sync)
				if c.backend.calls != 0 {
					t.Fatal("literal credentials were authenticated")
				}
			})
		}
	}
}

// TestLiteralDateFailureKeepsSavedSearch proves a syntax rejection with SAVE
// leaves the previous result and selection intact, rather than converting the
// literal into a valid quoted date and replacing the saved mailbox identity.
//
// checklist: IMAP-SEARCH-43
func TestLiteralDateFailureKeepsSavedSearch(t *testing.T) {
	for _, command := range []string{"SEARCH", "UID SEARCH"} {
		c, _ := literalTypeClient(t, "SELECT")
		_, tagged := c.command("S1", "SEARCH RETURN (SAVE) UID 10")
		requireOK(t, tagged, "S1")
		rejectTypedLiteral(t, c, command+" RETURN (SAVE) ON ", "1-Jan-2026", true)
		lines, tagged := c.command("F1", "UID FETCH $ FLAGS")
		requireOK(t, tagged, "F1")
		requireExactly(t, lines, "* 1 FETCH (UID 10 FLAGS ())")
	}
}

// TestLiteralRefusalPreservesAuthenticatedState keeps mode changes and selected
// state atomic when a later illegal literal follows otherwise valid arguments.
//
// checklist: IMAP-LITERAL-12
func TestLiteralRefusalPreservesAuthenticatedState(t *testing.T) {
	for _, sync := range []bool{false, true} {
		c, box := utf8Client(t)
		rejectTypedLiteral(t, c, "ENABLE UTF8=ACCEPT ", "", sync)
		if box.enables != 0 || c.backend.calls != 1 {
			t.Fatalf("refused ENABLE changed state: enabled=%d auth=%d", box.enables, c.backend.calls)
		}
		if sync {
			enableUTF8(t, c)
			if box.enables != 1 {
				t.Fatal("valid ENABLE after refusal did not work")
			}
		}
	}
	for _, command := range []string{"CHECK", "CLOSE", "EXPUNGE", "UNSELECT", "IDLE"} {
		c, box := literalTypeClient(t, "SELECT")
		rejectTypedLiteral(t, c, command+" ", "ignored", true)
		lines, tagged := c.command("F1", "FETCH 1 FLAGS")
		requireOK(t, tagged, "F1")
		requireExactly(t, lines, "* 1 FETCH (UID 10 FLAGS ())")
		if len(box.updates) != 0 {
			t.Fatal("refused command changed message flags")
		}
	}
}

// TestLiteralPositionRefusalKeepsTOOBIG retains LITERAL-'s required size error
// even when the argument type is also wrong; the unsafe stream still closes.
//
// checklist: IMAP-LITERAL-12
func TestLiteralPositionRefusalKeepsTOOBIG(t *testing.T) {
	for _, prefix := range []string{"NOOP ", "AUTHENTICATE PLAIN ", "SEARCH ON "} {
		c := dial(t)
		c.line()
		writeLiteralWire(t, c, "X1 "+prefix+"{4097+}\r\n")
		if got := c.line(); !strings.HasPrefix(got, "X1 BAD [TOOBIG] ") {
			t.Fatalf("size error was masked: %q", got)
		}
		if got := c.line(); !strings.HasPrefix(got, "* BYE ") || !strings.Contains(got, "TOOBIG") {
			t.Fatalf("oversized unread literal: %q", got)
		}
		if !c.closed() || c.backend.calls != 0 {
			t.Fatal("oversized data reached the command processor")
		}
	}
}
