package mime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"reflect"
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
	_, parseErr = ParseReader(direct)
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

// checkMessageStructure is what a caller holding only a reader does: the section, then the body.
func checkMessageStructure(source io.Reader) error {
	fields, br, reader, _, err := readTopFields(source, 0)
	if err != nil {
		return nil
	}
	return newHeaderSection(fields, reader).CheckStructure(br, 0)
}

// CheckStructure refuses what Parse refuses and only that, and stops reading where Parse stops.
func TestCheckStructureAgreesWithParse(t *testing.T) {
	withLimits(t, Limits{MaxNestingDepth: 20, MaxParts: 30})
	const outer = "From: a@example.com\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n"
	for name, message := range map[string]string{
		"empty":                     "",
		"not a message":             "this is not a header\r\nnor this\r\n\r\nbody\r\n",
		"not multipart":             "From: a@example.com\r\n\r\nbody\r\n",
		"multipart with no parts":   outer,
		"multipart no boundary":     "Content-Type: multipart/mixed\r\n\r\nbody\r\n",
		"nested at the limit":       nestedMessage(20),
		"nested past the limit":     nestedMessage(21),
		"nested far past the limit": nestedMessage(200),
		"parts at the limit":        manyParts(30),
		"parts past the limit":      manyParts(31),
		"cut short at the limit":    strings.TrimSuffix(manyParts(30), "--x--\r\n"),
		"cut short past the limit":  strings.TrimSuffix(manyParts(31), "--x--\r\n"),
		"an unknown charset":        outer + "--x\r\nContent-Type: text/plain; charset=nonsense\r\n\r\nbody\r\n--x--\r\n",
		"an unknown encoding":       outer + "--x\r\nContent-Transfer-Encoding: nonsense\r\n\r\nbody\r\n--x--\r\n",
		"a part that won't decode":  outer + "--x\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!\r\n--x\r\n\r\nbody\r\n--x--\r\n",
		"a part with bad headers":   outer + "--x\r\nno colon here\r\n\r\nbody\r\n--x\r\n\r\nbody\r\n--x--\r\n",
		"a nested part in base64": outer + "--x\r\nContent-Type: multipart/mixed; boundary=y\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
			base64.StdEncoding.EncodeToString([]byte("--y\r\n\r\nbody\r\n--y--\r\n")) + "\r\n--x--\r\n",
		"nested with parts past the limit": outer + strings.Repeat("--x\r\nContent-Type: multipart/mixed; boundary=y\r\n\r\n--y\r\n\r\nbody\r\n--y--\r\n", 16) + "--x--\r\n",
		"nested with parts at the limit":   outer + strings.Repeat("--x\r\nContent-Type: multipart/mixed; boundary=y\r\n\r\n--y\r\n\r\nbody\r\n--y--\r\n", 15) + "--x--\r\n",
		// However a part's type is spelled, what makes it a multipart is what Parse goes by.
		"capitals, parts past the limit":      outer + "--x\r\nContent-Type: MULTIPART/Mixed; boundary=y\r\n\r\n" + strings.Repeat("--y\r\n\r\nb\r\n", 31) + "--y--\r\n--x--\r\n",
		"folded, parts past the limit":        outer + "--x\r\nContent-Type:\r\n  multipart/mixed;\r\n boundary=y\r\n\r\n" + strings.Repeat("--y\r\n\r\nb\r\n", 31) + "--y--\r\n--x--\r\n",
		"lowers to multipart, past the limit": outer + "--x\r\nContent-Type: mult\u0130part/mixed; boundary=y\r\n\r\n" + strings.Repeat("--y\r\n\r\nb\r\n", 31) + "--y--\r\n--x--\r\n",
		"a type that fails to parse":          outer + "--x\r\nContent-Type: multipart/mixed; boundary=y; boundary=z\r\n\r\n" + strings.Repeat("--y\r\n\r\nb\r\n", 31) + "--y--\r\n--x--\r\n",
		"not a multipart, looks like one":     outer + "--x\r\nContent-Type: multipart; boundary=y\r\n\r\n" + strings.Repeat("--y\r\n\r\nb\r\n", 31) + "--y--\r\n--x--\r\n",
		"a second type on a part":             outer + "--x\r\nContent-Type: text/plain\r\nContent-Type: multipart/mixed; boundary=y\r\n\r\n" + strings.Repeat("--y\r\n\r\nb\r\n", 31) + "--y--\r\n--x--\r\n",
		// A header section has no limit of its own, so a long field hides nothing from this.
		"a field past 1 MiB, parts past the limit": "X-Long: " + strings.Repeat("y", 1<<20+1) + "\r\n" + manyParts(31),
		"parts with no fields, past the limit":     outer + strings.Repeat("--x\r\n\r\nb\r\n", 31) + "--x--\r\n",
		"a bad section, parts past the limit":      "no colon here\r\n" + manyParts(31),
	} {
		t.Run(name, func(t *testing.T) {
			parsing := &countingReader{r: strings.NewReader(message)}
			_, parseErr := ParseReader(parsing)
			checking := &countingReader{r: strings.NewReader(message)}
			checkErr := checkMessageStructure(checking)
			if errors.Is(parseErr, ErrStructureLimit) != errors.Is(checkErr, ErrStructureLimit) {
				t.Fatalf("Parse %v, CheckStructure %v", parseErr, checkErr)
			}
			if checkErr != nil && !errors.Is(checkErr, ErrStructureLimit) {
				t.Fatalf("CheckStructure answered something other than the limit: %v", checkErr)
			}
			if errors.Is(checkErr, ErrStructureLimit) && checking.n > parsing.n {
				t.Fatalf("CheckStructure read %d bytes to refuse it, Parse %d", checking.n, parsing.n)
			}
		})
	}
	if err := checkMessageStructure(strings.NewReader(nestedMessage(21))); !errors.Is(err, ErrStructureLimit) {
		t.Fatalf("one past the limit: %v, want ErrStructureLimit", err)
	}
	if err := checkMessageStructure(strings.NewReader(manyParts(31))); !errors.Is(err, ErrStructureLimit) {
		t.Fatalf("one part past the limit: %v, want ErrStructureLimit", err)
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

// A section read before the body answers what the whole message parsed at once answers.
func TestHeaderSectionParsesAsTheWholeMessageDoes(t *testing.T) {
	const outer = "From: a@example.com\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n"
	for name, message := range map[string]string{
		"plain":                 "From: a@example.com\r\nTo: b@example.com, c@example.com\r\nSubject: =?utf-8?q?gr=C3=BC=C3=9Fe?=\r\n\r\nbody\r\n",
		"no body":               "From: a@example.com\r\n",
		"no line break at all":  "From: a@example.com",
		"bare line feeds":       "From: a@example.com\nSubject: s\n\nbody\n",
		"a blank first line":    "\r\nFrom: not a field\r\n",
		"a line of spaces":      "From: a@example.com\r\n \r\nSubject: s\r\n\r\nbody\r\n",
		"a charset":             "Content-Type: text/plain; charset=iso-8859-1\r\n\r\ngr\xfc\xdfe\r\n",
		"an unknown charset":    "Content-Type: text/plain; charset=nonsense\r\n\r\nbody\r\n",
		"quoted-printable":      "Content-Transfer-Encoding: quoted-printable\r\n\r\ngr=C3=BC=C3=9Fe\r\n",
		"a long field":          "X-Long: " + strings.Repeat("y", 1<<17) + "\r\nSubject: s\r\n\r\nbody\r\n",
		"a long Content-Type":   "Content-Type: text/plain; charset=utf-8" + strings.Repeat(";\r\n p=v", 1<<14) + "\r\n\r\nbody\r\n",
		"parts":                 outer + "--x\r\nContent-Type: text/plain\r\n\r\none\r\n--x\r\nContent-Type: application/pdf; name=a.pdf\r\n\r\ntwo\r\n--x--\r\n",
		"a part with a long id": outer + "--x\r\nContent-Id: " + strings.Repeat("y", 1<<17) + "\r\nContent-Disposition: inline\r\n\r\none\r\n--x--\r\n",
		"a calendar":            "Content-Type: text/calendar\r\n\r\nBEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			want, err := ParseReader(strings.NewReader(message))
			if err != nil {
				t.Fatal(err)
			}
			size := len(message)
			for _, end := range []string{"\n\n", "\n\r\n"} {
				if at := strings.Index(message, end); at >= 0 {
					size = min(size, at+len(end))
				}
			}
			if strings.HasPrefix(message, "\r\n") {
				size = 2
			}
			section, err := ReadHeaderSection(message[:size])
			if err != nil {
				t.Fatal(err)
			}
			if err := section.CheckStructure(strings.NewReader(message[size:]), int64(len(message)-size)); err != nil {
				t.Fatal(err)
			}
			got, err := section.Parse(message[size:])
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("section first %+v\nwhole %+v", got, want)
			}
		})
	}
	if _, err := ReadHeaderSection("no colon here\r\n\r\n"); err == nil {
		t.Fatal("a bad section parsed")
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
	if TooManyReceived(message) {
		t.Fatal("two Received fields at a limit of two were refused")
	}
	if !TooManyReceived("Received: x\r\n" + message) {
		t.Fatal("three Received fields at a limit of two were taken")
	}
}
