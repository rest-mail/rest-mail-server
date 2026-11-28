package wire

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// retrLines is what RETR sends for raw from memory: the lines of WireMessage,
// dot-stuffed and each ended in CRLF, without the terminator.
func retrLines(raw string) string {
	var b strings.Builder
	for _, line := range SplitMessageLines(WireMessage(raw)) {
		b.WriteString(DotStuff(line) + "\r\n")
	}
	return b.String()
}

// topLines is what TOP sends for raw from memory, without the terminator.
func topLines(raw string, n int) string {
	var b strings.Builder
	for _, line := range TopLines(CanonicalCRLF(raw), n) {
		b.WriteString(DotStuff(line) + "\r\n")
	}
	return b.String()
}

// readers yields raw through readers that split it differently, so a line, a CRLF
// or a leading dot that spans two reads is framed as one that does not.
func readers(raw string) map[string]func() io.Reader {
	return map[string]func() io.Reader{
		"whole":    func() io.Reader { return strings.NewReader(raw) },
		"one byte": func() io.Reader { return iotest.OneByteReader(strings.NewReader(raw)) },
		"halves":   func() io.Reader { return iotest.HalfReader(strings.NewReader(raw)) },
		"eof with data": func() io.Reader {
			return iotest.DataErrReader(strings.NewReader(raw))
		},
	}
}

