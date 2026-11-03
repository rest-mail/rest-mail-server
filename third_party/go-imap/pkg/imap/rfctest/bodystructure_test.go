package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// structureResponse checks the complete wire response, including metadata's lack
// of Seen writes and the shared raw-message load when both forms are requested.
func structureResponse(t *testing.T, raw, body, extended string, literals ...string) {
	t.Helper()
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
			t.Run(selection+"/"+prefix, func(t *testing.T) {
				box := newFetchMailbox()
				box.raw[10] = raw
				c := fetchClient(t, box, selection)
				replies, tagged := fetchCommand(t, c, prefix+" (BODY BODYSTRUCTURE)")
				requireOK(t, tagged, "F1")
				want := "* 1 FETCH (UID 10 FLAGS () BODY " + body + " BODYSTRUCTURE " + extended + ")"
				if len(replies) != 1 || replies[0].framing != want || !slices.Equal(replies[0].literals, literals) {
					t.Fatalf("FETCH = %#v, want %q", replies, want)
				}
				if len(box.fetches) != 1 || box.fetches[0] != 10 || len(box.updates) != 0 {
					t.Fatalf("metadata side effects: fetches=%v updates=%v", box.fetches, box.updates)
				}
				_, tagged = c.command("N1", "NOOP")
				requireOK(t, tagged, "N1")
			})
		}
	}
}

// TestBodyStructureRFCExample constructs a 2279-octet, 48-line text message to
// reproduce the simple BODY example in RFC 3501 §7.4.2. The RFC supplies the
// structure and dimensions, not the message text; the payload here is synthetic.
//
// checklist: IMAP-BS-01 IMAP-BS-02 IMAP-BS-03 IMAP-BS-06
// checklist: IMAP2-RESP-213
// checklist: IMAP2-SEC-221
func TestBodyStructureRFCExample(t *testing.T) {
	payload := strings.Repeat(strings.Repeat("a", 45)+"\r\n", 47) + strings.Repeat("b", 68) + "\r\n"
	if len(payload) != 2279 {
		t.Fatal("incorrect RFC fixture dimensions")
	}
	body := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 2279 48)`
	extended := strings.TrimSuffix(body, ")") + " NIL NIL NIL NIL)"
	structureResponse(t, "Subject: example\r\n\r\n"+payload, body, extended)
}

// TestBodyStructureTransferEncoding uses independently specified encoded octet
// and physical-line counts: a soft break is a line even though decoding removes
// it. Invalid quoted-printable is still opaque data for this metadata operation.
//
// checklist: IMAP-BS-02 IMAP-BS-03 IMAP-BS-04 IMAP-BS-05 IMAP-BS-06
func TestBodyStructureTransferEncoding(t *testing.T) {
	for _, tc := range []struct {
		name, encoding, payload string
		octets, lines           int
	}{
		{"quoted-printable", "quoted-printable", "caf=C3=A9=\r\nnext=20line", 23, 2},
		{"base64", "base64", "Y2Fm\r\nw6k=", 10, 2},
		{"malformed-quoted-printable", "quoted-printable", "=ZZ=\r\n=", 7, 2},
		{"trailing-crlf", "quoted-printable", "a=20b\r\n", 7, 1},
		{"8bit", "8bit", "caf\xc3\xa9", 5, 1},
		{"empty", "quoted-printable", "", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.payload) != tc.octets {
				t.Fatalf("fixture length %d, want %d", len(tc.payload), tc.octets)
			}
			headers := "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: " + tc.encoding + "\r\n"
			part := fmt.Sprintf(`("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "%s" %d %d`, strings.ToUpper(tc.encoding), tc.octets, tc.lines)
			body := part + ")"
			extended := part + " NIL NIL NIL NIL)"
			// The exact same encoded entity is also checked at the top level.
			t.Run("single", func(t *testing.T) {
				structureResponse(t, headers+"\r\n"+tc.payload, body, extended)
			})
			raw := "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + headers + "\r\n" + tc.payload + "\r\n--B--\r\n"
			t.Run("multipart", func(t *testing.T) {
				structureResponse(t, raw, "("+body+` "MIXED")`, "("+extended+` "MIXED" ("BOUNDARY" "B") NIL NIL NIL)`)
			})
		})
	}
}

