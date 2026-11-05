package mail

import (
	"math/rand"
	"mime"
	"runtime"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
)

func wireSizeShapes() map[string]string {
	binary := "Content-Type: image/png\r\nContent-Transfer-Encoding: binary\r\n"
	return map[string]string{
		"7-bit":                    "Subject: hi\r\n\r\nbody\r\n",
		"bare line feeds":          "Subject: hi\n\nbody\n",
		"8-bit subject":            "Subject: Grüße \"qü\" (cö) <x>\r\n\r\nbody\r\n",
		"not UTF-8, folded":        "Subject: caf\xe9\r\n\tfolded \xff\r\n\r\nb\xf6dy\r\n",
		"8-bit address":            "From: Jö <j@exämple.test>, \"A é\" <üser@example.test>\r\nTo: bad\xff <<\r\n\r\nb\r\n",
		"8-bit field name":         "Naïme: value\r\n\r\nb\r\n",
		"name folding to a CTE":    "Content-Tranſfer-Encoding: base64\r\n\r\n\xf6\r\n",
		"not a field":              "NöColon line\r\n\r\nb\r\n",
		"8-bit type":               "Content-Type: appl\xf6/pdf; a=1\r\n\r\n\xf6\r\n",
		"8-bit parameter":          "Content-Type: text/plain; name=\"caf\xe9.txt\"\r\n\r\n\xf6\r\n",
		"NUL in a header":          "Subject: nul\x00\xf6\r\n\r\nb\r\n",
		"blank line in a header":   "Subject: \xf6\n\nX-Later: y\r\n\r\n\xf6\r\n",
		"base64, stray octets":     "Content-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nAAA\xff=\x00\r\n--B\xff\r\n",
		"base64 from binary":       binary + "\r\n" + strings.Repeat("\xff", 57*200+5) + "\r\n",
		"exact base64 lines":       binary + "\r\n" + strings.Repeat("\xff", 57*53),
		"quoted-printable":         "Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n=\xf6 \x00\r\n",
		"text with no charset":     "Content-Type: text/plain\r\n\r\n" + strings.Repeat("\xf6 x\r\n\t=abc\r", 1500),
		"unknown encoding":         "Content-Type: image/png\r\nContent-Transfer-Encoding: x-weird\r\n\r\n\xf6\r\n",
		"multipart":                "Subject: \xf6\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\npre\xf6\r\n--B\r\nSubject: \xf6\r\n\r\npart\xf6\r\n--B\r\n" + binary + "\r\n\xff\xfe\r\n--B--\r\nepi\xf6\r\n",
		"8-bit boundary":           "Content-Type: multipart/mixed; boundary=\"\xf6b\"\r\n\r\n--\xf6b\r\n\r\n\xf6\r\n--\xf6b--\r\n",
		"8-bit header, boundary":   "Subject: ü\r\nContent-Type: multipart/mixed; boundary=\"\xf6b\"\r\n\r\n--\xf6b\r\n\r\n\xf6\r\n--\xf6b--\r\n",
		"text that is a delimiter": "Content-Type: multipart/mixed; boundary=\"\xf6b\"\r\n\r\n--\xf6b\r\nContent-Type: text/plain; charset=x\r\n\r\n--=F6b\r\n\xf6\r\n--\xf6b--\r\n",
		"conversion makes one":     "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nAA\r\n--B\xff\r\nAA\r\n--B--\r\n",
		"nested, outer moves":      "Content-Type: multipart/mixed; boundary=\"A\xf6\"\r\n\r\n--A\xf6\r\nContent-Type: multipart/mixed; boundary=\"=\"\r\n\r\n--=\r\n\r\n\xf6\r\n--=--\r\n--A\xf6--\r\n",
		"delimiter in a part":      "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nX-A: \xf6\r\n--Bx: y\r\n\r\n\xf6\r\n--B--\r\n",
		"encapsulated":             "Content-Type: message/rfc822\r\n\r\nSubject: inner \xf6\r\nContent-Type: image/png\r\n\r\n\xf6\r\n",
		"malformed multipart":      "Content-Type: multipart/mixed\r\n\r\n--B\r\n\xf6\r\n",
	}
}

// measuresWhatFetchSends checks a message against the conversions FETCH runs.
func measuresWhatFetchSends(t *testing.T, name, raw string) {
	t.Helper()
	downgraded := DowngradeHeaders(raw)
	want := WireSizes{
		Downgraded: len(DowngradeForPOP3(raw)),
		IMAP:       len(wireform.Convert(downgraded, false)),
		IMAPUTF8:   len(wireform.Convert(raw, true)),
	}
	if got := MeasureWireSizes(raw); got != want {
		t.Fatalf("%s: measured %+v, FETCH sends %+v of %q", name, got, want, raw)
	}
	if got := wireform.Size(raw, false); got != len(wireform.Convert(raw, false)) {
		t.Fatalf("%s: Size counts %d of %q", name, got, raw)
	}
	for _, sent := range []string{wireform.Convert(downgraded, false), wireform.Convert(raw, true), wireform.Convert(raw, false)} {
		if strings.IndexByte(sent, 0) >= 0 {
			t.Fatalf("%s: a NUL survives the conversion of %q: %q", name, raw, sent)
		}
	}
}

