package message

import (
	"strings"
	"testing"
)

// A text/plain part with an explicit charset yields the exact non-extensible
// (BODY) and extensible (BODYSTRUCTURE) forms of RFC 3501 §7.4.2, matching the
// worked example in rest-mail-server#204: type, subtype, the parenthesized
// content-type params, id, description, encoding, octet size, line count — and,
// for BODYSTRUCTURE, the four single-part extension fields (MD5, disposition,
// language, location) all NIL when the message carries none.
func TestBuildBodyStructure_TextUTF8Exact(t *testing.T) {
	raw := "Content-Type: text/plain; charset=utf-8\r\n\r\nHello, world!\r\n"
	if got, want := BuildBodyStructure(raw, false),
		`("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 15 1)`; got != want {
		t.Errorf("BODY = %q, want %q", got, want)
	}
	if got, want := BuildBodyStructure(raw, true),
		`("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 15 1 NIL NIL NIL NIL)`; got != want {
		t.Errorf("BODYSTRUCTURE = %q, want %q", got, want)
	}
}

// The BODYSTRUCTURE disposition field (body-fld-dsp, RFC 3501 §7.4.2) is the
// Content-Disposition of the part rendered as (disp-type (params)) — not a blanket
// NIL. The bare BODY form omits extension data entirely, so it must stay unchanged
// whether or not a Content-Disposition is present.
func TestBuildBodyStructure_SinglePartDisposition(t *testing.T) {
	raw := "Content-Type: application/pdf; name=doc.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=doc.pdf\r\n\r\nAAAA\r\n"

	if got, want := BuildBodyStructure(raw, true),
		`("APPLICATION" "PDF" ("NAME" "doc.pdf") NIL NIL "BASE64" 6 NIL ("ATTACHMENT" ("FILENAME" "doc.pdf")) NIL NIL)`; got != want {
		t.Errorf("BODYSTRUCTURE = %q, want %q", got, want)
	}
	// BODY carries no extension data: the disposition must not leak into it.
	if got, want := BuildBodyStructure(raw, false),
		`("APPLICATION" "PDF" ("NAME" "doc.pdf") NIL NIL "BASE64" 6)`; got != want {
		t.Errorf("BODY = %q, want %q", got, want)
	}
}

// A disposition with no parameters (e.g. bare "inline") renders as (disp-type NIL),
// keeping the two-element body-fld-dsp shape.
func TestBuildBodyStructure_DispositionNoParams(t *testing.T) {
	raw := "Content-Type: image/png\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: inline\r\n\r\nAAAA\r\n"
	got := BuildBodyStructure(raw, true)
	if !strings.Contains(got, `NIL ("INLINE" NIL) NIL NIL)`) {
		t.Errorf("BODYSTRUCTURE disposition = %q, want ...NIL (\"INLINE\" NIL) NIL NIL)", got)
	}
}

// A nested multipart (multipart/mixed wrapping a multipart/alternative plus a
// disposition-bearing attachment) must recurse correctly and surface the
// attachment's Content-Disposition in BODYSTRUCTURE. The exact form pins both the
// recursion (nested part structures adjacent, no separator) and the extension data
// (body-ext-mpart param/dsp/lang/loc for each multipart level, body-fld-dsp for the
// attachment).
func TestBuildBodyStructure_NestedMultipartDisposition(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=MIX\r\n\r\n" +
		"--MIX\r\n" +
		"Content-Type: multipart/alternative; boundary=ALT\r\n\r\n" +
		"--ALT\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhello\r\n" +
		"--ALT\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<b>hello</b>\r\n" +
		"--ALT--\r\n" +
		"--MIX\r\n" +
		"Content-Type: application/pdf; name=report.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=report.pdf\r\n\r\nQUFB\r\n" +
		"--MIX--\r\n"

	want := `(` +
		`(` +
		`("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 5 1 NIL NIL NIL NIL)` +
		`("TEXT" "HTML" ("CHARSET" "utf-8") NIL NIL "7BIT" 12 1 NIL NIL NIL NIL)` +
		` "ALTERNATIVE" ("BOUNDARY" "ALT") NIL NIL NIL)` +
		`("APPLICATION" "PDF" ("NAME" "report.pdf") NIL NIL "BASE64" 4 NIL ("ATTACHMENT" ("FILENAME" "report.pdf")) NIL NIL)` +
		` "MIXED" ("BOUNDARY" "MIX") NIL NIL NIL)`

	if got := BuildBodyStructure(raw, true); got != want {
		t.Errorf("BODYSTRUCTURE =\n  %q\nwant\n  %q", got, want)
	}
}

