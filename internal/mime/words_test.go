package mime

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	stdmime "mime"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	gomessage "github.com/emersion/go-message"
	"golang.org/x/text/encoding"
)

// The reader that costs nothing per word reads every charset as go-message's does, including each
// length around the size of its buffer, and a decoder used again keeps nothing from the word before.
func TestWordCharsetReaderReadsAsGoMessageDoes(t *testing.T) {
	names := []string{"", " ", "x-none", "X-NONE", "utf-7", "UTF-7", "utf-8", "UTF-8", "utf8", "us-ascii", "iso-8859-1", "ISO-8859-1", "latin1", "windows-1252", "Windows-1252",
		"cp1252", "utf-16be", "utf-16le", "utf-16", "utf-32", "gb18030", "gbk", "big5", "shift_jis", "euc-jp", "iso-2022-jp", "hz-gb-2312", "iso-2022-kr", "euc-kr", "koi8-r", "koi8-u", "macintosh",
		"ansi_x3.110-1983", "ANSI_X3.110-1983", "x-utf_8j", "X-UTF_8J", "csISOLatin1", "isolatin1", "ibm037", "replacement", " utf-8", "utf-8 ", "utf-8*en", "caf\xc3\xa9", "\xff"}
	for _, prefix := range []string{"ibm", "cp", "iso-8859-", "windows-"} {
		for i := range 1400 {
			names = append(names, fmt.Sprintf("%s%d", prefix, i))
		}
	}
	strict := &stdmime.WordDecoder{CharsetReader: gomessage.CharsetReader}
	random := rand.New(rand.NewPCG(1915, 1))
	source := make([]byte, 4200)
	for i := range source {
		source[i] = byte(random.Uint32())
	}
	lengths := []int{0, 1, 2, 3, 5, 4095, 4096, 4097, 4200}
	read := 0
	for _, name := range names {
		lengths := lengths
		if _, err := gomessage.CharsetReader(name, strings.NewReader("")); err == nil {
			read++
		} else {
			// A refusal is about the name, whatever the word holds.
			lengths = lengths[:1]
		}
		for _, n := range lengths {
			content := source[:n]
			word := "=?" + name + "?b?" + base64.StdEncoding.EncodeToString(content) + "?="
			want, wantErr := strict.Decode(word)
			got, gotErr := wordDecoder.Decode(word)
			if (gotErr != nil) != (wantErr != nil) || got != want {
				t.Errorf("Decode(%q, %d octets) = %q, %v, go-message reads %q, %v", name, n, got, gotErr, want, wantErr)
			}
			header := "a " + word + " " + word + " b"
			want, wantErr = strict.DecodeHeader(header)
			got, gotErr = wordDecoder.DecodeHeader(header)
			if (gotErr != nil) != (wantErr != nil) || got != want {
				t.Errorf("DecodeHeader(%q, %d octets) = %q, %v, go-message reads %q, %v", name, n, got, gotErr, want, wantErr)
			}
			reader, err := wordCharsetReader(name, bytes.NewReader(content))
			original, originalErr := gomessage.CharsetReader(name, bytes.NewReader(content))
			if (err != nil) != (originalErr != nil) {
				t.Fatalf("reader(%q) = %v, go-message %v", name, err, originalErr)
			}
			if err != nil {
				continue
			}
			gotRead, gotErr := io.ReadAll(reader)
			wantRead, wantErr := io.ReadAll(original)
			if (gotErr != nil) != (wantErr != nil) || !bytes.Equal(gotRead, wantRead) {
				t.Errorf("Read(%q, %d octets) = %q, %v, go-message reads %q, %v", name, n, gotRead, gotErr, wantRead, wantErr)
			}
		}
	}
	if read < 50 {
		t.Fatalf("only %d of %d names read: this compares nothing", read, len(names))
	}
}

// A word that leaves a decoder part way through something doesn't change how the next one reads.
func TestWordDecoderKeepsNothingFromTheWordBefore(t *testing.T) {
	for charset, words := range map[string][]string{
		"iso-2022-jp": {"\x1b$B$\"", "ab"},
		"utf-16":      {"\xff\xfea\x00", "\x00a"},
		"hz-gb-2312":  {"~{VP", "ab"},
		"iso-2022-kr": {"a", "b"},
		"utf-7":       {"+AGE", "a"},
	} {
		t.Run(charset, func(t *testing.T) {
			for range 2 {
				for _, content := range words {
					word := "=?" + charset + "?b?" + base64.StdEncoding.EncodeToString([]byte(content)) + "?="
					want, wantErr := (&stdmime.WordDecoder{CharsetReader: gomessage.CharsetReader}).Decode(word)
					got, gotErr := wordDecoder.Decode(word)
					if (gotErr != nil) != (wantErr != nil) || got != want {
						t.Errorf("%q = %q, %v, go-message reads %q, %v", content, got, gotErr, want, wantErr)
					}
				}
			}
		})
	}
}

