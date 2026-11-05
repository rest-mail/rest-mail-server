package mime

import (
	"math/rand"
	stdmime "mime"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
)

// mediaAsRead is a field as a mail client is told it reads: mime.ParseMediaType of what go-imap's
// rule leaves of a repeated name.
func mediaAsRead(field string) (string, map[string]string, error) {
	if first, repeated := wireform.WithoutRepeats(field); repeated {
		field = first
	}
	return stdmime.ParseMediaType(field)
}

// A field reads as mime.ParseMediaType reads it once go-imap has taken the repeats out, whether it's
// long enough to read one parameter at a time or not, and whatever order, spelling and repeats its
// RFC 2231 pieces come in.
func TestFieldsReadWithTheFirstOfARepeatedName(t *testing.T) {
	rng := rand.New(rand.NewSource(1915))
	families := []string{"name", "filename", "charset", "boundary", "NAME", "FileName"}
	suffixes := []string{"", "*", "*0", "*0*", "*1", "*1*", "*2", "*3*", "*01", "*x", "**", "*7", "*12", "*11*", "*1234567890", "*0000"}
	values := []string{"v", "a%41", `"q r"`, `"q\"r"`, "us-ascii''a%20b", "utf-8''%C3%B6", "iso-8859-1''%F6", "x'y", "'", "%zz", `"a\b"`, "b1", "UTF-8'en'%41"}
	types := []string{"text/plain", "application/x", "BAD/Type", "bad", "attachment"}
	others := []string{"p=v", "P=v", "q*0=v", "q*=x", "r=w"}
	repeats := 0
	for round := range 20000 {
		var b strings.Builder
		b.WriteString(types[rng.Intn(len(types))])
		ordered := rng.Intn(2) == 0
		count := manyParameters + 2 + rng.Intn(12)
		if round%2 == 0 {
			count = 1 + rng.Intn(8)
		}
		for i := range count {
			switch rng.Intn(24) {
			case 0:
				b.WriteString(";") // a blank segment reads only where nothing but repeats follow
				continue
			case 1:
				b.WriteString("; p=\"unclosed")
				continue
			case 2, 3, 4, 5:
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
		wantType, wantAll, err := mediaAsRead(field)
		gotType, gotParams, failed := parseMedia(field)
		if failed != (err != nil) {
			t.Fatalf("round %d: %q: failed %v, the library says %v", round, field, failed, err)
		}
		if !failed {
			if _, _, refused := stdmime.ParseMediaType(field); refused != nil {
				repeats++
			}
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
	if repeats < 1000 {
		t.Fatalf("only %d fields read through a repeat, so this proves little", repeats)
	}
}

// The first value of a repeated name stands, in each form of a name, and a later one has to read.
func TestParseMediaReadsTheFirstOfARepeatedName(t *testing.T) {
	long := strings.Repeat("; p=v", manyParameters+1)
	for _, row := range []struct {
		field, name, want string
		failed            bool
	}{
		{field: "multipart/mixed; boundary=a; boundary=b", name: "boundary", want: "a"},
		{field: "multipart/mixed; BOUNDARY=a; boundary=b", name: "boundary", want: "a"},
		{field: "multipart/mixed; boundary=a; x=1; x=2", name: "boundary", want: "a"},
		{field: "multipart/mixed; x=1; boundary=a; x=2;", name: "boundary", want: "a"},
		{field: "attachment; filename=a.txt; filename=b.exe", name: "filename", want: "a.txt"},
		{field: "attachment; filename*=utf-8''a.txt; filename*=utf-8''b.exe", name: "filename", want: "a.txt"},
		{field: "attachment; filename*0=a; filename*0=x; filename*1=.txt", name: "filename", want: "a.txt"},
		{field: "attachment; filename=a.txt;; filename=b.exe", name: "filename", want: "a.txt"},
		{field: "text/plain; charset=iso-8859-1; charset=utf-8", name: "charset", want: "iso-8859-1"},
		{field: "a/b; name=a.txt; name=\"b", failed: true},
		{field: "a/b; name=a.txt; name=", failed: true},
		{field: "a/b; name=a.txt; x=1;; y=2", failed: true},
		{field: "a/b; name=a.txt; name=b.exe; bad", failed: true},
	} {
		for _, field := range []string{row.field, strings.Replace(row.field, ";", long+";", 1)} {
			_, params, failed := parseMedia(field)
			if failed != row.failed {
				t.Errorf("%q: failed %v", field, failed)
			}
			if !failed && params[row.name] != row.want {
				t.Errorf("%q: %s is %q, want %q", field, row.name, params[row.name], row.want)
			}
		}
	}
}
