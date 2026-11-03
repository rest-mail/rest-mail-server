package message

import (
	"math/big"
	"regexp"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// FuzzSectionSpec compares numeric section-part parsing with the RFC 3501 §9
// grammar expressed independently as a regexp plus arbitrary-precision bounds,
// and resolves the section against an arbitrary message, which must never
// panic. Unfiltered sections must return contiguous message octets; filtered
// headers may join non-adjacent lines and end each with CRLF, followed by the
// blank line only when the message has one (RFC 3501 §6.4.5).
func FuzzSectionSpec(f *testing.F) {
	nested := "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\n\r\none\r\n" +
		"--b\r\nContent-Type: message/rfc822\r\n\r\nSubject: in\r\n\r\ntwo\r\n--b--\r\n"
	for _, section := range []string{
		"", "1", "1.2.3", "1.MIME", "2.HEADER", "2.TEXT", "2.1", "2.HEADER.FIELDS (Subject)",
		"0", "01", "1.", "1..2", "4294967295", "4294967296", "MIME", "1.FOO", "1HEADER",
	} {
		f.Add(section, nested)
	}
	f.Add("1.1", "Content-Type: multipart/mixed; boundary=x\r\n\r\n--x  \r\n\r\na\r\n--xy\r\n--x--")
	// Filtered headers need not be contiguous in raw, and a truncated header
	// acquires its terminating CRLF but no blank line, which it does not have.
	f.Add("2.HEADER.FIELDS (SuBjeCt)", strings.Replace(nested, "Subject: in\r\n\r\ntwo", "SuBjeCt:", 1))
	f.Add("2.HEADER.FIELDS (Subject)", strings.Replace(nested, "Subject: in", "Subject: in\r\nX-Omitted: middle\r\nSubject: again", 1))
	f.Add("2.HEADER.FIELDS.NOT (X-Omitted)", strings.Replace(nested, "Subject: in", "Subject: in\r\nX-Omitted: middle\r\nFrom: sender", 1))
	grammar := regexp.MustCompile(`(?s)^((?:[1-9][0-9]*\.)*[1-9][0-9]*)?(?:\.?)(.*)$`)
	f.Fuzz(func(t *testing.T, section, raw string) {
		if len(section) > 1024 || len(raw) > 1<<16 {
			t.Skip()
		}
		path, text, ok := wire.SplitSectionPart(section)
		groups := grammar.FindStringSubmatch(section)
		wantOK := groups != nil
		if wantOK && groups[1] != "" {
			// A part path must be followed by nothing or "." and section-text.
			rest := section[len(groups[1]):]
			wantOK = rest == "" || (rest[0] == '.' && len(rest) > 1 && (rest[1] < '0' || rest[1] > '9'))
			for _, n := range strings.Split(groups[1], ".") {
				var v big.Int
				v.SetString(n, 10)
				wantOK = wantOK && v.BitLen() <= 32
			}
		} else if wantOK && section != "" && section[0] >= '0' && section[0] <= '9' {
			wantOK = false // starts with a digit but is not an nz-number path
		}
		if ok != wantOK {
			t.Fatalf("SplitSectionPart(%q) = %v, %q, %t; grammar accepts %t", section, path, text, ok, wantOK)
		}
		if !ok || len(path) == 0 {
			return
		}
		_, payload, valid := numericSection(raw, path, text)
		if !valid || payload == "" {
			return
		}
		if strings.HasPrefix(strings.ToUpper(text), "HEADER.FIELDS") {
			if !strings.HasSuffix(payload, "\r\n") {
				t.Fatalf("filtered headers lack a terminating CRLF: %q", payload)
			}
			// Every retained line must occur verbatim and in order in raw.
			remaining := raw
			for _, line := range strings.Split(strings.TrimSuffix(payload, "\r\n"), "\r\n") {
				if line == "" {
					continue
				}
				i := strings.Index(remaining, line)
				if i < 0 {
					t.Fatalf("numericSection(%q) header line %q is not in message order", section, line)
				}
				remaining = remaining[i+len(line):]
			}
		} else if !strings.Contains(raw, payload) {
			t.Fatalf("numericSection(%q) payload %q is not part of the message", section, payload)
		}
	})
}
