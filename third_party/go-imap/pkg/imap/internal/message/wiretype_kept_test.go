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

// What a repeated name comes to in a field of many parameters, in words, so the test above and the
// whole field can't both be wrong about it. The first of a name stands, and a later one has to read
// (RFC 2045 §5.1 has no rule against a repeat, so §5.2's default is only for what doesn't read).
func TestParseKeptReadsTheFirstOfARepeatedName(t *testing.T) {
	var p strings.Builder
	for i := range 30 {
		p.WriteString("; p" + strconv.Itoa(i) + "=v" + strconv.Itoa(i))
	}
	many := "multipart/mixed" + p.String()
	for _, tc := range []struct {
		name, field, boundary string
		refused               bool
	}{
		{name: "a left-out name", field: many + "; p1=other; boundary=B", boundary: "B"},
		{name: "a plain kept name", field: many + "; boundary=A; BOUNDARY=B", boundary: "A"},
		{name: "a segment", field: many + "; boundary*0=A; boundary*0=C; boundary*1=B", boundary: "AB"},
		{name: "a segment in another case", field: many + "; boundary*1=B; BOUNDARY*1=C; boundary*0=A", boundary: "AB"},
		{name: "before a blank that ends up last", field: many + "; boundary=B; p3=v3;; P4=\"v4\"", boundary: "B"},
		{name: "a later value that doesn't read", field: many + "; boundary=B; p1=", refused: true},
		{name: "a later value that isn't all one value", field: many + "; boundary=B; p1=a b", refused: true},
		{name: "a blank with a parameter after it", field: many + "; p1=other;; boundary=B", refused: true},
		{name: "two blanks", field: many + "; boundary=B; p1=other;;", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mt, params, err := parseKept(tc.field)
			if tc.refused {
				if err == nil {
					t.Fatalf("read %q %q, want it refused", mt, params)
				}
			} else if err != nil || mt != "multipart/mixed" || params["boundary"] != tc.boundary {
				t.Fatalf("read %q %q, %v, want boundary %q", mt, params, err, tc.boundary)
			}
			_, whole, wholeErr := parseUncommented(tc.field)
			if (wholeErr == nil) == tc.refused || whole["boundary"] != tc.boundary {
				t.Fatalf("the whole field reads %q, %v", whole, wholeErr)
			}
		})
	}
}

// Few names over many parameters, so most are repeats, and a blank is often followed by nothing else.
func TestParseKeptMatchesParseUncommented_Repeats(t *testing.T) {
	rng := rand.New(rand.NewSource(1934))
	names := []string{"p", "P", "q", "charset", "CHARSET", "boundary", "boundary*0", "BOUNDARY*1", "charset*0*", "charset*x", "*0"}
	values := []string{"v", "w", `"v"`, `"w x"`, `""`, "%41", ""}
	types := []string{"text/plain", "multipart/mixed"}
	read, blanks := 0, 0
	for round := range 40000 {
		var b strings.Builder
		b.WriteString(types[rng.Intn(len(types))])
		blank := false
		for range manyParameters + 2 + rng.Intn(8) {
			switch rng.Intn(40) {
			case 0:
				b.WriteString("; ")
				blank = true
				continue
			case 1:
				b.WriteString("; broken")
				continue
			}
			value := values[rng.Intn(len(values))]
			if value == "" && rng.Intn(8) != 0 {
				value = "v"
			}
			b.WriteString("; " + names[rng.Intn(len(names))] + "=" + value)
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
		read++
		if blank {
			blanks++
		}
		if wantType != gotType || wantParams["boundary"] != gotParams["boundary"] || wantParams["charset"] != gotParams["charset"] {
			t.Fatalf("round %d: %q:\n%q %q\n%q %q", round, field, wantType, wantParams, gotType, gotParams)
		}
	}
	if read < 1000 || blanks < 100 {
		t.Fatalf("%d fields read, %d of them with a blank, which proves too little", read, blanks)
	}
}
