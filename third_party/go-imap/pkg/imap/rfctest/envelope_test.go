package rfctest

import (
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// envelopeFixture deliberately disagrees with every cached summary field. A
// wire ENVELOPE must describe the raw headers, not the host's rendered summary.
func envelopeFixture(raw string) *fetchMailbox {
	box := newFetchMailbox()
	box.raw[10] = raw
	box.messages[0].Subject = "cached subject"
	box.messages[0].From = imap.Address{Name: "Cached", Email: "cached@example.net"}
	box.messages[0].To = "cached-to@example.net"
	return box
}

func requireEnvelope(t *testing.T, raw, want string, literals ...string) {
	t.Helper()
	for _, command := range []string{"FETCH 1 ENVELOPE", "UID FETCH 10 ENVELOPE"} {
		t.Run(command, func(t *testing.T) {
			box := envelopeFixture(raw)
			c := fetchClient(t, box, "SELECT")
			replies, tagged := fetchCommand(t, c, command)
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || replies[0].framing != "* 1 FETCH (UID 10 FLAGS () ENVELOPE "+want+")" || !slices.Equal(replies[0].literals, literals) {
				t.Fatalf("ENVELOPE: %#v, want %s / %q", replies, want, literals)
			}
			if !slices.Equal(box.fetches, []uint32{10}) || len(box.updates) != 0 {
				t.Fatalf("fetches=%v updates=%v", box.fetches, box.updates)
			}
			lines, tagged := c.command("N1", "FETCH 1 FLAGS")
			requireOK(t, tagged, "N1")
			requireExactly(t, lines, "* 1 FETCH (UID 10 FLAGS ())")
		})
	}
}

// TestEnvelopeExample replays the ENVELOPE portion of RFC 3501 §8's a003/a004
// message. The date's spelling, zone comment, address order and IDs are retained.
//
// checklist: IMAP-ENV-01 IMAP-ENV-02 IMAP-ENV-03 IMAP-ENV-04 IMAP-ENV-05
func TestEnvelopeExample(t *testing.T) {
	raw := "Date: Wed, 17 Jul 1996 02:23:25 -0700 (PDT)\r\n" +
		"From: Terry Gray <gray@cac.washington.edu>\r\n" +
		"Subject: IMAP4rev1 WG mtg summary and minutes\r\n" +
		"To: imap@cac.washington.edu\r\n" +
		"cc: minutes@CNRI.Reston.VA.US, John Klensin <KLENSIN@MIT.EDU>\r\n" +
		"Message-Id: <B27397-0100000@cac.washington.edu>\r\n\r\nbody\r\n"
	want := `("Wed, 17 Jul 1996 02:23:25 -0700 (PDT)" "IMAP4rev1 WG mtg summary and minutes" ` +
		`(("Terry Gray" NIL "gray" "cac.washington.edu")) ` +
		`(("Terry Gray" NIL "gray" "cac.washington.edu")) ` +
		`(("Terry Gray" NIL "gray" "cac.washington.edu")) ` +
		`((NIL NIL "imap" "cac.washington.edu")) ` +
		`((NIL NIL "minutes" "CNRI.Reston.VA.US")("John Klensin" NIL "KLENSIN" "MIT.EDU")) ` +
		`NIL NIL "<B27397-0100000@cac.washington.edu>")`
	requireEnvelope(t, raw, want)
}

// TestEnvelopeDistinctFields pins the ten-field order with distinguishable
// values, explicit Sender/Reply-To, multiple From addresses and Bcc.
//
// checklist: IMAP-ENV-01 IMAP-ENV-02 IMAP-ENV-03 IMAP-ENV-04 IMAP-ENV-05
func TestEnvelopeDistinctFields(t *testing.T) {
	raw := "Date: Fri, 1 Jan 2021 09:00:00 +0200\r\n" +
		"Subject: raw subject\r\n\tfolded\r\n" +
		"From: Alice <a@example.com>, Other <other@example.com>\r\n" +
		"Sender: sender@example.com\r\nReply-To: reply@example.com\r\n" +
		"To: to@example.com\r\nCc: cc@example.com\r\nBcc: bcc@example.com\r\n" +
		"In-Reply-To: <parent@example.com>\r\nMessage-ID: <child@example.com>\r\n\r\nbody"
	want := `("Fri, 1 Jan 2021 09:00:00 +0200" "raw subject folded" ` +
		`(("Alice" NIL "a" "example.com")("Other" NIL "other" "example.com")) ` +
		`((NIL NIL "sender" "example.com")) ((NIL NIL "reply" "example.com")) ` +
		`((NIL NIL "to" "example.com")) ((NIL NIL "cc" "example.com")) ` +
		`((NIL NIL "bcc" "example.com")) "<parent@example.com>" "<child@example.com>")`
	requireEnvelope(t, raw, want)
}

// TestEnvelopeAbsentAndEmpty distinguishes missing/empty scalar headers and
// ignores stale cached From/To/Date/Subject. Empty Sender/Reply-To default to From.
//
// checklist: IMAP-ENV-02 IMAP-ENV-03 IMAP-ENV-05 IMAP-ENV-06 IMAP-ENV-07
func TestEnvelopeAbsentAndEmpty(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"absent", "X-Test: present\r\n\r\nbody", `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`},
		{"empty", "Date:\r\nSubject:\r\nFrom:\r\nTo:\r\nCc:\r\nBcc:\r\nSender:\r\nReply-To:\r\nIn-Reply-To:\r\nMessage-ID:\r\n\r\nbody", `("" "" NIL NIL NIL NIL NIL NIL "" "")`},
		{"empty defaults", "From: a@example.com\r\nSender:\r\nReply-To:\r\n\r\nbody", `(NIL NIL ((NIL NIL "a" "example.com")) ((NIL NIL "a" "example.com")) ((NIL NIL "a" "example.com")) NIL NIL NIL NIL NIL)`},
		{"unparsed date", "Date: unknown date\r\n\r\nbody", `("unknown date" NIL NIL NIL NIL NIL NIL NIL NIL NIL)`},
	} {
		t.Run(tc.name, func(t *testing.T) { requireEnvelope(t, tc.raw, tc.want) })
	}
}

