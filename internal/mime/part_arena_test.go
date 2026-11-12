package mime

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// Parts read into one buffer, so one that's kept must keep what it holds, whatever is read after
// it: a part given back, a part that fails, or more than the buffer can hold.
func TestPartsKeepWhatTheyHoldInOneBuffer(t *testing.T) {
	latin := strings.Repeat("\xe9", 300)
	grown := strings.Repeat("é", 300)
	part := func(header, content string) string {
		return "--B\r\n" + header + "\r\n\r\n" + content + "\r\n"
	}
	message := "Subject: s\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
		part("Content-Type: text/plain", "first") +
		part("Content-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf", "given back") +
		part("Content-Type: text/html", "<p>second</p>") +
		part("Content-Type: image/png\r\nContent-Disposition: inline\r\nContent-ID: <i>", "also given back") +
		part("Content-Type: a/b\r\nContent-Transfer-Encoding: base64", base64.StdEncoding.EncodeToString([]byte("third"))) +
		part("Content-Type: text/calendar", "not a calendar") +
		// A charset makes these longer than the message they came in, so the buffer can't hold
		// them all and a part moves to another.
		part("Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit", latin) +
		part("Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit", latin) +
		part("Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit", latin) +
		part("Content-Type: a/b", "last") +
		"--B--\r\n"
	want := []string{"first", "<p>second</p>", "third", "not a calendar", grown, grown, grown, "last"}
	for name, source := range map[string]io.Reader{
		"bytes": bytes.NewReader([]byte(message)),
		"text":  strings.NewReader(message),
		// A reader with no size says nothing of how large the buffer should be.
		"one byte": iotest.OneByteReader(strings.NewReader(message)),
	} {
		t.Run(name, func(t *testing.T) {
			email, err := ParseReader(source)
			if err != nil {
				t.Fatal(err)
			}
			if len(email.Body.Parts) != len(want) {
				t.Fatalf("%d parts, want %d", len(email.Body.Parts), len(want))
			}
			for i, part := range email.Body.Parts {
				if part.Content != want[i] {
					t.Errorf("part %d holds %q, want %q", i, part.Content, want[i])
				}
			}
			if len(email.Attachments) != 1 || len(email.Inline) != 1 {
				t.Fatalf("%d attachments and %d inline", len(email.Attachments), len(email.Inline))
			}
			if email.Attachments[0].Content != base64.StdEncoding.EncodeToString([]byte("given back")) {
				t.Errorf("attachment holds %q", email.Attachments[0].Content)
			}
			if email.Inline[0].Content != base64.StdEncoding.EncodeToString([]byte("also given back")) {
				t.Errorf("inline holds %q", email.Inline[0].Content)
			}
		})
	}
}

// failingAfter reads text, and then fails.
type failingAfter struct {
	text *strings.Reader
	err  error
}

func (f *failingAfter) Read(p []byte) (int, error) {
	if f.text.Len() == 0 {
		return 0, f.err
	}
	return f.text.Read(p)
}

// A part that can't be read leaves nothing behind, and one it follows stays as it was.
func TestReadPartTakesBackWhatFailed(t *testing.T) {
	failure := errors.New("failed")
	budget := &partBudget{sourceSize: 64}
	kept, err := budget.readPart(strings.NewReader("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.readPart(&failingAfter{text: strings.NewReader("half of it"), err: failure}); !errors.Is(err, failure) {
		t.Fatalf("read answered %v", err)
	}
	next, err := budget.readPart(strings.NewReader("next"))
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "kept" || string(next) != "next" || string(budget.arena) != "keptnext" {
		t.Fatalf("kept %q, next %q, arena %q", kept, next, budget.arena)
	}
	budget.release(next)
	if string(budget.arena) != "kept" {
		t.Fatalf("arena %q after the release", budget.arena)
	}
	// Past what the arena holds, the part moves, and what's kept stays where it was.
	long := strings.Repeat("x", 4*cap(budget.arena))
	moved, err := budget.readPart(strings.NewReader(long))
	if err != nil {
		t.Fatal(err)
	}
	if string(moved) != long || string(kept) != "kept" {
		t.Fatalf("moved %d bytes, kept %q", len(moved), kept)
	}
}
