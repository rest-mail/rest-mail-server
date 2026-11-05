package mime

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"math/rand/v2"
	stdmime "mime"
	"runtime"
	"strings"
	"testing"

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
