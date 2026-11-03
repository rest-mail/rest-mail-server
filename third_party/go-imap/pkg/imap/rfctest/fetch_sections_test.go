package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// crlf converts a "\n"-separated literal into wire CRLF form.
func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

// The message of the RFC 3501 §6.4.5 part-numbering example.
var (
	sectionHeader   = crlf("Subject: outer\nMIME-Version: 1.0\nContent-Type: MULTIPART/MIXED; boundary=\"outer\"\n\n")
	section1MIME    = crlf("Content-Type: TEXT/PLAIN\n\n")
	section1        = "part one"
	section2MIME    = crlf("Content-Type: APPLICATION/OCTET-STREAM\n\n")
	section2        = "AAEC"
	section3Header  = crlf("Subject: inner three\nContent-Type: multipart/mixed; boundary=b3\n\n")
	section3Text    = crlf("--b3\n\nthree one\n--b3\nContent-Type: application/octet-stream\n\nAwQ=\n--b3--\n")
	section3        = section3Header + section3Text
	section41MIME   = crlf("Content-Type: IMAGE/GIF\nContent-Transfer-Encoding: base64\n\n")
	section41       = "R0lG"
	section42Header = crlf("Subject: inner four\nContent-Type: multipart/mixed; boundary=b42\n\n")
	section4221     = "alt plain"
	section4222     = "alt <bold>rich</bold>"
	section422      = crlf("--alt  \nContent-Type: text/plain\n\n") + section4221 + crlf("\n--alt\nContent-Type: text/richtext\n\n") + section4222 + crlf("\n--alt--")
	section42Text   = crlf("preamble\n--b42\nContent-Type: text/plain\n\nfour two one\n--b42\nContent-Type: multipart/alternative; boundary=alt\n\n") + section422 + crlf("\n--b42--\nepilogue\n")
	section42       = section42Header + section42Text
	section4        = crlf("--b4\n") + section41MIME + section41 + crlf("\n--b4\nContent-Type: MESSAGE/RFC822\n\n") + section42 + crlf("\n--b4--")
	sectionText     = crlf("--outer\n") + section1MIME + section1 + crlf("\n--outer\n") + section2MIME + section2 +
		crlf("\n--outer\nContent-Type: MESSAGE/RFC822\n\n") + section3 +
		crlf("\n--outer\nContent-Type: MULTIPART/MIXED; boundary=b4\n\n") + section4 + crlf("\n--outer--\n")
	sectionRaw = sectionHeader + sectionText
)

// TestFetchNumericSections fetches every part of the RFC 3501 §6.4.5 example
// structure, including nested MESSAGE/RFC822 HEADER/TEXT and MIME headers.
// Part numbers of a MESSAGE/RFC822 part address the encapsulated message's
// parts; a non-multipart message has only part 1.
//
// checklist: IMAP-FETCH-14
// checklist: IMAP2-CMD-152
func TestFetchNumericSections(t *testing.T) {
	for _, tc := range []struct{ section, label, payload string }{
		{"HEADER", "HEADER", sectionHeader},
		{"TEXT", "TEXT", sectionText},
		{"1", "1", section1},
		{"1.MIME", "1.MIME", section1MIME},
		{"2", "2", section2},
		{"2.mime", "2.MIME", section2MIME},
		{"3", "3", section3},
		{"3.HEADER", "3.HEADER", section3Header},
		{"3.TEXT", "3.TEXT", section3Text},
		{"3.HEADER.FIELDS (subject)", "3.HEADER.FIELDS (subject)", crlf("Subject: inner three\n\n")},
		{"3.HEADER.FIELDS.NOT (Subject)", "3.HEADER.FIELDS.NOT (Subject)", crlf("Content-Type: multipart/mixed; boundary=b3\n\n")},
		{"3.1", "3.1", crlf("three one")},
		{"3.1.MIME", "3.1.MIME", "\r\n"},
		{"3.2", "3.2", "AwQ="},
		{"4", "4", section4},
		{"4.1", "4.1", section41},
		{"4.1.MIME", "4.1.MIME", section41MIME},
		{"4.2", "4.2", section42},
		{"4.2.HEADER", "4.2.HEADER", section42Header},
		{"4.2.TEXT", "4.2.TEXT", section42Text},
		{"4.2.1", "4.2.1", "four two one"},
		{"4.2.2", "4.2.2", section422},
		{"4.2.2.1", "4.2.2.1", section4221},
		{"4.2.2.2", "4.2.2.2", section4222},
		{"4.2.2.2.MIME", "4.2.2.2.MIME", crlf("Content-Type: text/richtext\n\n")},
		// Absent parts and message-text specifiers on non-message parts are empty.
		{"5", "5", ""},
		{"1.1", "1.1", ""},
		{"4.3.MIME", "4.3.MIME", ""},
		{"1.HEADER", "1.HEADER", ""},
		{"4.TEXT", "4.TEXT", ""},
		{"4294967295", "4294967295", ""},
	} {
		t.Run(tc.section, func(t *testing.T) {
			box := newFetchMailbox()
			box.raw[10] = sectionRaw
			c := fetchClient(t, box, "SELECT")
			replies, tagged := fetchCommand(t, c, "FETCH 1 BODY.PEEK["+tc.section+"]")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{tc.payload}) {
				t.Fatalf("replies %#v, want payload %q", replies, tc.payload)
			}
			label := fmt.Sprintf("BODY[%s] {%d}", tc.label, len(tc.payload))
			if !strings.Contains(replies[0].framing, label) || len(box.updates) != 0 {
				t.Fatalf("framing %q (updates %v), want %q and no Seen write", replies[0].framing, box.updates, label)
			}
		})
	}
}

