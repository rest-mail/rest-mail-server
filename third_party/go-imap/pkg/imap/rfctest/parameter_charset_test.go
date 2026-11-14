package rfctest

import (
	"strings"
	"testing"
)

// requireStructure proves what BODY and BODYSTRUCTURE of raw hold, with and without
// UTF8=ACCEPT. Every string in want is 7-bit, so it reads the same in either session.
func requireStructure(t *testing.T, raw string, want ...string) {
	t.Helper()
	for _, item := range []string{"BODY", "BODYSTRUCTURE"} {
		c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
		requireHolds(t, fetchMetadata(t, c, item), want...)
		framing, _ := fetchUTF8Metadata(t, raw, item)
		requireHolds(t, framing, want...)
	}
}

// TestParameterInCharsetNotDecoded: mime.ParseMediaType drops an RFC 2231 parameter
// in any charset but UTF-8 and US-ASCII. A parameter in a charset go-imap has no
// reader for goes out as an encoded word naming that charset, so no byte is guessed,
// at the top of a message, in a part, on a multipart and in an encapsulated message.
//
// checklist: IMAP-BS-13
func TestParameterInCharsetNotDecoded(t *testing.T) {
	const fields = "Content-Type: text/plain; charset=utf-8; name*=windows-1252''J%F6rg.txt; other=keep\r\n" +
		"Content-Disposition: attachment; filename*=iso-8859-2''%A3.txt\r\n\r\nbody\r\n"
	const params = `("CHARSET" "utf-8" "NAME" "=?windows-1252?Q?J=F6rg=2Etxt?=" "OTHER" "keep")`
	const disposition = `("ATTACHMENT" ("FILENAME" "=?iso-8859-2?Q?=A3=2Etxt?="))`

	for name, raw := range map[string]string{
		"top":          fields,
		"part":         "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + fields + "--B--\r\n",
		"encapsulated": "Content-Type: message/rfc822\r\n\r\n" + fields,
	} {
		t.Run(name, func(t *testing.T) {
			requireStructure(t, raw, params)
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), disposition)
		})
	}

	t.Run("multipart", func(t *testing.T) {
		const raw = "Content-Type: multipart/mixed; boundary=B; note*=x-no-such-charset''J%F6rg\r\n" +
			"Content-Disposition: inline; filename*=WINDOWS-1252'de'%F6.txt\r\n\r\n" +
			"--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B--\r\n"
		c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
		requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"),
			`("BOUNDARY" "B" "NOTE" "=?x-no-such-charset?Q?J=F6rg?=")`,
			`("INLINE" ("FILENAME" "=?WINDOWS-1252?Q?=F6=2Etxt?="))`)
	})
}

// TestParameterContinuationInCharset: mime.ParseMediaType keeps only the later
// segments of a continued parameter whose first segment names such a charset. All
// of it is one encoded word, because go-imap can't tell where a character of that
// charset ends, and a word may not split one (RFC 2047 §5).
//
// checklist: IMAP-BS-13
func TestParameterContinuationInCharset(t *testing.T) {
	requireStructure(t, "Content-Type: application/x-y; name*0*=windows-1252''J%F6; name*1=\"rg \"; name*2*=%F6.txt\r\n\r\nbody\r\n",
		`("NAME" "=?windows-1252?Q?J=F6rg_=F6=2Etxt?=")`)

	long := strings.Repeat("%F6", 60)
	requireStructure(t, "Content-Type: application/x-y; name*=Shift_JIS''"+long+"\r\n\r\nbody\r\n",
		`("NAME" "=?Shift_JIS?Q?`+strings.Repeat("=F6", 60)+`?=")`)
}

// TestParameterCharsetGoIMAPDecodes: a charset go-imap has a reader for is decoded
// under every name it knows, then follows the session's rule like any decoded value.
//
// checklist: IMAP-BS-13
func TestParameterCharsetGoIMAPDecodes(t *testing.T) {
	for _, charset := range []string{"ISO-8859-1", "latin1", "iso8859-1", "L1"} {
		t.Run(charset, func(t *testing.T) {
			raw := "Content-Type: application/x-y; name*=" + charset + "''caf%E9\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `("NAME" "=?UTF-8?Q?caf=C3=A9?=")`)

			_, literals := fetchUTF8Metadata(t, raw, "BODYSTRUCTURE")
			requireHolds(t, literals, "café")
		})
	}
}

