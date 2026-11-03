package mutf7

import (
	"regexp"
	"testing"
	"unicode/utf8"
)

// FuzzModifiedUTF7 checks the mailbox name converter against RFC 3501 §5.1.3,
// with the grammar written independently as a regexp: every UTF-8 name encodes
// to printable US-ASCII that matches it and decodes back unchanged, and any
// string the decoder takes matches it and is exactly the encoding of what it
// decodes to, so each name has one wire form.
func FuzzModifiedUTF7(f *testing.F) {
	for _, seed := range []string{
		"", "INBOX", "Tom & Jerry", "~peter/mail/&U,BTFw-/&ZeVnLIqe-", "&Jjo!", "&Jjo-!",
		"&U,BTFw-&ZeVnLIqe-", "&U,BTF2XlZyyKng-", "&AGE-", "&2D0-", "&-&-", "a\r\nb",
		"~peter/mail/台北/日本語", "\U0001F600", "&2D3eAA-",
	} {
		f.Add(seed)
	}
	grammar := regexp.MustCompile(`^(?:[\x20-\x25\x27-\x7e]|&-|&[A-Za-z0-9+,]+-)*$`)
	f.Fuzz(func(t *testing.T, s string) {
		if utf8.ValidString(s) {
			wire := Encode(s)
			if !grammar.MatchString(wire) {
				t.Fatalf("Encode(%q) = %q, outside the grammar", s, wire)
			}
			if back, ok := Decode(wire); !ok || back != s {
				t.Fatalf("Decode(Encode(%q)) = %q, %t", s, back, ok)
			}
		}
		decoded, ok := Decode(s)
		if !ok {
			return
		}
		if !grammar.MatchString(s) {
			t.Fatalf("Decode took %q, outside the grammar", s)
		}
		if !utf8.ValidString(decoded) || Encode(decoded) != s {
			t.Fatalf("Decode(%q) = %q, which encodes to %q", s, decoded, Encode(decoded))
		}
	})
}