// A word costs its own few octets, whatever its charset is called and however many there are, where it
// cost 40 KiB (#1915).
func TestEncodedWordsCostNoBuffers(t *testing.T) {
	const words = 1000
	for name, word := range map[string]func(i int) string{
		"one charset":    func(i int) string { return fmt.Sprintf("=?windows-1252?q?N%d?=", i%10) },
		"base64":         func(int) string { return "=?windows-1252?b?TjA=?=" },
		"upper case":     func(int) string { return "=?WINDOWS-1252?Q?N?=" },
		"a stateful one": func(int) string { return "=?iso-2022-jp?b?GyRCJCIbKEI=?=" },
		"more than it has": func(i int) string {
			return fmt.Sprintf("=?windows-%d?q?N?=", 1250+i%9) + fmt.Sprintf(" =?iso-8859-%d?q?N?=", 2+i%9)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			for i := range words {
				if _, err := wordDecoder.DecodeHeader(word(i)); err != nil {
					t.Fatal(err)
				}
			}
			runtime.ReadMemStats(&after)
			if each := (after.TotalAlloc - before.TotalAlloc) / words; each > 1024 {
				t.Fatalf("each word allocated %d bytes", each)
			}
		})
	}
}

// What a reader keeps to use again has an end, whatever a field names or how long a word is.
func TestWordReaderKeepsLittle(t *testing.T) {
	r := &wordReader{decoders: make(map[string]*encoding.Decoder)}
	for i := range 40 {
		name := fmt.Sprintf("windows-%d", 1250+i%9)
		if i%2 == 1 {
			name = fmt.Sprintf(" iso-8859-%d ", 2+i%9)
		}
		if r.decoderFor(name) == nil {
			t.Fatalf("%q isn't read", name)
		}
		if len(r.decoders) > maxWordDecoders {
			t.Fatalf("%d decoders kept", len(r.decoders))
		}
	}
	long := bytes.Repeat([]byte("a"), 4*maxKeptWord)
	reader, err := wordCharsetReader("windows-1252", bytes.NewReader(long))
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := reader.(*wordReader)
	var out bytes.Buffer
	if _, err := io.Copy(&out, kept); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), long) {
		t.Fatalf("read %d octets of %d", out.Len(), len(long))
	}
	if kept.src.Cap() > maxKeptWord {
		t.Fatalf("kept room for %d octets", kept.src.Cap())
	}
}

