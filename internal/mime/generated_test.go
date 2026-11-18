package mime

import (
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

func generatedFixture() Generated {
	return Generated{
		From:      pipeline.Address{Name: "Server", Address: "postmaster@example.test"},
		To:        []pipeline.Address{{Address: "owner@example.test"}},
		Subject:   "Notice",
		MessageID: "<id@example.test>",
		Date:      time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	}
}

// Every line of the source ends in CRLF and fits, and the source is 7-bit, whatever the text holds.
func TestRenderGeneratedLines(t *testing.T) {
	long := strings.Repeat("Grüße ", 400)
	m := generatedFixture()
	m.Subject = "Über" + strings.Repeat(" the limit", 30)
	m.BodyText = "bare LF\nbare CR\rCRLF\r\n.dot\n" + long + "\n \t\n"
	m.BodyHTML = "<p>" + long + "</p>"
	raw, err := RenderGenerated(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(raw, "\r\n") {
		t.Fatalf("no final line break: %q", raw)
	}
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.ContainsAny(line, "\r\n") {
			t.Fatalf("a bare line break in %q", line)
		}
		if len(line) > 998 {
			t.Fatalf("a line of %d octets", len(line))
		}
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] >= 0x80 {
			t.Fatalf("8-bit octet at %d: %q", i, raw[i:i+1])
		}
	}
	parsed, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Headers.Subject != m.Subject {
		t.Errorf("subject %q", parsed.Headers.Subject)
	}
	if len(parsed.Body.Parts) != 2 {
		t.Fatalf("%d parts", len(parsed.Body.Parts))
	}
	if got := parsed.Body.Parts[1].Content; got != m.BodyHTML {
		t.Errorf("the HTML reads back as %q", got)
	}
	if got := parsed.Body.Parts[0].Content; !strings.Contains(got, long) || !strings.Contains(got, "bare LF\r\nbare CR\r\nCRLF\r\n.dot\r\n") {
		t.Errorf("the text reads back as %q", got)
	}
}

// Text alone is a single part, with the required headers.
func TestRenderGeneratedTextOnly(t *testing.T) {
	m := generatedFixture()
	m.BodyText = "hello"
	raw, err := RenderGenerated(m)
	if err != nil {
		t.Fatal(err)
	}
	const want = "From: \"Server\" <postmaster@example.test>\r\n" +
		"To: <owner@example.test>\r\n" +
		"Subject: Notice\r\n" +
		"Date: Wed, 04 Nov 2026 12:00:00 +0000\r\n" +
		"Message-ID: <id@example.test>\r\n" +
		"Auto-Submitted: auto-generated\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"hello\r\n"
	if raw != want {
		t.Errorf("rendered\n%q\nwant\n%q", raw, want)
	}
}

// A line break in a field the message quotes can't start a header of its own.
func TestRenderGeneratedSubjectCannotAddAHeader(t *testing.T) {
	m := generatedFixture()
	m.Subject = "hi\r\nBcc: stranger@example.test"
	m.BodyText = "hello"
	raw, err := RenderGenerated(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "\r\nBcc:") {
		t.Errorf("a header was injected: %q", raw)
	}
}
