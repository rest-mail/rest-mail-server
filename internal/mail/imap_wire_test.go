package mail

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
)

// IMAPWireSize is the length of what a FETCH of the whole message sends, and a
// message already 7-bit measures its stored size in both kinds of session.
func TestIMAPWireSize(t *testing.T) {
	const ascii = "Subject: x\r\n\r\nbody\r\n"
	for _, utf8 := range []bool{false, true} {
		if got := IMAPWireSize(ascii, utf8); got != len(ascii) {
			t.Fatalf("utf8=%v: %d, want %d", utf8, got, len(ascii))
		}
	}
	const raw = "Subject: Grüße\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: binary\r\n\r\n\xff\xfe\r\n"
	if got, want := IMAPWireSize(raw, false), len(wireform.Convert(DowngradeHeaders(raw), false)); got != want {
		t.Fatalf("without UTF-8: %d, want %d", got, want)
	}
	if got, want := IMAPWireSize(raw, true), len(wireform.Convert(raw, true)); got != want {
		t.Fatalf("with UTF-8: %d, want %d", got, want)
	}
	if IMAPWireSize(raw, false) == len(raw) || IMAPWireSize(raw, true) == len(raw) {
		t.Fatal("binary data was not counted in its encoded form")
	}
}

// Octets that are not UTF-8 are never labelled UTF-8 by the downgrade: they go
// out as UNKNOWN-8BIT, and a media type keeps its meaning.
func TestDowngradeNotUTF8UsesUnknown8bit(t *testing.T) {
	const raw = "Subject: caf\xe9\r\nX-Note: caf\xe9\r\n" +
		"Content-Type: text/plain; charset=iso-8859-1; name=\"caf\xe9.txt\"\r\n\r\nbod\xe9\r\n"
	got := DowngradeHeaders(raw)
	header, body, _ := strings.Cut(got, "\r\n\r\n")
	if containsNonASCII(header) {
		t.Fatalf("header is not ASCII: %q", header)
	}
	for _, want := range []string{
		"Subject: =?UNKNOWN-8BIT?Q?caf=E9?=\r\n",
		"Downgraded-X-Note: =?UNKNOWN-8BIT?Q?caf=E9?=\r\n",
		"Content-Type: text/plain;\r\n charset=\"iso-8859-1\";\r\n name*=UNKNOWN-8BIT''caf%E9.txt",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
	if body != "bod\xe9\r\n" {
		t.Fatalf("the downgrade changed the body: %q", body)
	}
}