// TestBodyStructureNestedEncoding checks recursive multipart traversal, basic
// field order, non-text parts without a text line count, and disposition order.
//
// checklist: IMAP-BS-02 IMAP-BS-04 IMAP-BS-05 IMAP-BS-06 IMAP-BS-07
func TestBodyStructureNestedEncoding(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=OUT\r\nContent-Disposition: inline\r\n\r\n" +
		"--OUT\r\nContent-Type: multipart/alternative; boundary=IN\r\n\r\n" +
		"--IN\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\na=20b=\r\nc\r\n--IN--\r\n" +
		"--OUT\r\nContent-Type: application/octet-stream; name=file.bin\r\n" +
		"Content-ID: <part@example.com>\r\nContent-Description: binary\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"Content-Disposition: attachment; filename=file.bin\r\n\r\n=00=FF\r\n--OUT--\r\n"
	text := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "QUOTED-PRINTABLE" 9 2`
	attachment := `("APPLICATION" "OCTET-STREAM" ("NAME" "file.bin") "<part@example.com>" "binary" "QUOTED-PRINTABLE" 6`
	body := "((" + text + `) "ALTERNATIVE")` + attachment + `) "MIXED")`
	extended := "((" + text + ` NIL NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "IN") NIL NIL NIL)` +
		attachment + ` NIL ("ATTACHMENT" ("FILENAME" "file.bin")) NIL NIL) "MIXED" ("BOUNDARY" "OUT") ("INLINE" NIL) NIL NIL)`
	structureResponse(t, raw, body, extended)
}

// TestBodyStructureRFCMultipartExample reproduces the second §7.4.2 structure
// from synthetic MIME entities with its exact encoded sizes and line counts.
// The attachment's base64 characters are placeholders, not an RFC test vector.
//
// checklist: IMAP-BS-01 IMAP-BS-02 IMAP-BS-03 IMAP-BS-04 IMAP-BS-05 IMAP-BS-06 IMAP-BS-07
// checklist: IMAP2-RESP-209
// checklist: IMAP2-SEC-222
func TestBodyStructureRFCMultipartExample(t *testing.T) {
	plain := strings.Repeat(strings.Repeat("a", 48)+"\r\n", 22) + strings.Repeat("b", 50) + "\r\n"
	encoded := strings.Repeat(strings.Repeat("A", 60)+"\r\n", 70) +
		strings.Repeat(strings.Repeat("A", 68)+"\r\n", 2) + strings.Repeat("A", 72) + "\r\n"
	if len(plain) != 1152 || len(encoded) != 4554 {
		t.Fatal("incorrect RFC fixture dimensions")
	}
	raw := "Content-Type: multipart/mixed; boundary=R\r\n\r\n" +
		"--R\r\nContent-Type: text/plain\r\n\r\n" + plain + "\r\n" +
		"--R\r\nContent-Type: text/plain; name=cc.diff\r\n" +
		"Content-ID: <960723163407.20117h@cac.washington.edu>\r\n" +
		"Content-Description: Compiler diff\r\nContent-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n--R--\r\n"
	text := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1152 23`
	attachment := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII" "NAME" "cc.diff") "<960723163407.20117h@cac.washington.edu>" "Compiler diff" "BASE64" 4554 73`
	body := "(" + text + ")" + attachment + `) "MIXED")`
	extended := "(" + text + " NIL NIL NIL NIL)" + attachment + ` NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "R") NIL NIL NIL)`
	structureResponse(t, raw, body, extended)
}