// A MESSAGE/RFC822 part carries, immediately after the basic single-part fields,
// the envelope, body structure, and line count of the ENCAPSULATED message
// (body-type-msg, RFC 3501 §7.4.2), then the single-part extension fields for
// BODYSTRUCTURE. The bare BODY form supplies the same envelope/body/lines but omits
// the trailing extension fields.
func TestBuildBodyStructure_MessageRFC822(t *testing.T) {
	inner := "From: a@b.com\r\nSubject: Inner\r\n\r\nhi\r\n"
	raw := "Content-Type: message/rfc822\r\n\r\n" + inner

	env := `(NIL "Inner" ((NIL NIL "a" "b.com")) ((NIL NIL "a" "b.com")) ((NIL NIL "a" "b.com")) NIL NIL NIL NIL NIL)`
	innerBS := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1 NIL NIL NIL NIL)`

	wantBS := `("MESSAGE" "RFC822" NIL NIL NIL "7BIT" 37 ` + env + ` ` + innerBS + ` 4 NIL NIL NIL NIL)`
	if got := BuildBodyStructure(raw, true); got != wantBS {
		t.Errorf("BODYSTRUCTURE =\n  %q\nwant\n  %q", got, wantBS)
	}

	innerBody := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1)`
	wantBody := `("MESSAGE" "RFC822" NIL NIL NIL "7BIT" 37 ` + env + ` ` + innerBody + ` 4)`
	if got := BuildBodyStructure(raw, false); got != wantBody {
		t.Errorf("BODY =\n  %q\nwant\n  %q", got, wantBody)
	}
}

// A single text/plain part must yield the exact non-extensible (BODY) and
// extensible (BODYSTRUCTURE) forms of RFC 3501 §7.4.2.
func TestBuildBodyStructure_SingleTextExact(t *testing.T) {
	raw := "Subject: Hi\r\n\r\nbody 5\r\n" // no Content-Type → text/plain; charset US-ASCII
	if got, want := BuildBodyStructure(raw, false),
		`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 8 1)`; got != want {
		t.Errorf("BODY = %q, want %q", got, want)
	}
	if got, want := BuildBodyStructure(raw, true),
		`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 8 1 NIL NIL NIL NIL)`; got != want {
		t.Errorf("BODYSTRUCTURE = %q, want %q", got, want)
	}
}

// A non-text single part carries no line count, and its declared encoding and
// parameters are reported.
func TestBuildBodyStructure_NonText(t *testing.T) {
	raw := "Content-Type: application/pdf; name=doc.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nAAAA\r\n"
	got := BuildBodyStructure(raw, false)
	want := `("APPLICATION" "PDF" ("NAME" "doc.pdf") NIL NIL "BASE64" 6)`
	if got != want {
		t.Errorf("BODY = %q, want %q", got, want)
	}
}

// A multipart entity nests its parts, then subtype, then extension data. The
// parts are adjacent (no separator) and the whole structure is well-formed.
func TestBuildBodyStructure_Multipart(t *testing.T) {
	raw := "Content-Type: multipart/alternative; boundary=BB\r\n\r\n" +
		"--BB\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--BB\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<b>hi</b>\r\n" +
		"--BB--\r\n"
	got := BuildBodyStructure(raw, true)
	if !parensBalanced(got) {
		t.Fatalf("multipart structure unbalanced: %s", got)
	}
	if !strings.Contains(got, `"ALTERNATIVE"`) {
		t.Errorf("multipart subtype missing: %s", got)
	}
	if !strings.Contains(got, `"TEXT" "PLAIN"`) || !strings.Contains(got, `"TEXT" "HTML"`) {
		t.Errorf("multipart parts missing: %s", got)
	}
	// Nested parts are adjacent: a closing paren immediately followed by an open.
	if !strings.Contains(got, ")(") {
		t.Errorf("multipart parts should be adjacent with no separator: %s", got)
	}
}
