package wire

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// FuzzHeaderFields compares the section parser with an independent RFC 3501 §9
// regexp grammar and checks decoded values and preserved response tokens. The
// ASCII restriction is inherited from ParseFetchItems, including spliced literals.
func FuzzHeaderFields(f *testing.F) {
	for _, seed := range []string{
		"HEADER.FIELDS (Subject From)", `header.fields.not ("Subject" "X-\"Quote" "X-\\Slash")`,
		`HEADER.FIELDS ("" " spaced name " X-[ X-])`, `HEADER.FIELDS ()`,
		`HEADER.FIELDS  (Subject)`, `HEADER.FIELDS ("Sub\ject")`,
		`HEADER.FIELDS ("Subject"FROM)`, `HEADER.FIELDS (Subject) EXTRA`,
		"HEADER.FIELDS (\"\x00\")", "HEADER.FIELDS (\"\r\n\")", "HEADER.FIELDS (\"\xff\")",
	} {
		f.Add(seed)
	}
	atom := `[^\x00-\x20\x7f-\xff(){%*"\\]+`
	quoted := `"(?:[\x01-\x09\x0b\x0c\x0e-\x21\x23-\x5b\x5d-\x7e]|\\["\\])*"`
	token := `(?:` + atom + `|` + quoted + `)`
	grammar := regexp.MustCompile(`(?i)^HEADER\.FIELDS(?:\.NOT)? \(` + token + `(?: ` + token + `)*\)$`)
	tokens := regexp.MustCompile(token)
	decode := strings.NewReplacer(`\"`, `"`, `\\`, `\`)
	f.Fuzz(func(t *testing.T, input string) {
		fields, wire, exclude, ok := ParseHeaderFields(input)
		ascii := true
		for i := range len(input) {
			ascii = ascii && input[i] < 0x7f
		}
		if ok != (ascii && grammar.MatchString(input)) {
			t.Fatalf("grammar disagrees for %q: accepted=%t", input, ok)
		}
		if !ok {
			return
		}
		start := strings.IndexByte(input, '(')
		wantWire := tokens.FindAllString(input[start+1:len(input)-1], -1)
		wantFields := make([]string, len(wantWire))
		for i, tok := range wantWire {
			if tok[0] == '"' {
				wantFields[i] = decode.Replace(tok[1 : len(tok)-1])
			} else {
				wantFields[i] = tok
			}
		}
		if !reflect.DeepEqual(fields, wantFields) || !reflect.DeepEqual(wire, wantWire) || exclude != strings.HasPrefix(strings.ToUpper(input), "HEADER.FIELDS.NOT ") {
			t.Fatalf("input %q: got %q/%q exclude=%t, want %q/%q", input, fields, wire, exclude, wantFields, wantWire)
		}
		item := "BODY.PEEK[" + input + "]"
		if _, valid := ParseFetchItems(item); !valid {
			t.Fatalf("valid section refused in %q", item)
		}
	})
}
