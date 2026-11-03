package message

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestTransferEncodingLexicalEdges(t *testing.T) {
	for _, value := range []string{" ", "\t(comment)", "base64 (dangling\\", "base64\r\n", "base64\x00", "x-é", ")base64", "(unfinished", "base(comment)64"} {
		if got := transferEncoding(value); got != strings.ToUpper(value) {
			t.Errorf("transferEncoding(%q) = %q, want opaque %q", value, got, strings.ToUpper(value))
		}
	}
}

// Exercise arbitrary malformed syntax, plus a grammar-generated token with
// independently constructed CFWS. Hex encodes arbitrary comment bytes so the
// comment body remains legal ASCII; fixed quoted-pairs cover both parentheses.
func FuzzTransferEncoding(f *testing.F) {
	for _, seed := range []string{"", "base64", "(outer (inner)\\))", "x-é", "base(comment)64", "\x00\r\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_ = transferEncoding(input)
		token := "x-MiXeD." + hex.EncodeToString([]byte(input))
		comment := "(outer (" + hex.EncodeToString([]byte(input)) + `) escaped \) \()`
		value := comment + "\t" + token + " " + comment
		if got, want := transferEncoding(value), strings.ToUpper(token); got != want {
			t.Fatalf("transferEncoding(%q) = %q, want %q", value, got, want)
		}
		// Comments separate lexical tokens; they cannot glue a mechanism.
		split := token + "(comment)extra"
		if got := transferEncoding(split); got != strings.ToUpper(split) {
			t.Fatalf("joined tokens: %q -> %q", split, got)
		}
	})
}
