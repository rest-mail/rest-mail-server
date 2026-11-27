package mime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// What clamav and rspamd are sent is Serialize of what the filters are handed, so a body tree has to
// come back from it whole, at any depth and with each multipart's own type: the texts under
// multipart/mixed → multipart/alternative are what a mail client reads (#1968, #2107 MIME AC03).
func TestSerializeKeepsTheWholeBodyTree(t *testing.T) {
	const head = "From: a@example.com\r\nTo: b@example.com\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nMIME-Version: 1.0\r\n"
	for _, tc := range []struct {
		name, message string
	}{
		{"alternatives beside an attachment", head + "Content-Type: multipart/mixed; boundary=m\r\n\r\n" +
			"--m\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n--a\r\nContent-Type: text/plain\r\n\r\nplain\r\n--a\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--a--\r\n" +
			"--m\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\nPDF\r\n--m--\r\n"},
		{"alternatives in related in mixed", head + "Content-Type: multipart/mixed; boundary=m\r\n\r\n" +
			"--m\r\nContent-Type: multipart/related; boundary=r\r\n\r\n--r\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" +
			"--a\r\nContent-Type: text/plain\r\n\r\ndeep plain\r\n--a\r\nContent-Type: text/html\r\n\r\ndeep html\r\n--a--\r\n--r--\r\n" +
			"--m\r\nContent-Type: text/plain\r\n\r\nafter\r\n--m--\r\n"},
		{"two texts in a mixed body", head + "Content-Type: multipart/mixed; boundary=m\r\n\r\n" +
			"--m\r\nContent-Type: text/plain\r\n\r\none\r\n--m\r\nContent-Type: text/plain\r\n\r\ntwo\r\n--m--\r\n"},
		{"an encapsulated message", head + "Content-Type: multipart/mixed; boundary=m\r\n\r\n" +
			"--m\r\nContent-Type: text/plain\r\n\r\nforwarding\r\n--m\r\nContent-Type: message/rfc822\r\n\r\nSubject: inner\r\n\r\ninner words\r\n--m--\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := Parse([]byte(tc.message))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := Serialize(parsed)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse(Serialize) %v:\n%s", err, raw)
			}
			if got, want := bodyTree(again.Body), bodyTree(parsed.Body); got != want {
				t.Errorf("body tree sent %s, handed %s:\n%s", got, want, raw)
			}
			if len(again.Attachments) != len(parsed.Attachments) {
				t.Errorf("%d attachments sent, %d handed", len(again.Attachments), len(parsed.Attachments))
			}
		})
	}
}

func bodyTree(body pipeline.Body) string {
	if len(body.Parts) == 0 {
		return fmt.Sprintf("%s:%q", body.ContentType, strings.TrimSpace(body.Content))
	}
	parts := make([]string, 0, len(body.Parts))
	for _, part := range body.Parts {
		parts = append(parts, bodyTree(part))
	}
	return body.ContentType + "[" + strings.Join(parts, " ") + "]"
}
