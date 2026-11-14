package mail

import (
	"strings"
	"testing"

	gopop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// POP3WireSize counts what the POP3 library sends, for every arrangement of the octets
// that matter to it, so a change in the library's wire form fails here.
func TestPOP3WireSize_MatchesTheLibrary(t *testing.T) {
	const alphabet = "\r\n.a"
	raw := make([]byte, 0, 7)
	var walk func()
	walk = func() {
		if got, want := POP3WireSize(string(raw)), gopop3.OctetCount(raw); got != want {
			t.Fatalf("POP3WireSize(%q) = %d, the library counts %d", raw, got, want)
		}
		if len(raw) == cap(raw) {
			return
		}
		for i := range len(alphabet) {
			raw = append(raw, alphabet[i])
			walk()
			raw = raw[:len(raw)-1]
		}
	}
	walk()
}

// Measuring holds no copy of the message, whatever its line endings, so it costs one pass
// over the octets and nothing that grows with them. A message with an ASCII header isn't
// copied by DowngradedSize either.
func TestPOP3Size_HoldsNoCopy(t *testing.T) {
	const lines = 1 << 16
	for name, raw := range map[string]string{
		"CRLF":    "Subject: hi\r\n\r\n" + strings.Repeat("line\r\n", lines),
		"bare LF": "Subject: hi\n\n" + strings.Repeat("line\n", lines),
		"bare CR": "Subject: hi\r\n\r\n" + strings.Repeat("line\r", lines),
		"no end":  "Subject: hi\r\n\r\n" + strings.Repeat("x", lines),
	} {
		if allocs := testing.AllocsPerRun(10, func() { POP3WireSize(raw) }); allocs != 0 {
			t.Errorf("%s: POP3WireSize made %v allocations", name, allocs)
		}
		if allocs := testing.AllocsPerRun(10, func() { DowngradedSize(raw) }); allocs != 0 {
			t.Errorf("%s: DowngradedSize made %v allocations", name, allocs)
		}
	}
}

// DowngradedSize counts the converted header and the line breaks RETR gives what follows it.
func TestDowngradedSize_CountsTheFormRetrSends(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      int
	}{
		{"ASCII header, CRLF", "Subject: hi\r\n\r\nbody\r\n", len("Subject: hi\r\n\r\nbody\r\n")},
		{"ASCII header, bare LF", "Subject: hi\n\nbody\n", len("Subject: hi\r\n\r\nbody\r\n")},
		{"ASCII header, no final line break", "Subject: hi\r\n\r\nbody", len("Subject: hi\r\n\r\nbody\r\n")},
		{"8-bit header, bare LF", "Subject: Grüße\n\nbody\n", len("Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n\r\nbody\r\n")},
		{"8-bit header, no final line break", "Subject: Grüße\r\n\r\nbody", len("Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n\r\nbody\r\n")},
		{"8-bit header only", "Subject: Grüße", len("Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n\r\n")},
		{"bare CR stays", "Subject: hi\r\n\r\na\rb\r\n", len("Subject: hi\r\n\r\na\rb\r\n")},
		{"empty", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DowngradedSize(tc.raw); got != tc.want {
				t.Fatalf("DowngradedSize(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}