func streamedRetr(t *testing.T, r io.Reader) (string, int64) {
	t.Helper()
	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	n, err := SendMessage(w, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.String(), n
}

func streamedTop(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	if err := SendTop(w, r, n); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// checkStreamed proves that the streamed RETR, its count and the streamed TOP
// send exactly what the in-memory framing sends for raw.
func checkStreamed(t *testing.T, raw string) {
	t.Helper()
	want := retrLines(raw)
	wantSize := int64(len(WireMessage(raw)))
	for name, open := range readers(raw) {
		got, sent := streamedRetr(t, open())
		if got != want || sent != wantSize {
			t.Fatalf("%s: RETR of %q streamed %q (%d octets), want %q (%d)", name, raw, got, sent, want, wantSize)
		}
		size, err := CountMessage(open())
		if err != nil || size != wantSize {
			t.Fatalf("%s: count of %q = %d, %v; want %d", name, raw, size, err, wantSize)
		}
		for n := 0; n <= 3; n++ {
			if got, want := streamedTop(t, open(), n), topLines(raw, n); got != want {
				t.Fatalf("%s: TOP %d of %q streamed %q, want %q", name, n, raw, got, want)
			}
		}
	}
}

var streamShapes = []string{
	"",
	"\n", "\r", "\r\n", "\n\n", "\r\r", "\r\n\r\n", "\n\r", "\r\r\n",
	".", "..", ".\r\n", ".\n", "\r.", "\n.", "\r\n.\r\n", ".\r",
	"Subject: hi\r\n\r\nbody\r\n",
	"Subject: hi\n\nline one\nline two\n",
	"Subject: hi\r\n\r\nline one\nline two\r\n",
	"Subject: hi\r\n\r\nline\rone\r\n",
	"Subject: hi\r\n\r\nline\r\r\n",
	"Subject: hi\r\n\r\nline\n\rone\r\n",
	"Subject: hi\r\n\r\nbody",
	"Subject: hi\r\n\r\nbody\r",
	"Subject: hi\r\n\r\nbody\n",
	"Subject: hi\r\n\r\n",
	"Subject: hi\n\n",
	"Subject: hi\r\n",
	"Subject: hi",
	"Subject: hi\r\n\r\n.\r\n..\r\n.leading\r\nplain\r\n",
	"Subject: hi\n\n.\n..\n.leading\nplain\n",
	"Subject: hi\r\n\r\n.last",
	"Subject: Gr\xc3\xbc\xc3\x9fe\r\n\r\n8-bit \xff\x00 body\r\n",
	"\r\nno header\r\nsecond\r\n",
	"\nno header\nsecond",
	"A: 1\r\nB: 2\r\n\r\none\r\ntwo\r\n\r\nfour\r\n.five\r\n",
	"A: 1\r\r\n\r\nbody\r\n",
	"A: 1\r\n\n\r\nbody",
}

func TestStreamedFramingMatchesInMemory(t *testing.T) {
	for _, raw := range streamShapes {
		checkStreamed(t, raw)
	}
}

// Every message of up to seven octets drawn from the octets framing treats
// specially is framed the same streamed as in memory.
func TestStreamedFramingMatchesInMemoryExhaustively(t *testing.T) {
	alphabet := []byte{'\r', '\n', '.', 'a'}
	var walk func(prefix []byte)
	walk = func(prefix []byte) {
		raw := string(prefix)
		want := retrLines(raw)
		got, sent := streamedRetr(t, strings.NewReader(raw))
		if got != want || sent != int64(len(WireMessage(raw))) {
			t.Fatalf("RETR of %q streamed %q (%d), want %q", raw, got, sent, want)
		}
		for n := 0; n <= 2; n++ {
			if got, want := streamedTop(t, strings.NewReader(raw), n), topLines(raw, n); got != want {
				t.Fatalf("TOP %d of %q streamed %q, want %q", n, raw, got, want)
			}
		}
		if len(prefix) == 7 {
			return
		}
		for _, b := range alphabet {
			walk(append(prefix, b))
		}
	}
	walk(nil)
}

// A message longer than a read, with lines longer than a read, frames the same.
func TestStreamedFramingAcrossReads(t *testing.T) {
	long := strings.Repeat("x", streamChunk+7)
	raw := "Subject: big\r\n\r\n" + long + "\r\n." + long + "\n\r" + long + "\r"
	checkStreamed(t, raw)
	checkStreamed(t, strings.Repeat(".\r\n", streamChunk))
}

// TOP stops reading once its last line has ended.
func TestSendTopStopsReading(t *testing.T) {
	r := &countingReader{r: iotest.OneByteReader(strings.NewReader("A: 1\r\n\r\none\r\ntwo\r\nthree\r\n"))}
	if got := streamedTop(t, r, 1); got != "A: 1\r\n\r\none\r\n" {
		t.Fatalf("TOP 1 sent %q", got)
	}
	if want := len("A: 1\r\n\r\none\r\nt"); r.n != want {
		t.Fatalf("TOP 1 read %d octets, want %d", r.n, want)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

var errBroken = errors.New("broken")

// A read error stops the count and the response, and is returned.
func TestStreamedFramingReturnsReadErrors(t *testing.T) {
	broken := func() io.Reader {
		return io.MultiReader(strings.NewReader("A: 1\r\n\r\nbody\r\n"), iotest.ErrReader(errBroken))
	}
	if _, err := CountMessage(broken()); !errors.Is(err, errBroken) {
		t.Fatalf("CountMessage: %v", err)
	}
	if _, err := SendMessage(bufio.NewWriter(io.Discard), broken()); !errors.Is(err, errBroken) {
		t.Fatalf("SendMessage: %v", err)
	}
	if err := SendTop(bufio.NewWriter(io.Discard), broken(), 5); !errors.Is(err, errBroken) {
		t.Fatalf("SendTop: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errBroken }

// A write error stops the response, before the message is read to its end, and is
// returned.
func TestStreamedFramingReturnsWriteErrors(t *testing.T) {
	raw := strings.Repeat("line\r\n", 4096)
	r := &countingReader{r: iotest.OneByteReader(strings.NewReader(raw))}
	if _, err := SendMessage(bufio.NewWriterSize(failingWriter{}, 16), r); !errors.Is(err, errBroken) {
		t.Fatalf("SendMessage: %v", err)
	}
	if r.n >= len(raw) {
		t.Fatalf("SendMessage read all %d octets after its writer failed", r.n)
	}
	if err := SendTop(bufio.NewWriterSize(failingWriter{}, 16), strings.NewReader(raw), 4096); !errors.Is(err, errBroken) {
		t.Fatalf("SendTop: %v", err)
	}
}

// The streamed framing reads and writes through fixed buffers, whatever the
// message's size.
func TestStreamedFramingHoldsNoCopy(t *testing.T) {
	raw := strings.Repeat("a line of a large message\n", 1<<15) // ~850 KiB
	w := bufio.NewWriter(io.Discard)
	allocs := testing.AllocsPerRun(5, func() {
		if _, err := SendMessage(w, strings.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := CountMessage(strings.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 4 {
		t.Fatalf("streaming %d octets made %v allocations", len(raw), allocs)
	}
}

func FuzzStreamedFraming(f *testing.F) {
	for _, raw := range streamShapes {
		f.Add(raw, uint8(1))
	}
	f.Fuzz(func(t *testing.T, raw string, n uint8) {
		got, sent := streamedRetr(t, iotest.HalfReader(strings.NewReader(raw)))
		if want := retrLines(raw); got != want || sent != int64(len(WireMessage(raw))) {
			t.Fatalf("RETR of %q streamed %q (%d), want %q", raw, got, sent, want)
		}
		if got, want := streamedTop(t, strings.NewReader(raw), int(n)), topLines(raw, int(n)); got != want {
			t.Fatalf("TOP %d of %q streamed %q, want %q", n, raw, got, want)
		}
	})
}