// A size is the conversion counted, not worked out apart from it, for every shape of message
// that reaches a different path through it.
func TestWireSizesAreWhatFetchSends(t *testing.T) {
	shapes := wireSizeShapes()
	for name, raw := range shapes {
		t.Run(name, func(t *testing.T) { measuresWhatFetchSends(t, name, raw) })
	}
	seeds := make([]string, 0, len(shapes))
	for _, raw := range shapes {
		seeds = append(seeds, raw)
	}
	// Map order would make a failure here hard to repeat.
	for i := range seeds {
		for j := i + 1; j < len(seeds); j++ {
			if seeds[j] < seeds[i] {
				seeds[i], seeds[j] = seeds[j], seeds[i]
			}
		}
	}
	r := rand.New(rand.NewSource(1883))
	const alphabet = "\r\n\xf6\x00:;=\"\\ -B\tA"
	for range 4000 {
		seed := seeds[r.Intn(len(seeds))]
		if len(seed) > 2000 {
			seed = seed[:2000]
		}
		m := []byte(seed)
		for k := r.Intn(4); k >= 0; k-- {
			m[r.Intn(len(m))] = alphabet[r.Intn(len(alphabet))]
		}
		measuresWhatFetchSends(t, "mutated", string(m))
	}
}

func TestWriteQWordsMatchesTheLibrary(t *testing.T) {
	for _, text := range []string{
		"ü", "Grüße", "a=b?c_d e\tfü", strings.Repeat("ü", 200), strings.Repeat("x", 62) + "ü", strings.Repeat("\U0001F600", 40),
		strings.Repeat("a", 200) + "ü", "ü\r\n", "\x01ü\x7f",
	} {
		var b strings.Builder
		writeQWords(&b, text)
		if want := mime.QEncoding.Encode("utf-8", text); b.String() != want {
			t.Fatalf("%q: got %q, the library says %q", text, b.String(), want)
		}
	}
}

func allocated(work func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	work()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A size at store time costs less than a copy of what it measures, whatever the message
// holds, so a large attachment isn't held to size it and no one can make it cost more by
// choosing the headers or the shape of what they send.
func TestMeasuringHoldsNoCopy(t *testing.T) {
	const size = 1 << 20
	attachment := func(encoding, data string) string {
		return "Subject: Grüße\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nsmäll\r\n--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: " + encoding + "\r\n\r\n" + data + "\r\n--B--\r\n"
	}
	for name, raw := range map[string]string{
		"ASCII":                 attachment("base64", strings.Repeat(strings.Repeat("A", 76)+"\r\n", size/78)),
		"binary attachment":     attachment("binary", strings.Repeat("\xf6", size)),
		"base64, stray octets":  attachment("base64", strings.Repeat("A\xf6", size/2)),
		"text with no charset":  "Content-Type: text/plain\r\n\r\n" + strings.Repeat("\xf6", size) + "\r\n",
		"8-bit subject":         "Subject: " + strings.Repeat("\xf6", size) + "\r\n\r\nb\r\n",
		"UTF-8 subject":         "Subject: " + strings.Repeat("ü", size/2) + "\r\n\r\nb\r\n",
		"encapsulated in depth": strings.Repeat("Content-Type: message/rfc822\r\n\r\n", 60) + "Subject: \xf6\r\n\r\n" + strings.Repeat("\xf6", size),
	} {
		t.Run(name, func(t *testing.T) {
			if got := allocated(func() { MeasureWireSizes(raw) }); got > uint64(len(raw)) {
				t.Errorf("measuring %d bytes allocated %d", len(raw), got)
			}
		})
	}
	// POP3 sends the body as stored but for the delimiter lines of an 8-bit boundary, so
	// those are respelled to count them, once: that one copy is all this message costs.
	raw := "Subject: \u00fc\r\nContent-Type: multipart/mixed; boundary=\"\xf6b\"\r\n\r\n--\xf6b\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: binary\r\n\r\n" + strings.Repeat("\xf6", size) + "\r\n--\xf6b--\r\n"
	if got := allocated(func() { MeasureWireSizes(raw) }); got > 3*uint64(len(raw))/2 {
		t.Errorf("measuring %d bytes with an 8-bit boundary allocated %d", len(raw), got)
	}
}

// A NUL doesn't reach a client through any conversion, spelled in an encoded word
// or not, and the sizes still measure what is sent.
func TestDowngradeSpellsNoNUL(t *testing.T) {
	for _, raw := range []string{
		"Subject: nul\x00\xf6\r\n\r\nb\r\n",
		"X-Other: \x00\xc3\xa9\r\n\r\nb\r\n",
		"To: J\xc3\xb6\x00rg <j@example.test>\r\n\r\nb\r\n",
		"Content-Type: text/plain; name=\"\x00\xc3\xa9\"\r\n\r\nb\r\n",
		"N\xc3\xb6Colon\x00 line\r\n\r\nb\r\n",
	} {
		down := DowngradeHeaders(raw)
		if strings.Contains(down, "=00") || strings.Contains(down, "%00") || strings.IndexByte(down, 0) >= 0 {
			t.Errorf("%q downgrades to %q", raw, down)
		}
		measuresWhatFetchSends(t, "NUL", raw)
	}
}