// TestEnvelopeScalarStrings checks quoted-specials, encoded words left encoded,
// and literal framing for UTF-8 scalar values (the latter is not UTF8=ACCEPT).
//
// checklist: IMAP-ENV-02 IMAP-ENV-05 IMAP-ENV-08
func TestEnvelopeScalarStrings(t *testing.T) {
	for _, tc := range []struct{ subject, want string }{
		{`a "quote" and \slash`, `"a \"quote\" and \\slash"`},
		{`=?UTF-8?Q?caf=C3=A9?=`, `"=?UTF-8?Q?caf=C3=A9?="`},
	} {
		t.Run(tc.subject, func(t *testing.T) {
			requireEnvelope(t, "Subject: "+tc.subject+"\r\n\r\nbody", "(NIL "+tc.want+" NIL NIL NIL NIL NIL NIL NIL NIL)")
		})
	}
	requireEnvelope(t, "Subject: café\r\nMessage-ID: <é@example.com>\r\n\r\nbody",
		"(NIL {5} NIL NIL NIL NIL NIL NIL NIL {16})", "café", "<é@example.com>")
}

// TestEnvelopeAddressStrings frames each address component independently:
// quoted-specials stay escaped, while 8-bit names, mailboxes and hosts, and a
// CRLF decoded from an RFC 2047 personal name, use counted literals so they
// cannot break the quoted string or inject a response line.
//
// checklist: IMAP-ENV-03 IMAP-ENV-05 IMAP-ENV-13
func TestEnvelopeAddressStrings(t *testing.T) {
	for _, tc := range []struct {
		name, header, list string
		literals           []string
	}{
		{"quoted-specials", `To: "a \"b\" \\c" <x@example.com>`, `(("a \"b\" \\c" NIL "x" "example.com"))`, nil},
		{"encoded-word name", "To: =?UTF-8?Q?J=C3=B6rg?= <j@example.com>", `(({5} NIL "j" "example.com"))`, []string{"Jörg"}},
		{"utf-8 mailbox and host", "To: jörg@exämple.com", `((NIL NIL {5} {12}))`, []string{"jörg", "exämple.com"}},
		{"decoded CRLF name", "To: =?UTF-8?Q?a=0D=0A*_1_BYE?= <x@example.com>", `(({10} NIL "x" "example.com"))`, []string{"a\r\n* 1 BYE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireEnvelope(t, tc.header+"\r\n\r\nbody", "(NIL NIL NIL NIL NIL "+tc.list+" NIL NIL NIL NIL)", tc.literals...)
		})
	}
}

// TestEnvelopeReadOnlyAndSharedLoad checks metadata has no Seen side effects
// under EXAMINE, and repeated ENVELOPE items share one raw-message read.
//
// checklist: IMAP-ENV-05 IMAP-ENV-09
func TestEnvelopeReadOnlyAndSharedLoad(t *testing.T) {
	box := envelopeFixture("Subject: raw\r\n\r\nbody")
	c := fetchClient(t, box, "EXAMINE")
	replies, tagged := fetchCommand(t, c, "UID FETCH 10 (ENVELOPE ENVELOPE INTERNALDATE)")
	requireOK(t, tagged, "F1")
	want := `ENVELOPE (NIL "raw" NIL NIL NIL NIL NIL NIL NIL NIL)`
	if len(replies) != 1 || strings.Count(replies[0].framing, want) != 2 || !strings.Contains(replies[0].framing, `INTERNALDATE "07-Feb-1994 21:52:25 -0800"`) {
		t.Fatalf("metadata: %#v", replies)
	}
	if !slices.Equal(box.fetches, []uint32{10}) || len(box.updates) != 0 {
		t.Fatalf("fetches=%v updates=%v", box.fetches, box.updates)
	}
}
