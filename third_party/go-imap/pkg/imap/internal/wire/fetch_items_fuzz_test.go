package wire

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// FuzzFetchItems compares section-free inputs against the RFC 3501 §9 grammar
// using a separate regular-expression oracle. Section-bearing inputs still
// exercise framing and the invariant that expanded items form a valid list.
func FuzzFetchItems(f *testing.F) {
	for _, seed := range []string{
		"FAST", "all", "FuLl", "FAſT", "FLAGſ", "FLAGS", "(FLAGS UID RFC822.SIZE)", "(FAST)",
		"FLAGS UID", "(BODY[] BOGUS)", "()", "(FLAGS  UID)", "BODY[][]",
		"BODY.PEEK[HEADER.FIELDS (DATE FROM)]<0.1>",
		`(FLAGS BODY.PEEK[HEADER.FIELDS ("X-]" "X-(" "X-\"Quote")])`,
		"(BODY[TEXT])", "(BODY[TEXT)", "(BODY[[TEXT]])", "FLAGS\x00", "(FLAGS\tUID)",
	} {
		f.Add(seed)
	}
	atom := `(?:ENVELOPE|FLAGS|INTERNALDATE|RFC822(?:\.HEADER|\.SIZE|\.TEXT)?|BODY(?:STRUCTURE)?|UID)`
	flat := regexp.MustCompile(`(?i)^(?:ALL|FULL|FAST|` + atom + `|\(` + atom + `(?: ` + atom + `)*\))$`)
	f.Fuzz(func(t *testing.T, input string) {
		tokens, ok := ParseFetchItems(input)
		ascii := true
		for i := range len(input) {
			ascii = ascii && input[i] < 0x7f
		}
		if !strings.Contains(input, "[") && ok != (ascii && flat.MatchString(input)) {
			t.Fatalf("flat grammar disagrees for %q: accepted=%t", input, ok)
		}
		if !ok {
			return
		}
		if len(tokens) == 0 {
			t.Fatal("accepted an empty item list")
		}
		again, valid := ParseFetchItems("(" + strings.Join(tokens, " ") + ")")
		if !valid || !reflect.DeepEqual(tokens, again) {
			t.Fatalf("expanded items do not round trip: input=%q tokens=%q again=%q valid=%t", input, tokens, again, valid)
		}
	})
}
