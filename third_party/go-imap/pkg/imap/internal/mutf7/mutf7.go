package mutf7

import (
	"encoding/base64"
	"strings"
	"unicode/utf16"
)

// mutf7Base64 is modified BASE64: base64 with "," for "/" and no padding.
var mutf7Base64 = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+,").WithPadding(base64.NoPadding).Strict()

// Encode writes a UTF-8 mailbox name in modified UTF-7 (RFC 3501 §5.1.3):
// printable US-ASCII other than "&" stands for itself and "&" is "&-"; each
// run of other characters is "&", their UTF-16 in modified BASE64, then "-".
func Encode(name string) string {
	var b strings.Builder
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		units := utf16.Encode(run)
		buf := make([]byte, 0, 2*len(units))
		for _, u := range units {
			buf = append(buf, byte(u>>8), byte(u))
		}
		b.WriteByte('&')
		b.WriteString(mutf7Base64.EncodeToString(buf))
		b.WriteByte('-')
		run = run[:0]
	}
	for _, r := range name {
		switch {
		case r == '&':
			flush()
			b.WriteString("&-")
		case r >= 0x20 && r <= 0x7e:
			flush()
			b.WriteRune(r)
		default:
			run = append(run, r)
		}
	}
	flush()
	return b.String()
}

// Decode reads a modified UTF-7 mailbox name as UTF-8. ok is false unless
// name is exactly what Encode writes for what it decodes to, so a name
// with an octet outside printable US-ASCII, a base64 run not closed by "-", a
// run encoding a character that can represent itself, a superfluous shift
// between runs, nonzero padding bits or an unpaired surrogate is refused
// (RFC 3501 §5.1.3). The base64 is compared exactly, so its case is kept.
func Decode(name string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(name); {
		c := name[i]
		if c < 0x20 || c > 0x7e {
			return "", false
		}
		if c != '&' {
			b.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(name[i+1:], '-')
		if end < 0 {
			return "", false
		}
		run := name[i+1 : i+1+end]
		i += end + 2
		if run == "" {
			b.WriteByte('&')
			continue
		}
		buf, err := mutf7Base64.DecodeString(run)
		if err != nil || len(buf)%2 != 0 {
			return "", false
		}
		units := make([]uint16, len(buf)/2)
		for j := range units {
			units[j] = uint16(buf[2*j])<<8 | uint16(buf[2*j+1])
		}
		for _, r := range utf16.Decode(units) {
			b.WriteRune(r)
		}
	}
	decoded := b.String()
	if Encode(decoded) != name {
		return "", false
	}
	return decoded, true
}
