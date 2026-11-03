package rfctest

import (
	"fmt"
	"testing"
)

// messageStructureResponse checks MESSAGE/RFC822's envelope/body/line fields
// and extension order both at the top level and inside a multipart attachment.
// The octet length covers the entire encapsulated message, including headers;
// lines is independently specified by each fixture, not derived by a MIME parser.
func messageStructureResponse(t *testing.T, inner, envelope, body, extended string, lines int, literals ...string) {
	t.Helper()
	base := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d %s `, len(inner), envelope)
	wantBody := fmt.Sprintf("%s%s %d)", base, body, lines)
	wantExtended := fmt.Sprintf(`%s%s %d NIL ("ATTACHMENT" ("FILENAME" "forward.eml")) NIL NIL)`, base, extended, lines)
	headers := "Content-Type: message/rfc822\r\nContent-Disposition: attachment; filename=forward.eml\r\n\r\n"
	// Both requested forms carry the same scalar literals, in request order.
	wantLiterals := append(append([]string(nil), literals...), literals...)
	t.Run("single", func(t *testing.T) {
		structureResponse(t, headers+inner, wantBody, wantExtended, wantLiterals...)
	})
	t.Run("multipart", func(t *testing.T) {
		raw := "Content-Type: multipart/mixed; boundary=F\r\n\r\n--F\r\n" + headers + inner + "\r\n--F--\r\n"
		structureResponse(t, raw, "("+wantBody+` "MIXED")`, "("+wantExtended+` "MIXED" ("BOUNDARY" "F") NIL NIL NIL)`, wantLiterals...)
	})
}

// TestMessageStructureRFCEnvelope embeds the §8 example headers in a forwarded
// message. The enclosing MIME wrapper and short body are synthetic; the RFC
// gives no MESSAGE/RFC822 example with raw message octets.
//
// checklist: IMAP-BS-09 IMAP-MSG-01 IMAP-MSG-02 IMAP-MSG-03 IMAP-MSG-04
func TestMessageStructureRFCEnvelope(t *testing.T) {
	inner := "Date: Wed, 17 Jul 1996 02:23:25 -0700 (PDT)\r\n" +
		"From: Terry Gray <gray@cac.washington.edu>\r\n" +
		"Subject: IMAP4rev1 WG mtg summary and minutes\r\n" +
		"To: imap@cac.washington.edu\r\n" +
		"cc: minutes@CNRI.Reston.VA.US, John Klensin <KLENSIN@MIT.EDU>\r\n" +
		"Message-Id: <B27397-0100000@cac.washington.edu>\r\n\r\nbody\r\n"
	envelope := `("Wed, 17 Jul 1996 02:23:25 -0700 (PDT)" "IMAP4rev1 WG mtg summary and minutes" ` +
		`(("Terry Gray" NIL "gray" "cac.washington.edu")) ` +
		`(("Terry Gray" NIL "gray" "cac.washington.edu")) ` +
		`(("Terry Gray" NIL "gray" "cac.washington.edu")) ` +
		`((NIL NIL "imap" "cac.washington.edu")) ` +
		`((NIL NIL "minutes" "CNRI.Reston.VA.US")("John Klensin" NIL "KLENSIN" "MIT.EDU")) ` +
		`NIL NIL "<B27397-0100000@cac.washington.edu>")`
	messageStructureResponse(t, inner, envelope,
		`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 6 1)`,
		`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 6 1 NIL NIL NIL NIL)`, 8)
}

// TestMessageStructureHeaderDefaults covers distinguishable fields, folding,
// empty/missing scalars and Sender/Reply-To defaults. Missing required message
// headers are robustness cases, not examples of valid RFC 2822 messages.
//
// checklist: IMAP-BS-09 IMAP-MSG-01 IMAP-MSG-02 IMAP-MSG-03 IMAP-MSG-04 IMAP-MSG-05
func TestMessageStructureHeaderDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, inner, envelope string
		lines                 int
	}{
		{"absent", "X-Test: inner\r\n\r\nx", `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`, 3},
		{"empty", "Date:\r\nSubject:\r\nFrom:\r\nSender:\r\nReply-To:\r\nTo:\r\nCc:\r\nBcc:\r\nIn-Reply-To:\r\nMessage-ID:\r\n\r\nx", `("" "" NIL NIL NIL NIL NIL NIL "" "")`, 12},
		{"empty-defaults", "From: a@example.com\r\nSender:\r\nReply-To:\r\n\r\nx", `(NIL NIL ((NIL NIL "a" "example.com")) ((NIL NIL "a" "example.com")) ((NIL NIL "a" "example.com")) NIL NIL NIL NIL NIL)`, 5},
		{"distinct", "Date: Fri, 1 Jan 2021 09:00:00 +0200\r\nSubject: inner\r\n\tfolded\r\n" +
			"From: Alice <a@example.com>\r\nSender: sender@example.com\r\nReply-To: reply@example.com\r\n" +
			"To: to@example.com\r\nCc: cc@example.com\r\nBcc: bcc@example.com\r\n" +
			"In-Reply-To: <parent@example.com>\r\nMessage-ID: <child@example.com>\r\n\r\nx",
			`("Fri, 1 Jan 2021 09:00:00 +0200" "inner folded" (("Alice" NIL "a" "example.com")) ` +
				`((NIL NIL "sender" "example.com")) ((NIL NIL "reply" "example.com")) ` +
				`((NIL NIL "to" "example.com")) ((NIL NIL "cc" "example.com")) ` +
				`((NIL NIL "bcc" "example.com")) "<parent@example.com>" "<child@example.com>")`, 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messageStructureResponse(t, tc.inner, tc.envelope,
				`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1)`,
				`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1 NIL NIL NIL NIL)`, tc.lines)
		})
	}
}

// TestMessageStructureScalarStrings checks quoted-specials, encoded words and
// nested address components that need literals.
// Raw 8-bit headers are a compatibility robustness case, not UTF8=ACCEPT;
// counted scalar literals must keep the following nested body synchronized.
//
// checklist: IMAP-BS-09 IMAP-MSG-01 IMAP-MSG-02 IMAP-MSG-03 IMAP-MSG-04 IMAP-MSG-06 IMAP-ENV-13
func TestMessageStructureScalarStrings(t *testing.T) {
	for _, tc := range []struct {
		name, headers, envelope string
		literals                []string
	}{
		{"quoted", "Subject: a \"quote\" and \\slash\r\n", `(NIL "a \"quote\" and \\slash" NIL NIL NIL NIL NIL NIL NIL NIL)`, nil},
		{"encoded-word", "Subject: =?UTF-8?Q?caf=C3=A9?=\r\n", `(NIL "=?UTF-8?Q?caf=C3=A9?=" NIL NIL NIL NIL NIL NIL NIL NIL)`, nil},
		{"literal", "Subject: café\r\nIn-Reply-To: <é@x>\r\nMessage-ID: <é@y>\r\n",
			`(NIL {5} NIL NIL NIL NIL NIL NIL {6} {6})`, []string{"café", "<é@x>", "<é@y>"}},
		{"address literals", "From: =?UTF-8?Q?J=C3=B6rg?= <j@example.com>\r\nTo: jörg@exämple.com\r\nCc: =?UTF-8?Q?a=0D=0Ab?= <x@example.com>\r\n",
			`(NIL NIL (({5} NIL "j" "example.com")) (({5} NIL "j" "example.com")) (({5} NIL "j" "example.com")) ((NIL NIL {5} {12})) (({4} NIL "x" "example.com")) NIL NIL NIL)`,
			[]string{"Jörg", "Jörg", "Jörg", "jörg", "exämple.com", "a\r\nb"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := 3
			if tc.literals != nil {
				lines = 5
			}
			messageStructureResponse(t, tc.headers+"\r\nx", tc.envelope,
				`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1)`,
				`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1 NIL NIL NIL NIL)`, lines, tc.literals...)
		})
	}
}

// TestMessageStructureRecursiveBody checks a forwarded multipart with opaque
// quoted-printable data. The outer line count includes the encapsulated headers,
// MIME delimiters and encoded text; the child's line count includes its soft break.
//
// checklist: IMAP-BS-09 IMAP-MSG-01 IMAP-MSG-02 IMAP-MSG-03 IMAP-MSG-04 IMAP-MSG-07
func TestMessageStructureRecursiveBody(t *testing.T) {
	inner := "Subject: multipart\r\nContent-Type: multipart/alternative; boundary=I\r\n\r\n" +
		"--I\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\na=20b=\r\nc\r\n--I--\r\n"
	envelope := `(NIL "multipart" NIL NIL NIL NIL NIL NIL NIL NIL)`
	body := `(("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "QUOTED-PRINTABLE" 9 2) "ALTERNATIVE")`
	extended := `(("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "QUOTED-PRINTABLE" 9 2 NIL NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "I") NIL NIL NIL)`
	messageStructureResponse(t, inner, envelope, body, extended, 10)
}

// TestMessageStructureNestedMessage distinguishes two encapsulation levels and
// verifies the MESSAGE/RFC822-only fields are not added to other MESSAGE types.
//
// checklist: IMAP-BS-09 IMAP-MSG-01 IMAP-MSG-02 IMAP-MSG-03 IMAP-MSG-04 IMAP-MSG-07
func TestMessageStructureNestedMessage(t *testing.T) {
	deep := "Subject: deepest\r\n\r\nz\r\n"
	inner := "Subject: middle\r\nContent-Type: message/rfc822\r\n\r\n" + deep
	deepEnvelope := `(NIL "deepest" NIL NIL NIL NIL NIL NIL NIL NIL)`
	base := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d %s `, len(deep), deepEnvelope)
	body := base + `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 3 1) 3)`
	extended := base + `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 3 1 NIL NIL NIL NIL) 3 NIL NIL NIL NIL)`
	messageStructureResponse(t, inner, `(NIL "middle" NIL NIL NIL NIL NIL NIL NIL NIL)`, body, extended, 6)
	t.Run("other-message-subtype", func(t *testing.T) {
		structureResponse(t, "Content-Type: message/delivery-status\r\n\r\nx\r\n",
			`("MESSAGE" "DELIVERY-STATUS" NIL NIL NIL "7BIT" 3)`,
			`("MESSAGE" "DELIVERY-STATUS" NIL NIL NIL "7BIT" 3 NIL NIL NIL NIL)`)
	})
}
