package rfctest

import (
	"fmt"
	"testing"
)

// Synthetic MIME fixtures exercise RFC 3501 §§4.3/9 string grammar. Expected
// framing and decoded literal bytes are independent of the server's encoder.
// RFC 2231 parameters can decode into CRLF even when the stored header contains
// only ASCII; response-looking text must remain inside a counted literal.
//
// checklist: IMAP-BS-13
func TestBodyStructureParameterStrings(t *testing.T) {
	for _, tc := range []struct {
		name, parameter, wire string
		literals              []string
	}{
		{"empty", `name=""`, `""`, nil},
		{"quoted", `name="a\"b\\c"`, `"a\"b\\c"`, nil},
		{"encoded-word-opaque", `name="=?UTF-8?Q?caf=C3=A9?="`, `"=?UTF-8?Q?caf=C3=A9?="`, nil},
		{"utf8", `name*=utf-8''caf%C3%A9`, `{5}`, []string{"café"}},
		{"line-breaks", `name*=us-ascii''x%0D%0AF1%20OK%20forged`, `{15}`, []string{"x\r\nF1 OK forged"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := "Content-Type: application/octet-stream; " + tc.parameter + "\r\n"
			base := `("APPLICATION" "OCTET-STREAM" ("NAME" ` + tc.wire + `) NIL NIL "7BIT" 1`
			literals := append(append([]string{}, tc.literals...), tc.literals...)
			structureResponse(t, headers+"\r\nx", base+")", base+" NIL NIL NIL NIL)", literals...)
		})
	}
}

// Optional ID/description fields remain NIL when absent; transfer encoding is
// a mandatory string. Even an unrecognised encoding token must be framed safely
// rather than breaking the response. This does not claim that it is valid MIME.
//
// checklist: IMAP-BS-14
func TestBodyStructureHeaderStrings(t *testing.T) {
	for _, tc := range []struct {
		name, headers, fields string
		literals              []string
	}{
		{"absent", "", `NIL NIL "7BIT"`, nil},
		{"quoted", "Content-ID: <a\\b\"c@x>\r\nContent-Description: a \"quote\" and \\slash\r\n", `"<a\\b\"c@x>" "a \"quote\" and \\slash" "7BIT"`, nil},
		{"encoded-word", "Content-Description: =?UTF-8?Q?caf=C3=A9?=\r\n", `NIL "=?UTF-8?Q?caf=C3=A9?=" "7BIT"`, nil},
		{"utf8", "Content-ID: <é@x>\r\nContent-Description: café\r\n", `{6} {5} "7BIT"`, []string{"<é@x>", "café"}},
		{"extension-encoding", "Content-Transfer-Encoding: x-é\r\n", `NIL NIL {4}`, []string{"X-É"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") ` + tc.fields + " 1 1"
			if tc.name == "extension-encoding" {
				base = `("APPLICATION" "OCTET-STREAM" NIL ` + tc.fields + " 1"
			}
			literals := append(append([]string{}, tc.literals...), tc.literals...)
			structureResponse(t, tc.headers+"\r\nx", base+")", base+" NIL NIL NIL NIL)", literals...)
		})
	}
}

// Disposition parameter values are strings, never NIL, and appear only in the
// extended form. Multipart content-type parameters use that same grammar.
//
// checklist: IMAP-BS-15
func TestBodyStructureDispositionStrings(t *testing.T) {
	plain := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	for _, tc := range []struct {
		name, parameter, wire string
		literals              []string
	}{
		{"empty", `filename=""`, `""`, nil},
		{"quoted", `filename="a\"b\\c"`, `"a\"b\\c"`, nil},
		{"utf8", `filename*=utf-8''caf%C3%A9`, `{5}`, []string{"café"}},
		{"line-breaks", `filename*=us-ascii''x%0D%0AF1%20OK%20forged`, `{15}`, []string{"x\r\nF1 OK forged"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := "Content-Disposition: attachment; " + tc.parameter + "\r\n"
			disposition := `("ATTACHMENT" ("FILENAME" ` + tc.wire + `))`
			structureResponse(t, header+"\r\nx", plain+")", plain+" NIL "+disposition+" NIL NIL)", tc.literals...)
			structureResponse(t, "Content-Type: multipart/mixed; boundary=B; name=\"\"\r\n"+header+"\r\n--B\r\n\r\nx\r\n--B--\r\n",
				"("+plain+`) "MIXED")`, "("+plain+` NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "B" "NAME" "") `+disposition+" NIL NIL)", tc.literals...)
		})
	}
}

// A literal must retain its boundary and bytes at every recursion level; both
// ordinary multipart children and an encapsulated message use the same fields.
//
// checklist: IMAP-BS-16
func TestBodyStructureNestedStrings(t *testing.T) {
	inner := "Content-Type: application/octet-stream; name*=utf-8''caf%C3%A9\r\nContent-Description: café\r\n\r\nx"
	part := `("APPLICATION" "OCTET-STREAM" ("NAME" {5}) NIL {5} "7BIT" 1`
	body, extended := part+")", part+" NIL NIL NIL NIL)"
	structureResponse(t, "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n"+inner+"\r\n--B--\r\n",
		"("+body+` "MIXED")`, "("+extended+` "MIXED" ("BOUNDARY" "B") NIL NIL NIL)`, "café", "café", "café", "café")
	message := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL) `, len(inner))
	structureResponse(t, "Content-Type: message/rfc822\r\n\r\n"+inner,
		message+body+" 4)", message+extended+" 4 NIL NIL NIL NIL)", "café", "café", "café", "café")
}
