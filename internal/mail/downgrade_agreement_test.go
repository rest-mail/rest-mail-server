package mail

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
)

// downgradeAgreementRows are address fields of every shape #1891 names, each with what a
// client without UTF-8 is sent for it.
var downgradeAgreementRows = []struct{ name, stored, want string }{
	{"group name", "To: Grüppe: a@example.com, b@example.com;",
		"To: =?UTF-8?Q?Gr=C3=BCppe?= : a@example.com, b@example.com;"},
	{"empty group", "To: Grüppe:;", "To: =?UTF-8?Q?Gr=C3=BCppe?= :;"},
	{"8-bit name in a group", "Cc: Grüppe: Jörg <j@example.com>, a@example.com;",
		"Cc: =?UTF-8?Q?Gr=C3=BCppe?= : =?UTF-8?Q?J=C3=B6rg?= <j@example.com>, a@example.com;"},
	{"comment", "From: Jörg <j@example.com> (Büro)",
		"From: =?UTF-8?Q?J=C3=B6rg?= <j@example.com> (=?UTF-8?Q?B=C3=BCro?=)"},
	{"7-bit comment beside an 8-bit name", "Cc: Ann <a@example.com> (kept), Jörg <j@example.com>",
		"Cc: Ann <a@example.com> (kept), =?UTF-8?Q?J=C3=B6rg?= <j@example.com>"},
	{"comment before the name", "Reply-To: (Büro) Jörg <j@example.com>",
		"Reply-To: (=?UTF-8?Q?B=C3=BCro?=) =?UTF-8?Q?J=C3=B6rg?= <j@example.com>"},
	{"nested comment", "Sender: a@example.com (x (Bü) y)", "Sender: a@example.com (=?UTF-8?Q?x_=28B=C3=BC=29_y?=)"},
	{"quoted string", "To: \"Jörg \\\"J\\\" M.\" <j@example.com>", "To: =?UTF-8?Q?J=C3=B6rg_=22J=22_M=2E?= <j@example.com>"},
	{"quoted string with a comma stays 7-bit", "To: \"Ann, Lee\" <a@example.com>, Jörg <j@example.com>",
		"To: \"Ann, Lee\" <a@example.com>, =?UTF-8?Q?J=C3=B6rg?= <j@example.com>"},
	{"words in a run", "To: Hans Jörg Müller Lee <j@example.com>", "To: Hans =?UTF-8?Q?J=C3=B6rg_M=C3=BCller?= Lee <j@example.com>"},
	{"not UTF-8", "From: J\xf6rg <j@example.com> (caf\xe9)",
		"From: =?UNKNOWN-8BIT?Q?J=F6rg?= <j@example.com> (=?UNKNOWN-8BIT?Q?caf=E9?=)"},
	{"mixed charsets in one field", "Cc: Jörg <j@example.com>, J\xf6rg <k@example.com> (caf\xe9), =?ISO-8859-2?Q?=A3ukasz?= <l@example.com>",
		"Cc: =?UTF-8?Q?J=C3=B6rg?= <j@example.com>, =?UNKNOWN-8BIT?Q?J=F6rg?= <k@example.com> \r\n (=?UNKNOWN-8BIT?Q?caf=E9?=), =?ISO-8859-2?Q?=A3ukasz?= <l@example.com>"},
	{"an encoded word beside raw text", "To: =?UTF-8?Q?Ann?= Jörg =?UTF-8?Q?Lee?= <j@example.com>",
		"To: =?UTF-8?Q?Ann?= =?UTF-8?Q?_J=C3=B6rg_?= =?UTF-8?Q?Lee?= <j@example.com>"},
	{"folded lines", "To: Ann <a@example.com>,\r\n\tJörg <j@example.com>,\r\n Lee <l@example.com>",
		"To: Ann <a@example.com>,\r\n\t=?UTF-8?Q?J=C3=B6rg?= <j@example.com>,\r\n Lee <l@example.com>"},
	{"folded inside a group", "Bcc: Grüppe:\r\n a@example.com,\r\n Jörg <j@example.com>;",
		"Bcc: =?UTF-8?Q?Gr=C3=BCppe?= :\r\n a@example.com,\r\n =?UTF-8?Q?J=C3=B6rg?= <j@example.com>;"},
	{"8-bit domain", "To: J <j@bücher.example> (x)", "To: J <j@xn--bcher-kva.example> (x)"},
	{"8-bit local part", "To: üser@example.com (Büro), b@example.com",
		"To: =?UTF-8?Q?=C3=BCser=40example=2Ecom?= :; (=?UTF-8?Q?B=C3=BCro?=), b@example.com"},
	{"8-bit local part in a group", "Resent-To: Team: üser@example.com, b@example.com; (kept), c@example.com",
		"Resent-To: Team =?UTF-8?Q?=C3=BCser=40example=2Ecom=2C_b=40example=2Ecom?= :; (kept), c@example.com"},
	{"not an address list", "To: Jörg", "Downgraded-To: =?utf-8?q?J=C3=B6rg?="},
}

// The top-level header a gateway downgrades (RFC 6857) and the header of an encapsulated
// message the engine converts, as the whole body or as a part, are the same octets for the
// same address field, so a client reads a forwarded message's group names and comments as it
// would the message's own (#1891). The engine's size of each is the octets it sends.
func TestDowngrade_AddressFieldAgreesWithTheEngine(t *testing.T) {
	for _, row := range downgradeAgreementRows {
		t.Run(row.name, func(t *testing.T) {
			inner := row.stored + "\r\nSubject: s\r\n\r\nbody\r\n"
			want := row.want + "\r\nSubject: s\r\n\r\n"
			top, _, _ := strings.Cut(DowngradeHeaders(inner), "\r\n\r\n")
			if top+"\r\n\r\n" != want {
				t.Fatalf("gateway downgrade\n got %q\nwant %q", top+"\r\n\r\n", want)
			}
			for _, shape := range []struct{ name, outer string }{
				{"the whole body", "Content-Type: message/rfc822\r\n\r\n" + inner},
				{"a part", "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nx\r\n" +
					"--b\r\nContent-Type: message/rfc822\r\n\r\n" + inner + "--b--\r\n"},
			} {
				sent := wireform.Convert(shape.outer, false)
				if !strings.Contains(sent, "message/rfc822\r\n\r\n"+want+"body\r\n") {
					t.Errorf("%s: the engine sent\n%q\nwant the encapsulated header\n%q", shape.name, sent, want)
				}
				if size := wireform.Size(shape.outer, false); size != len(sent) {
					t.Errorf("%s: measured %d octets, sent %d", shape.name, size, len(sent))
				}
			}
		})
	}
}

// A field that isn't an address field is the engine's to convert, and Content-Type keeps its
// parameters in both, so only address fields take the host's conversion.
func TestDowngrade_EngineKeepsItsOtherFields(t *testing.T) {
	const inner = "Content-Type: text/plain; name=\"Jörg.txt\"\r\nX-Note: Jörg (Büro)\r\n\r\nbody\r\n"
	sent := wireform.Convert("Content-Type: message/rfc822\r\n\r\n"+inner, false)
	const want = "Content-Type: text/plain;\r\n name*=UTF-8''J%C3%B6rg.txt\r\nX-Note: =?UTF-8?Q?J=C3=B6rg_=28B=C3=BCro?=)\r\n\r\n"
	if !strings.Contains(sent, want) {
		t.Fatalf("the engine sent\n%q\nwant\n%q", sent, want)
	}
}