// TestFetchNumericSectionSingle covers a non-multipart message, whose only
// part is 1 (its body), and a partial of a numeric section.
//
// checklist: IMAP-FETCH-14
// checklist: IMAP2-CMD-152
func TestFetchNumericSectionSingle(t *testing.T) {
	raw := crlf("Subject: one\nContent-Type: text/plain\n\nhello world\n")
	for _, tc := range []struct{ item, label, payload string }{
		{"BODY.PEEK[1]", "BODY[1]", crlf("hello world\n")},
		{"BODY.PEEK[1.MIME]", "BODY[1.MIME]", crlf("Subject: one\nContent-Type: text/plain\n\n")},
		{"BODY.PEEK[1]<6.5>", "BODY[1]<6>", "world"},
		{"BODY.PEEK[2]", "BODY[2]", ""},
		{"BODY.PEEK[1.1]", "BODY[1.1]", ""},
	} {
		t.Run(tc.item, func(t *testing.T) {
			box := newFetchMailbox()
			box.raw[10] = raw
			c := fetchClient(t, box, "SELECT")
			replies, tagged := fetchCommand(t, c, "FETCH 1 "+tc.item)
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{tc.payload}) {
				t.Fatalf("replies %#v, want payload %q", replies, tc.payload)
			}
			if label := fmt.Sprintf("%s {%d}", tc.label, len(tc.payload)); !strings.Contains(replies[0].framing, label) {
				t.Fatalf("framing %q, want %q", replies[0].framing, label)
			}
		})
	}
}

// TestFetchInvalidSection rejects malformed section-specs with BAD before any
// message is loaded or Seen changes: zero or leading-zero part numbers,
// overflow, empty components, MIME without a part and unknown section-text.
//
// checklist: IMAP-FETCH-14
func TestFetchInvalidSection(t *testing.T) {
	for _, section := range []string{
		"0", "01", "1.0", "1.01", "4294967296", "1.", ".1", "1..2", "1.2.",
		"MIME", "1.FOO", "FOO", "1HEADER", "1.MIME.1", "1.HEADER.1", "-1", "+1", "1 ", " 1",
	} {
		t.Run(section, func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			replies, tagged := fetchCommand(t, c, "FETCH 1 (BODY[] BODY["+section+"])")
			if !strings.HasPrefix(tagged, "F1 BAD ") || len(replies) != 0 {
				t.Errorf("replies=%#v tagged=%q, want only BAD", replies, tagged)
			}
			if len(box.fetches) != 0 || len(box.updates) != 0 {
				t.Errorf("invalid section accessed backend: fetches=%v updates=%v", box.fetches, box.updates)
			}
		})
	}
}