// TestParameterNamingNoCharset: an extended value may leave its charset out (RFC 2231
// §4), and one that is malformed still has text worth keeping. Neither is dropped,
// and an escape that isn't one stays as it was sent.
//
// checklist: IMAP-BS-13
func TestParameterNamingNoCharset(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"''plain.txt", `"plain.txt"`},
		{"''caf%C3%A9", `"=?UTF-8?Q?caf=C3=A9?="`},
		{"''caf%E9", `"=?UNKNOWN-8BIT?Q?caf=E9?="`},
		{"no-quotes", `"no-quotes"`},
		{"utf-8''a%ZZb", `"a%ZZb"`},
		{"windows-1252''a%ZZ%F6", `"=?windows-1252?Q?a=25ZZ=F6?="`},
		{`"bad?charset''x"`, `"x"`},
	} {
		t.Run(tc.value, func(t *testing.T) {
			raw := "Content-Type: application/x-y; name*=" + tc.value + "\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `("NAME" `+tc.want+`)`)
		})
	}
}

// TestStructureParameterInCharset: a boundary and a charset name are ASCII whatever
// the charset their RFC 2231 form names (RFC 2046 §5.1.1, RFC 2045 §5.1), and the
// structure depends on them, so they go on being read rather than becoming words.
//
// checklist: IMAP-BS-13
func TestStructureParameterInCharset(t *testing.T) {
	const raw = "Content-Type: multipart/mixed; boundary*=windows-1252''B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain; charset*=windows-1252''utf-8\r\n\r\nGr\xc3\xbc\xc3\x9fe\r\n" +
		"--B\r\nContent-Type: application/x-y\r\n\r\nz\r\n--B--\r\n"
	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"),
		`("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 7 1`,
		`("APPLICATION" "X-Y" NIL NIL NIL "7BIT" 1`,
		`("BOUNDARY" "B")`)
	_, part := fetchLiteral(t, c, "BODY.PEEK[1]")
	if part != "Gr\xc3\xbc\xc3\x9fe" {
		t.Fatalf("BODY[1] = %q", part)
	}
}

