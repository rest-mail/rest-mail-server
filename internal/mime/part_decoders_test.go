package mime

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime/quotedprintable"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
)

// spacesAsBreaks is what base64Reader does with a space, in front of the library's decoder.
type spacesAsBreaks struct{ source io.Reader }

func (r *spacesAsBreaks) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	for i := 0; i < n; i++ {
		if p[i] == ' ' || p[i] == '\t' {
			p[i] = '\n'
		}
	}
	return n, err
}

var errBrokenSource = errors.New("broken source")

// readsOf is every answer a reader gives, read sizes chosen by random, a few past its end included.
func readsOf(reader io.Reader, seed uint64) string {
	sizes := rand.New(rand.NewPCG(seed, 7))
	var out strings.Builder
	buffer := make([]byte, 32<<10)
	after := 0
	for after < 3 {
		size := []int{1, 2, 3, 4, 5, 7, 11, 100, 768, 1024, 4096, 32 << 10}[sizes.IntN(12)]
		n, err := reader.Read(buffer[:size])
		fmt.Fprintf(&out, "%q %v\n", buffer[:n], err)
		if err != nil {
			after++
		}
	}
	return out.String()
}

func generated(random *rand.Rand, alphabet string) []byte {
	out := make([]byte, random.IntN(5000))
	for i := range out {
		out[i] = alphabet[random.IntN(len(alphabet))]
	}
	return out
}

// sourceOf reads input in one of the ways a part's body can arrive. It answers a new one each call.
func sourceOf(input []byte, way int) io.Reader {
	switch way % 5 {
	case 1:
		return iotest.OneByteReader(bytes.NewReader(input))
	case 2:
		return iotest.HalfReader(bytes.NewReader(input))
	case 3:
		return iotest.DataErrReader(bytes.NewReader(input))
	case 4:
		return io.MultiReader(bytes.NewReader(input), iotest.ErrReader(errBrokenSource))
	}
	return bytes.NewReader(input)
}

// These read as the library reads, one answer at a time, on input that's good, broken, or cut off,
// however it's asked for and however it arrives. Each run borrows what the one before left dirty.
func TestPartDecodersReadAsTheLibraryDoes(t *testing.T) {
	const base64Letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	random := rand.New(rand.NewPCG(1915, 999))
	for i := range 20000 {
		var input []byte
		switch i % 4 {
		case 0: // what a sender usually writes
			decoded := generated(random, base64Letters)
			encoded := base64.StdEncoding.EncodeToString(decoded)
			var b strings.Builder
			for len(encoded) > 76 {
				b.WriteString(encoded[:76] + "\r\n")
				encoded = encoded[76:]
			}
			b.WriteString(encoded)
			input = []byte(b.String())
		case 1: // mostly letters, with what breaks them
			input = generated(random, base64Letters+base64Letters+"\r\n \t=!\xff")
		case 2: // nothing but what's passed over, and then an end
			input = append(generated(random, "\r\n \t"), "AB="[:random.IntN(4)]...)
		case 3: // cut anywhere
			input = []byte(base64.StdEncoding.EncodeToString(generated(random, base64Letters)))
			input = input[:random.IntN(len(input)+1)]
		}
		way, seed := i, uint64(i)
		got := readsOf(newBase64Reader(sourceOf(input, way)), seed)
		want := readsOf(base64.NewDecoder(base64.StdEncoding, &spacesAsBreaks{sourceOf(input, way)}), seed)
		if got != want {
			t.Fatalf("base64, %q read in way %d:\ngot  %.2000s\nwant %.2000s", input, way%5, got, want)
		}
	}
	const quotedLetters = "abc =0123456789ABCDEF\r\n\t\xff\x00"
	for i := range 20000 {
		input := generated(random, quotedLetters)
		if i%3 == 0 {
			input = input[:min(len(input), 100)]
		}
		way, seed := i, uint64(i)
		got := readsOf(newQuotedReader(sourceOf(input, way)), seed)
		want := readsOf(quotedprintable.NewReader(sourceOf(input, way)), seed)
		if got != want {
			t.Fatalf("quoted-printable, %q read in way %d:\ngot  %.2000s\nwant %.2000s", input, way%5, got, want)
		}
	}
}

// Two read at once have a room each: a multipart can be encoded, and hold a part that's encoded too.
func TestPartDecodersReadAtOnceDoNotShare(t *testing.T) {
	one := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("one"), 1000))
	two := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("two"), 1000))
	first, second := newBase64Reader(strings.NewReader(one)), newBase64Reader(strings.NewReader(two))
	var gotOne, gotTwo bytes.Buffer
	buffer := make([]byte, 7)
	for range 2000 {
		n, _ := first.Read(buffer)
		gotOne.Write(buffer[:n])
		n, _ = second.Read(buffer)
		gotTwo.Write(buffer[:n])
	}
	if gotOne.String() != strings.Repeat("one", 1000) || gotTwo.String() != strings.Repeat("two", 1000) {
		t.Fatalf("read %d and %d bytes that aren't what was encoded", gotOne.Len(), gotTwo.Len())
	}
}

// What opening and reading a part costs doesn't depend on how little it holds, and it's far less than
// the 1.8 KiB and 4 KiB of the library's: that's the point of them.
func TestPartDecodersKeepLittlePerPart(t *testing.T) {
	const parts = 1000
	for name, open := range map[string]func(io.Reader) io.Reader{
		"base64":           func(r io.Reader) io.Reader { return newBase64Reader(r) },
		"quoted-printable": func(r io.Reader) io.Reader { return newQuotedReader(r) },
	} {
		t.Run(name, func(t *testing.T) {
			buffer := make([]byte, 1024)
			source := &strings.Reader{}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			for range parts {
				source.Reset("QUJD")
				reader := open(source)
				for {
					if _, err := reader.Read(buffer); err != nil {
						break
					}
				}
			}
			runtime.ReadMemStats(&after)
			if each := (after.TotalAlloc - before.TotalAlloc) / parts; each > 512 {
				t.Fatalf("%d bytes a part", each)
			}
		})
	}
}