// decodeWords is its own copy of mime.WordDecoder.DecodeHeader, so what a field reads as depends on the
// two reading alike, in every way a word can be spelled or fail to be one.
func TestDecodeWordsReadsAsWordDecoderDoes(t *testing.T) {
	agree := func(t *testing.T, value string) {
		t.Helper()
		want, err := wordDecoder.DecodeHeader(value)
		if err != nil {
			want = value
		}
		if got := decodeWords(value); got != want {
			t.Errorf("decodeWords(%q) = %q, mime.WordDecoder reads %q", value, got, want)
		}
	}
	for _, value := range []string{
		"", "plain", "=?", "=??", "=???", "=?x?q??=", "=?utf-8?q?a?=", "=?UTF-8?Q?a_b?=", "=?utf-8?b?YWJj?=", "=?utf-8?B?YWJj?=",
		"a =?utf-8?q?b?= c", "=?utf-8?q?a?= =?utf-8?q?b?=", "=?utf-8?q?a?=\r\n =?utf-8?q?b?=", "=?utf-8?q?a?=\t\n =?utf-8?q?b?=", "=?utf-8?q?a?=\v=?utf-8?q?b?=",
		"=?utf-8?q?a?= x =?utf-8?q?b?=", "=?utf-8?q?a?=x=?utf-8?q?b?=", "=?utf-8?q?=?= =?utf-8?q?b?=", "=?utf-8?q?=4?= b", "=?utf-8?q?=zz?= =?utf-8?q?a?= =?utf-8?q?b?=",
		"=?utf-8?b?****?= =?utf-8?q?a?=", "=?utf-8?x?a?= =?utf-8?q?b?=", "=?utf-8?q?\xff?=", "=?utf-8?q?\x00?=", "=?utf-8?q?=00?=", "=?iso-8859-1?q?=E9?=", "=?ISO-8859-1?Q?=E9?=", "=?us-ascii?q?=E9a?=", "=?US-ASCII?q?a?=",
		"=?windows-1252?q?=E9?=", "=?WINDOWS-1252?Q?=E9?=", "=?windows-1252?b?6Q==?=", "=?koi8-r?q?=C1?=", "=?shift_jis?b?gqA=?=", "=?iso-2022-jp?b?GyRCJCIbKEI=?=", "=?utf-16le?b?QQA=?=", "=?utf-16be?b?AEE=?=", "=?gb18030?b?gTCBMA==?=",
		"=?x-none?q?a?=", "=?x-none?q?a?= =?utf-8?q?b?=", "=?utf-8?q?a?= =?x-none?q?b?=", "=??q?a?=", "=?utf-8*en?q?a?=", "=?utf-8?q?a", "=?utf-8?q", "=?utf-8?", "=?utf-8", "a=?", "=?utf-8?qq?a?=", "=?utf-8?q?a?b?=", "=?utf-8?q??=", "=?utf-8?b??=", "=?=?utf-8?q?a?=", "=?a?=", "=?a?q?=",
		"caf\xc3\xa9 =?utf-8?q?a?=", "\xff =?utf-8?q?a?=", "=?utf-8?q?a?= \xff", "=?windows-1252?q?a?=" + strings.Repeat(" =?windows-1252?q?b?=", 40), "=?windows-1252?b?" + strings.Repeat("6Q", 5000) + "?=",
	} {
		agree(t, value)
	}
	random := rand.New(rand.NewPCG(1915, 2))
	pieces := []string{"=?", "?=", "?", "=", "utf-8", "UTF-8", "iso-8859-1", "us-ascii", "windows-1252", "WINDOWS-1252", "koi8-r", "x-none", "iso-2022-jp", "utf-16le", "?q?", "?Q?", "?b?", "?B?", "?x?", "a", "_", "=E9", "=e9", "=00", "=4", "=zz", "YWJj", "6Q==", "6Q", "*", " ", "\t", "\r\n ", "\n", "\v", "\xff", "\x00", "\xc3\xa9",
		"=?utf-8?q?a?=", "=?windows-1252?q?=E9?=", "=?iso-8859-1?b?6Q==?=", "=?x-none?q?a?="}
	for range 60000 {
		var value strings.Builder
		for range random.IntN(12) {
			value.WriteString(pieces[random.IntN(len(pieces))])
		}
		agree(t, value.String())
	}
}

// Words cost what they write and a buffer, and a word's room isn't left for the words after it.
func TestDecodeWordsTakesNoBufferPerWord(t *testing.T) {
	const words = 2000
	value := "=?windows-1252?q?a?=" + strings.Repeat("\r\n =?windows-1252?q?a?=", words)
	decodeWords(value)
	allocated := testing.AllocsPerRun(5, func() { decodeWords(value) })
	if allocated > 20 {
		t.Fatalf("%.0f allocations for %d words", allocated, words)
	}
	decodeWords("=?windows-1252?b?" + strings.Repeat("6Q", 2*maxKeptWord) + "?=")
	for range 8 {
		words, _ := headerWordBuffers.Get().(*headerWords)
		defer headerWordBuffers.Put(words)
		if words.text.Held() > maxKeptWord {
			t.Fatalf("kept %d octets of room for the next word", words.text.Held())
		}
	}
}

// failingReader gives its octets, then fails.
type failingReader struct {
	octets []byte
	err    error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.octets) == 0 {
		return 0, r.err
	}
	n := copy(p, r.octets)
	r.octets = r.octets[n:]
	return n, nil
}

