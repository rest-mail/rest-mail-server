package mime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// nestedMessage is a message whose multiparts nest depth deep, with one text part at the bottom.
func nestedMessage(depth int) string {
	var head, tail strings.Builder
	head.WriteString("From: a@example.com\r\nTo: b@example.com\r\nSubject: nested\r\nMIME-Version: 1.0\r\n")
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&head, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	head.WriteString("Content-Type: text/plain\r\n\r\nbottom\r\n")
	for i := depth - 1; i >= 0; i-- {
		fmt.Fprintf(&tail, "--b%d--\r\n", i)
	}
	return head.String() + tail.String()
}

// manyParts is a multipart/mixed message of n text parts.
func manyParts(n int) string {
	var b strings.Builder
	b.WriteString("From: a@example.com\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "--x\r\nContent-Type: text/plain\r\n\r\npart %d\r\n", i)
	}
	b.WriteString("--x--\r\n")
	return b.String()
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

func parseBoth(t *testing.T, message string) (parseRead, streamRead int, parseErr, streamErr error) {
	t.Helper()
	direct := &countingReader{r: strings.NewReader(message)}
	_, parseErr = parseFrom(direct)
	streamed := &countingReader{r: strings.NewReader(message)}
	_, streamErr = ParseStream(context.Background(), streamed, StreamOptions{Store: (&memoryStore{}).put, MaxText: 1 << 20})
	return direct.n, streamed.n, parseErr, streamErr
}

func withLimits(t *testing.T, l Limits) {
	t.Helper()
	before := CurrentLimits()
	SetLimits(l)
	t.Cleanup(func() { SetLimits(before) })
}

// A message nested past the limit is refused on both parsers, and the work done does not grow
// with how far past the limit it goes: both parsers stop reading at the limit (#1641).
func TestParsersRefuseNestingPastTheLimit(t *testing.T) {
	withLimits(t, Limits{MaxNestingDepth: 20})
	var shortRead, shortStreamRead int
	for _, depth := range []int{200, 2000} {
		message := nestedMessage(depth)
		parseRead, streamRead, parseErr, streamErr := parseBoth(t, message)
		if !errors.Is(parseErr, ErrStructureLimit) || !errors.Is(streamErr, ErrStructureLimit) {
			t.Fatalf("depth %d: Parse %v, ParseStream %v; want ErrStructureLimit from both", depth, parseErr, streamErr)
		}
		if parseRead >= len(message)/2 || streamRead >= len(message)/2 {
			t.Fatalf("depth %d: read %d and %d of %d bytes; want the parse to stop at the limit", depth, parseRead, streamRead, len(message))
		}
		if shortRead == 0 {
			shortRead, shortStreamRead = parseRead, streamRead
		} else if parseRead != shortRead || streamRead != shortStreamRead {
			t.Fatalf("depth %d read %d/%d bytes, depth 200 read %d/%d: the work grew with the depth", depth, parseRead, streamRead, shortRead, shortStreamRead)
		}
	}
}

func TestParsersTakeNestingAtTheLimit(t *testing.T) {
	withLimits(t, Limits{MaxNestingDepth: 20})
	_, _, parseErr, streamErr := parseBoth(t, nestedMessage(20))
	if parseErr != nil || streamErr != nil {
		t.Fatalf("depth at the limit: Parse %v, ParseStream %v; want both to parse", parseErr, streamErr)
	}
	_, _, parseErr, streamErr = parseBoth(t, nestedMessage(21))
	if !errors.Is(parseErr, ErrStructureLimit) || !errors.Is(streamErr, ErrStructureLimit) {
		t.Fatalf("one past the limit: Parse %v, ParseStream %v; want ErrStructureLimit", parseErr, streamErr)
	}
}

func TestParsersRefuseTooManyParts(t *testing.T) {
	withLimits(t, Limits{MaxParts: 30})
	_, _, parseErr, streamErr := parseBoth(t, manyParts(30))
	if parseErr != nil || streamErr != nil {
		t.Fatalf("parts at the limit: Parse %v, ParseStream %v; want both to parse", parseErr, streamErr)
	}
	_, _, parseErr, streamErr = parseBoth(t, manyParts(31))
	if !errors.Is(parseErr, ErrStructureLimit) || !errors.Is(streamErr, ErrStructureLimit) {
		t.Fatalf("one part past the limit: Parse %v, ParseStream %v; want ErrStructureLimit", parseErr, streamErr)
	}
}

// Ordinary nested mail parses under the default limits: depth 5 with attachments, and a
// forwarded message/rfc822.
func TestParsersTakeOrdinaryNestedMail(t *testing.T) {
	var b strings.Builder
	b.WriteString("From: a@example.com\r\nTo: b@example.com\r\nSubject: ordinary\r\nMIME-Version: 1.0\r\n")
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=m%d\r\n\r\n", i)
		fmt.Fprintf(&b, "--m%d\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"f%d.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0=\r\n", i, i)
		fmt.Fprintf(&b, "--m%d\r\n", i)
	}
	b.WriteString("Content-Type: message/rfc822\r\n\r\nFrom: c@example.com\r\nSubject: forwarded\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=alt\r\n\r\n--alt\r\nContent-Type: text/plain\r\n\r\nplain\r\n--alt\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--alt--\r\n")
	for i := 4; i >= 0; i-- {
		fmt.Fprintf(&b, "--m%d--\r\n", i)
	}
	message := b.String()
	parsed, err := Parse([]byte(message))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Attachments) != 5 {
		t.Fatalf("Parse kept %d attachments, want 5", len(parsed.Attachments))
	}
	streamed, err := ParseStream(context.Background(), strings.NewReader(message), StreamOptions{Store: (&memoryStore{}).put, MaxText: 1 << 20})
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if len(streamed.Attachments) != 5 {
		t.Fatalf("ParseStream kept %d attachments, want 5", len(streamed.Attachments))
	}
}

func TestReceivedCount(t *testing.T) {
	message := "Received: from a by b; Tue, 3 Nov 2026 10:00:00 +0000\r\n" +
		"received : from c\r\n by d; Tue, 3 Nov 2026 10:00:00 +0000\r\n" +
		"X-Received: not counted\r\n" +
		"Subject: hi\r\n\r\nReceived: in the body, not counted\r\n"
	if got := ReceivedCount([]byte(message)); got != 2 {
		t.Fatalf("ReceivedCount = %d, want 2", got)
	}
	withLimits(t, Limits{MaxReceived: 2})
	if TooManyReceived([]byte(message)) {
		t.Fatal("two Received fields at a limit of two were refused")
	}
	if !TooManyReceived([]byte("Received: x\r\n" + message)) {
		t.Fatal("three Received fields at a limit of two were taken")
	}
}
