package handlers

import (
	"errors"
	"strings"
	"testing"

	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// The header section ends at the first empty line, wherever that falls in what's read at a time, and
// the file's own length of it is kept even when a field is removed from what's answered.
func TestHeaderSectionIsReadOnceAtItsLength(t *testing.T) {
	long := "X-Long: " + strings.Repeat("y", 4096-len("X-Long: ")) // fills bufio's buffer exactly
	for name, c := range map[string]struct{ message, section string }{
		"empty":                  {"", ""},
		"no empty line":          {"From: a@b.test\r\n", "From: a@b.test\r\n"},
		"no line break":          {"From: a@b.test", "From: a@b.test"},
		"CRLF":                   {"From: a@b.test\r\n\r\nbody\r\n\r\nmore", "From: a@b.test\r\n\r\n"},
		"LF":                     {"From: a@b.test\n\nbody\n", "From: a@b.test\n\n"},
		"a blank first line":     {"\r\nbody\r\n", "\r\n"},
		"a line of a space":      {"From: a@b.test\r\n \r\nTo: c@d.test\r\n\r\nbody", "From: a@b.test\r\n \r\nTo: c@d.test\r\n\r\n"},
		"a line ends its buffer": {long + "\r\nTo: c@d.test\r\n\r\nbody", long + "\r\nTo: c@d.test\r\n\r\n"},
		"a long line":            {"X-Long: " + strings.Repeat("y", 1<<16) + "\n\nbody", "X-Long: " + strings.Repeat("y", 1<<16) + "\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := newSpooledText()
			if err != nil {
				t.Fatal(err)
			}
			defer raw.remove()
			if err := raw.replace([]byte(c.message)); err != nil {
				t.Fatal(err)
			}
			spool := &deliverySpool{raw: raw}
			for range 2 {
				section, err := spool.headerSection()
				if err != nil {
					t.Fatal(err)
				}
				if section != c.section {
					t.Fatalf("section %q", section)
				}
			}
			if spool.sectionSize != int64(len(c.section)) {
				t.Fatalf("size %d for %d", spool.sectionSize, len(c.section))
			}
		})
	}
	// What's refused is the message past a removed field, read from where the file says it starts.
	claimed := "Authentication-Results: " + pipeline.AuthServID + "; dkim=pass\r\n"
	message := claimed + "Content-Type: multipart/mixed; boundary=B\r\n\r\n" + strings.Repeat("--B\r\n\r\nx\r\n", rmime.CurrentLimits().MaxParts+1) + "--B--\r\n"
	raw, err := newSpooledText()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.remove()
	if err := raw.replace([]byte(message)); err != nil {
		t.Fatal(err)
	}
	spool := &deliverySpool{raw: raw}
	if _, parseErr, err := spool.readHeaders(); parseErr != nil || !errors.Is(err, rmime.ErrStructureLimit) {
		t.Fatalf("parse %v, check %v", parseErr, err)
	}
	if strings.Contains(spool.section, "Authentication-Results") || spool.sectionSize <= int64(len(spool.section)) {
		t.Fatalf("section %q of %d", spool.section, spool.sectionSize)
	}
}