// A part's text reads as go-message's reader reads it, by however little it's read at a time, wherever
// its source breaks off, and with what the part before it left behind in the same reader.
func TestPartCharsetReaderReadsAsGoMessageDoes(t *testing.T) {
	random := rand.New(rand.NewPCG(1915, 3))
	source := make([]byte, 9000)
	for i := range source {
		source[i] = byte(random.Uint32())
	}
	ascii := bytes.Repeat([]byte("hello world\r\n"), 700)
	shifted := []byte("\x1b$B$\"\x1b(B a \x1b$B$\"")
	bom := append([]byte{0xff, 0xfe}, []byte("a\x00b\x00")...)
	sources := [][]byte{source[:0], source[:1], source[:3], source[:4095], source[:4096], source[:4097], source[:8192], source, ascii, shifted, bom}
	names := []string{"windows-1252", "iso-8859-1", "koi8-r", "shift_jis", "euc-jp", "big5", "gbk", "gb18030", "euc-kr", "iso-2022-jp", "hz-gb-2312", "utf-16le", "utf-16be", "utf-16", "macintosh", "ansi_x3.110-1983", "x-utf_8j", "replacement"}
	readers := map[string]func(io.Reader) io.Reader{
		"whole":    func(r io.Reader) io.Reader { return r },
		"one":      iotest.OneByteReader,
		"half":     iotest.HalfReader,
		"data err": iotest.DataErrReader,
	}
	failure := errors.New("the source broke off")
	var shared partCharsets
	readAll := func(reader io.Reader, chunk int) ([]byte, error) {
		var out bytes.Buffer
		buffer := make([]byte, chunk)
		for {
			n, err := reader.Read(buffer)
			out.Write(buffer[:n])
			if err != nil {
				return out.Bytes(), err
			}
		}
	}
	for _, name := range names {
		for _, octets := range sources {
			for kind, wrap := range readers {
				for _, chunk := range []int{1, 7, 4096, 16384} {
					if chunk == 1 && len(octets) > 4097 {
						continue
					}
					original, err := gomessage.CharsetReader(name, wrap(bytes.NewReader(octets)))
					if err != nil {
						continue
					}
					want, wantErr := readAll(original, chunk)
					// What's left of the part before stands in the reader this one takes.
					if left := shared.open(name, bytes.NewReader(octets)); left != nil && len(octets) > 0 {
						_, _ = left.Read(make([]byte, 1))
					}
					reader := shared.open(name, wrap(bytes.NewReader(octets)))
					if reader == nil {
						t.Fatalf("%s isn't found", name)
					}
					got, gotErr := readAll(reader, chunk)
					if !errors.Is(gotErr, wantErr) && gotErr.Error() != wantErr.Error() || !bytes.Equal(got, want) {
						t.Fatalf("%s, %d octets, %s, %d at a time: %q %v, go-message reads %q %v", name, len(octets), kind, chunk, got, gotErr, want, wantErr)
					}
				}
			}
			original, err := gomessage.CharsetReader(name, &failingReader{octets: octets, err: failure})
			if err != nil {
				continue
			}
			want, wantErr := readAll(original, 512)
			got, gotErr := readAll(shared.open(name, &failingReader{octets: octets, err: failure}), 512)
			if !errors.Is(gotErr, failure) || !errors.Is(wantErr, failure) || !bytes.Equal(got, want) {
				t.Fatalf("%s, %d octets, failing: %q %v, go-message reads %q %v", name, len(octets), got, gotErr, want, wantErr)
			}
		}
	}
	for _, name := range []string{"x-none", "utf-7", "caf\xc3\xa9", ""} {
		if reader := shared.open(name, strings.NewReader("a")); reader != nil {
			if _, err := gomessage.CharsetReader(name, strings.NewReader("a")); err != nil {
				t.Errorf("%q is read here, and go-message refuses it: %v", name, err)
			}
		}
	}
}

// A message's parts share a reader and a decoder for each charset, however many parts it has, and the
// decoders it keeps have an end.
func TestPartCharsetsKeepOneReaderPerMessage(t *testing.T) {
	var parts strings.Builder
	parts.WriteString("From: a@example.test\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n")
	for range 200 {
		parts.WriteString("--B\r\nContent-Type: text/plain; charset=windows-1252\r\n\r\nx\r\n")
	}
	parts.WriteString("--B--\r\n")
	raw := []byte(parts.String())
	perPart := func() float64 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if _, err := Parse(raw); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		return float64(after.TotalAlloc-before.TotalAlloc) / 200
	}
	best := perPart()
	for range 3 {
		best = min(best, perPart())
	}
	// go-message's reader alone is 8 KiB for each part.
	if best > 4096 {
		t.Fatalf("%.0f bytes a part", best)
	}
	var shared partCharsets
	for i := range 4 * maxWordDecoders {
		if shared.open(fmt.Sprintf("windows-%d", 1250+i%9), strings.NewReader("a")) == nil {
			t.Fatal("not found")
		}
		if shared.open(fmt.Sprintf("iso-8859-%d", 1+i%15), strings.NewReader("a")) == nil && i%15+1 != 12 {
			t.Fatalf("iso-8859-%d not found", 1+i%15)
		}
		if len(shared.decoders) > maxWordDecoders {
			t.Fatalf("%d decoders kept", len(shared.decoders))
		}
	}
}
