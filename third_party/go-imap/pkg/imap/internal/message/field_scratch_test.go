package message

import (
	"fmt"
	"maps"
	"math/rand"
	"strings"
	"testing"
)

// foldedFields are 8-bit fields with line breaks everywhere one can stand: between pieces, inside a
// name, a value, a quoted-string and a comment, on the type, and at either end.
func foldedFields(rng *rand.Rand, count int) []string {
	pieces := []string{
		"charset*%d=v", "CHARSET*%d=v", "boundary*%d=b", "name*%d=n", "p%d=v", "P%d=é", "x%d=\"a;b\"", "x%d=\"aé\"",
		"y%d=v (c)", "y%d=(cé) v", "charset=c%d", "boundary=b%d", "boundary*%d*=utf-8''a%%20b", "z%d=\"un", "a%d=b)", "w%d",
	}
	breaks := []string{"\r\n ", "\r\n\t", "\n ", " \r\n  "}
	fields := make([]string, 0, count)
	for range count {
		var b strings.Builder
		b.WriteString([]string{"text/plain", "multipart/mixed", "text/é", "text\r\n /plain", "\r\n text/plain"}[rng.Intn(5)])
		b.WriteString("; x=é")
		many := manyParameters + 1 + rng.Intn(20)
		inside := rng.Intn(2) == 0
		for i := range many {
			b.WriteString(";")
			if rng.Intn(3) > 0 {
				b.WriteString(breaks[rng.Intn(len(breaks))])
			}
			piece := fmt.Sprintf(pieces[rng.Intn(len(pieces))], i)
			if inside && rng.Intn(12) == 0 {
				at := 1 + rng.Intn(len(piece)-1)
				piece = piece[:at] + breaks[rng.Intn(len(breaks))] + piece[at:]
			}
			b.WriteString(piece)
		}
		fields = append(fields, b.String())
	}
	return fields
}

// A field read as it's spelled reads as the same field unfolded. That's the whole of why it needn't be
// built again without its line breaks.
func TestFoldedFieldReadsAsItsUnfoldedField(t *testing.T) {
	rng := rand.New(rand.NewSource(1915))
	spelledRead, spelledWritten, rebuilt := 0, 0, 0
	for _, field := range foldedFields(rng, 20000) {
		header := "Content-Type: " + field + "\r\n\r\n"
		first, lines, _ := cutHeaderLine(field + "\r\n")
		want := strings.TrimLeft(unfoldedField(trimHeaderSpace(first), lines), " \t")
		gotType, gotParams, gotErr := readWireField(strings.TrimRight(field, " \t\r\n"))
		wantType, wantParams, wantErr := parseKeptParameters(want)
		if gotType != wantType || !maps.Equal(gotParams, wantParams) || (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%q read as %q %q %v, unfolded %q %q %v", field, gotType, gotParams, gotErr, wantType, wantParams, wantErr)
		}
		if _, _, _, read := readKeptSpelled(strings.TrimRight(field, " \t\r\n"), conversionFamilies, true); read {
			spelledRead++
		}
		if contentType, _ := readContentFields(header, true); contentType != strings.TrimLeft(strings.TrimRight(field, " \t\r\n"), " \t") {
			t.Fatalf("%q kept as %q", field, contentType)
		}

		var folded, unfolded strings.Builder
		ok, read := WriteFoldedParameterField(&folded, "Content-Type", field)
		if !read {
			if folded.Len() != 0 {
				t.Fatalf("%q wrote %q and said it didn't read", field, folded.String())
			}
			rebuilt++
			continue
		}
		spelledWritten++
		if wantOK := WriteParameterField(&unfolded, "Content-Type", unfoldHeader(field)); ok != wantOK || folded.String() != unfolded.String() {
			t.Fatalf("%q wrote %v %q, unfolded %v %q", field, ok, folded.String(), wantOK, unfolded.String())
		}
	}
	// A test that took one way only would prove nothing about the other.
	if spelledRead < 1000 || spelledWritten < 1000 || rebuilt < 1000 {
		t.Fatalf("read %d as spelled, wrote %d as spelled, rebuilt %d", spelledRead, spelledWritten, rebuilt)
	}
}

// Nothing a reading answers stays a view of memory the next reading writes over.
func TestWireTypeHoldsNothingOfItsScratch(t *testing.T) {
	for _, value := range []string{
		"multipart/mixed; x=é; boundary*=utf-8''keepme" + strings.Repeat("; p=v", manyParameters+1),
		"multipart/mixed; x=é;\r\n boundary*=utf-8''keepme" + strings.Repeat(";\r\n p=v", manyParameters+1),
		"multipart/mixed; x=é;\r\n boundary=\"keep\r\n me\"" + strings.Repeat(";\r\n p=v", manyParameters+1),
		"multipart/\r\n mixed;\r\n boundary=keepme",
	} {
		mt, params, err := readWireField(value)
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		wantType, want := strings.Clone(mt), maps.Clone(params)
		for range 8 {
			overwrite := TakeFieldScratch(4 * len(value))
			_, _ = overwrite.WriteString(strings.Repeat("#", 4*len(value)))
			defer overwrite.Release()
		}
		for range 8 {
			_, _, _ = readWireField(strings.ReplaceAll(value, "keep", "gone"))
		}
		if mt != wantType || !maps.Equal(params, want) {
			t.Fatalf("%q: read %q %q, now %q %q", value, wantType, want, mt, params)
		}
	}
}

func TestFieldScratchUnfoldsAsTheReplacersDo(t *testing.T) {
	rng := rand.New(rand.NewSource(1915))
	parts := []string{"\r\n", "\n", "\r", " ", "a", "\r\r\n", "\n\n"}
	for range 20000 {
		var b strings.Builder
		for range rng.Intn(9) {
			b.WriteString(parts[rng.Intn(len(parts))])
		}
		value := b.String()
		scratch := TakeFieldScratch(0)
		if got, want := scratch.Unfolded(value), strings.NewReplacer("\r\n", "", "\n", "").Replace(value); got != want {
			t.Fatalf("Unfolded(%q) is %q, not %q", value, got, want)
		}
		if got, want := scratch.withoutCRLF(value), unfoldHeader(value); got != want {
			t.Fatalf("withoutCRLF(%q) is %q, not %q", value, got, want)
		}
		scratch.Release()
	}
}

// A name that's a view of its field compares with one that isn't, without regard to case, however the
// table has grown.
func TestNameSetComparesViewsAsItComparesCopies(t *testing.T) {
	source := "Alpha;BETA;gamma;delta"
	set := takeNameSet(0)
	defer set.release()
	set.source = source
	for _, name := range []string{source[:5], source[6:10], source[11:16], source[17:]} {
		if set.add(name) {
			t.Fatalf("%q was there already", name)
		}
	}
	for i := range 2000 {
		if name := fmt.Sprintf("Name%d", i); set.add(name) {
			t.Fatalf("%q was there already", name)
		}
	}
	for _, name := range []string{"ALPHA", "beta", "GaMmA", "Delta", "name7", source[:5], "NAME1999"} {
		if !set.has(name) {
			t.Fatalf("%q isn't there", name)
		}
		if !set.add(name) {
			t.Fatalf("%q was added again", name)
		}
	}
	for _, name := range []string{"alph", "alphaa", "name2000"} {
		if set.has(name) {
			t.Fatalf("%q is there", name)
		}
	}
}
