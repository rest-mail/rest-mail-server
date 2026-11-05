package mime

import (
	"math/rand"
	stdmime "mime"
	"strconv"
	"strings"
	"testing"
)

// A field of many RFC 2231 pieces reads as mime.ParseMediaType reads it, whatever order, spelling and
// repeats they come in, because that's what every part of a message was read with before.
func TestManyContinuationsReadAsParseMediaTypeDoes(t *testing.T) {
	rng := rand.New(rand.NewSource(1915))
	families := []string{"name", "filename", "charset", "boundary", "NAME", "FileName"}
	suffixes := []string{"", "*", "*0", "*0*", "*1", "*1*", "*2", "*3*", "*01", "*x", "**", "*7", "*12", "*11*", "*1234567890", "*0000"}
	values := []string{"v", "a%41", `"q r"`, `"q\"r"`, "us-ascii''a%20b", "utf-8''%C3%B6", "iso-8859-1''%F6", "x'y", "'", "%zz", `"a\b"`, "b1", "UTF-8'en'%41"}
	types := []string{"text/plain", "application/x", "BAD/Type", "bad", "attachment"}
	others := []string{"p=v", "P=v", "q*0=v", "q*=x", "r=w"}
	for round := range 20000 {
		var b strings.Builder
		b.WriteString(types[rng.Intn(len(types))])
		ordered := rng.Intn(2) == 0
		for i := range manyParameters + 2 + rng.Intn(12) {
			if rng.Intn(6) == 0 {
				b.WriteString("; " + others[rng.Intn(len(others))])
				continue
			}
			suffix := suffixes[rng.Intn(len(suffixes))]
			if ordered && rng.Intn(8) != 0 {
				suffix = "*" + strconv.Itoa(i)
			}
			b.WriteString("; " + families[rng.Intn(len(families))] + suffix + "=" + values[rng.Intn(len(values))])
		}
		field := b.String()
		if strings.Count(field, ";") <= manyParameters {
			continue
		}
		wantType, wantAll, err := stdmime.ParseMediaType(field)
		gotType, gotParams, failed := parseMedia(field)
		if failed != (err != nil) {
			t.Fatalf("round %d: %q: failed %v, the library says %v", round, field, failed, err)
		}
		if failed {
			if gotType != field {
				t.Fatalf("round %d: %q: answered %q", round, field, gotType)
			}
			continue
		}
		want := map[string]string{}
		for name, value := range wantAll {
			if keptMediaName(name) {
				want[name] = decodeWords(value)
			}
		}
		if wantType != gotType || len(want) != len(gotParams) {
			t.Fatalf("round %d: %q:\n%q %q\n%q %q", round, field, wantType, want, gotType, gotParams)
		}
		for name, value := range want {
			if gotParams[name] != value {
				t.Fatalf("round %d: %q: %s is %q, the library says %q", round, field, name, gotParams[name], value)
			}
		}
	}
}
