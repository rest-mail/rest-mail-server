package server

import (
	"regexp"
	"testing"
)

// FuzzSearchQuotes compares quote framing to a regular-expression grammar,
// including the materialized-literal representation's CR/LF and 8-bit bytes.
func FuzzSearchQuotes(f *testing.F) {
	for _, body := range []string{"", "hello", `a\"b`, `a\\b`, `a\" (b) \\c`, `bad\n`, `end\`, `" ALL "`, "café\r\nnext", "a\x00b"} {
		f.Add(body)
	}
	escapes := regexp.MustCompile(`\\(["\\])`)
	grammar := regexp.MustCompile(`\A"([^"\\\x00]|\\["\\])*"\z`)
	f.Fuzz(func(t *testing.T, body string) {
		token := `"` + body + `"`
		tokens, ok := tokenizeSearch(token)
		got := ok && len(tokens) == 1 && tokens[0] == token
		if want := grammar.MatchString(token); got != want {
			t.Fatalf("tokenizeSearch(%q) = %q, %t; single quoted token = %t", token, tokens, ok, want)
		}
		if got {
			criteria, err := (&Session{}).parseSearchCriteria("SUBJECT " + token)
			if err != nil || len(criteria) != 1 || criteria[0].value != escapes.ReplaceAllString(body, "$1") {
				t.Fatalf("quoted criterion: criteria=%+v err=%v", criteria, err)
			}
		}
	})
}
