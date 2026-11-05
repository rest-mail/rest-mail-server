package message

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// A field of many RFC 2231 pieces reads as parseUncommented reads it, whatever order, spelling and
// repeats they come in, because a client is sent what this reads.
func TestParseKeptMatchesParseUncommented(t *testing.T) {
	rng := rand.New(rand.NewSource(1915))
	families := []string{"charset", "boundary", "CHARSET", "Boundary"}
	suffixes := []string{"", "*", "*0", "*0*", "*1", "*1*", "*2", "*3*", "*01", "*x", "**", "*7", "*12", "*11*", "*1234567890", "*0000"}
	values := []string{"v", "a%41", `"q r"`, `"q\"r"`, "us-ascii''a%20b", "utf-8''%C3%B6", "iso-8859-1''%F6", "x'y", "'", "%zz", `"a\b"`, "b1"}
	types := []string{"text/plain", "multipart/mixed", "TEXT / plain", "bad", "application/x"}
	others := []string{"p=v", "P=w", "name*0=v", "q*=x", "r=\"w\"", "r=w"}
	for round := range 20000 {
		var b strings.Builder
		b.WriteString(types[rng.Intn(len(types))])
		shuffled := rng.Intn(2) == 0
		count := manyParameters + 2 + rng.Intn(12)
		for i := range count {
			family := families[rng.Intn(len(families))]
			suffix := suffixes[rng.Intn(len(suffixes))]
			switch {
			case rng.Intn(8) == 0:
				// A plain name, which a later form may stand beside.
			case !shuffled:
				suffix = "*" + strconv.Itoa(i)
			}
			if rng.Intn(6) == 0 {
				b.WriteString("; " + others[rng.Intn(len(others))])
				continue
			}
			b.WriteString("; " + family + suffix + "=" + values[rng.Intn(len(values))])
		}
		field := b.String()
		wantType, wantParams, wantErr := parseUncommented(field)
		gotType, gotParams, gotErr := parseKept(field)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("round %d: %q: errors %v and %v", round, field, wantErr, gotErr)
		}
		if wantErr != nil {
			continue
		}
		if wantType != gotType || wantParams["boundary"] != gotParams["boundary"] || wantParams["charset"] != gotParams["charset"] {
			t.Fatalf("round %d: %q:\n%q %q\n%q %q", round, field, wantType, wantParams, gotType, gotParams)
		}
	}
}
