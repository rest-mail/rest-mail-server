package imap

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// normalizeToCRLF is the whole-message form of crlfReader, which APPEND used before the
// message was streamed (#2107): it collapses CRLF to LF, then makes every LF CRLF.
func normalizeToCRLF(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
	return b
}

// readCRLF reads src through a crlfReader in reads of size octets.
func readCRLF(t testing.TB, src io.Reader, size int) []byte {
	t.Helper()
	r := newCRLFReader(src)
	var out []byte
	buf := make([]byte, size)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCRLFReaderAgreesWithNormalizeToCRLF(t *testing.T) {
	inputs := []string{
		"", "\n", "\r", "\r\n", "\n\n", "\r\r\n", "\n\r", "a\nb\r\nc\rd\n",
		"Subject: x\n\nbody\n", "Subject: x\r\n\r\nbody\r\n",
		strings.Repeat("line\n", crlfChunk), strings.Repeat("\n", 3*crlfChunk+1),
		strings.Repeat("ab\r", crlfChunk) + "\n",
	}
	for _, in := range inputs {
		want := normalizeToCRLF([]byte(in))
		for _, size := range []int{1, 2, 3, 7, 512, crlfChunk, 4 * crlfChunk} {
			for name, src := range map[string]io.Reader{
				"whole":    strings.NewReader(in),
				"one-byte": iotest.OneByteReader(strings.NewReader(in)),
				"half":     iotest.HalfReader(strings.NewReader(in)),
				"data-err": iotest.DataErrReader(strings.NewReader(in)),
			} {
				if got := readCRLF(t, src, size); !bytes.Equal(got, want) {
					t.Fatalf("%q in reads of %d (%s) = %q, want %q", in, size, name, got, want)
				}
			}
		}
	}
	if err := iotest.TestReader(newCRLFReader(strings.NewReader("a\nb\r\nc\n")), []byte("a\r\nb\r\nc\r\n")); err != nil {
		t.Fatal(err)
	}
}

func FuzzCRLFReader(f *testing.F) {
	f.Add([]byte("a\nb\r\nc\rd\n"), uint8(1))
	f.Add([]byte("\r\r\n\n\r"), uint8(2))
	f.Fuzz(func(t *testing.T, in []byte, size uint8) {
		want := normalizeToCRLF(in)
		if got := readCRLF(t, iotest.HalfReader(bytes.NewReader(in)), int(size)+1); !bytes.Equal(got, want) {
			t.Fatalf("%q in reads of %d = %q, want %q", in, int(size)+1, got, want)
		}
	})
}