// TestHeaderSectionKeepsEveryParameter: converting an 8-bit parameter changes that
// parameter alone. The others stay as they were sent, in their order, RFC 2231 text
// and comments included, and what comes out still reads as the same parameters.
//
// checklist: IMAP-BS-13 IMAP-INV-TEXT-02
func TestHeaderSectionKeepsEveryParameter(t *testing.T) {
	for _, tc := range []struct{ name, stored, want, structure string }{
		{"charset beside 8-bit", "Content-Type: application/x-y; name*=windows-1252''x%F6.bin; note=\"Gr\xc3\xbc\"",
			"Content-Type: application/x-y;\r\n name*=windows-1252''x%F6.bin;\r\n note*=UTF-8''Gr%C3%BC",
			`("NAME" "=?windows-1252?Q?x=F6=2Ebin?=" "NOTE" "=?UTF-8?Q?Gr=C3=BC?=")`},
		{"continuation beside 8-bit", "Content-Type: application/x-y; name*0*=windows-1252''J%F6; name*1=\"rg\"; note=\"caf\xe9\"",
			"Content-Type: application/x-y;\r\n name*0*=windows-1252''J%F6;\r\n name*1=\"rg\";\r\n note*=UNKNOWN-8BIT''caf%E9",
			`("NAME" "=?windows-1252?Q?J=F6rg?=" "NOTE" "=?UNKNOWN-8BIT?Q?caf=E9?=")`},
		{"disposition", "Content-Type: application/x-y\r\nContent-Disposition: attachment; filename*=iso-8859-2''%A3.txt; note=\"Gr\xc3\xbc\"",
			"Content-Type: application/x-y\r\nContent-Disposition: attachment;\r\n filename*=iso-8859-2''%A3.txt;\r\n note*=UTF-8''Gr%C3%BC",
			`("ATTACHMENT" ("FILENAME" "=?iso-8859-2?Q?=A3=2Etxt?=" "NOTE" "=?UTF-8?Q?Gr=C3=BC?="))`},
		{"comments", "Content-Type: application/x-y (J\xc3\xb6rg); a=b (kept); note=\"Gr\xc3\xbc\" (caf\xe9)",
			"Content-Type: application/x-y (=?UTF-8?Q?J=C3=B6rg?=);\r\n a=b (kept);\r\n note*=UTF-8''Gr%C3%BC (=?UNKNOWN-8BIT?Q?caf=E9?=)",
			`("A" "b" "NOTE" "=?UTF-8?Q?Gr=C3=BC?=")`},
		{"case and order", "Content-Type: Application/X-Y; Zed=1; Note=\"Gr\xc3\xbc\"; alpha=\"2 3\"",
			"Content-Type: Application/X-Y;\r\n Zed=1;\r\n Note*=UTF-8''Gr%C3%BC;\r\n alpha=\"2 3\"",
			`("ALPHA" "2 3" "NOTE" "=?UTF-8?Q?Gr=C3=BC?=" "ZED" "1")`},
		{"quoted pair", "Content-Type: application/x-y; note=\"a\\\"Gr\xc3\xbc\"",
			"Content-Type: application/x-y;\r\n note*=UTF-8''a%22Gr%C3%BC",
			`("NOTE" "=?UTF-8?Q?a=22Gr=C3=BC?=")`},
		{"8-bit beside its extended form", "Content-Disposition: attachment; filename=\"J\xc3\xb6rg\"; filename*=windows-1252''J%F6rg",
			"Content-Disposition: attachment;\r\n filename=\"=?UTF-8?Q?J=C3=B6rg?=\";\r\n filename*=windows-1252''J%F6rg",
			`("ATTACHMENT" ("FILENAME" "=?windows-1252?Q?J=F6rg?="))`},
		{"8-bit in an extended value", "Content-Type: application/x-y; name*=\"windows-1252''J\xf6rg\"",
			"Content-Type: application/x-y;\r\n name*=\"windows-1252''J%F6rg\"",
			`("NAME" "=?windows-1252?Q?J=F6rg?=")`},
		{"8-bit first segment", "Content-Type: application/x-y; name*0=\"J\xc3\xb6\"; name*1=\"rg\"",
			"Content-Type: application/x-y;\r\n name*0*=UTF-8''J%C3%B6;\r\n name*1=\"rg\"",
			`("NAME" "=?UTF-8?Q?J=C3=B6rg?=")`},
		{"8-bit later segment", "Content-Type: application/x-y; name*0=\"J\"; name*1=\"\xc3\xb6rg\"",
			"Content-Type: application/x-y;\r\n name*0=\"J\";\r\n name*1*=%C3%B6rg",
			`("NAME" "=?UTF-8?Q?J=C3=B6rg?=")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.stored + "\r\n\r\nbody\r\n"
			box := sevenBitBox(raw)
			c := fetchClient(t, box, "EXAMINE")
			_, header := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
			if header != tc.want+"\r\n\r\n" {
				t.Fatalf("HEADER = %q\n  want %q", header, tc.want+"\r\n\r\n")
			}
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), tc.structure)
			if box.raw[10] != raw {
				t.Fatalf("stored octets changed: %q", box.raw[10])
			}

			// A part's MIME header and an encapsulated message's follow the same rule.
			c = fetchClient(t, sevenBitBox("Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n"+tc.stored+"\r\n\r\nx\r\n--B--\r\n"), "EXAMINE")
			_, mimeHeader := fetchLiteral(t, c, "BODY.PEEK[1.MIME]")
			if mimeHeader != tc.want+"\r\n\r\n" {
				t.Fatalf("1.MIME = %q", mimeHeader)
			}
			c = fetchClient(t, sevenBitBox("Content-Type: message/rfc822\r\n\r\n"+raw), "EXAMINE")
			_, inner := fetchLiteral(t, c, "BODY.PEEK[1.HEADER]")
			if inner != tc.want+"\r\n\r\n" {
				t.Fatalf("1.HEADER = %q", inner)
			}
		})
	}
}

// TestHeaderSectionKeepsParameterInUTF8Session: a field of valid UTF-8 stays as it
// was sent after UTF8=ACCEPT, and one that isn't valid is still rewritten without
// losing its other parameters.
//
// checklist: IMAP-BS-13 IMAP-INV-TEXT-02
func TestHeaderSectionKeepsParameterInUTF8Session(t *testing.T) {
	for _, tc := range []struct{ stored, want string }{
		{"Content-Type: application/x-y; name*=windows-1252''x%F6.bin; note=\"Gr\xc3\xbc\"", ""},
		{"Content-Type: application/x-y; name*=windows-1252''x%F6.bin; note=\"caf\xe9\"",
			"Content-Type: application/x-y;\r\n name*=windows-1252''x%F6.bin;\r\n note*=UNKNOWN-8BIT''caf%E9"},
	} {
		want := tc.want
		if want == "" {
			want = tc.stored
		}
		_, header := fetchUTF8Metadata(t, tc.stored+"\r\n\r\nbody\r\n", "BODY.PEEK[HEADER]")
		if header != want+"\r\n\r\n" {
			t.Fatalf("HEADER = %q\n  want %q", header, want+"\r\n\r\n")
		}
	}
}
