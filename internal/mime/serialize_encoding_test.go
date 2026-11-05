package mime

import (
	stdmime "mime"
	"net/mail"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// headerValue returns the unfolded value of the named header from a serialized
// message, or "" when it is absent.
func headerValue(t *testing.T, raw, name string) string {
	t.Helper()
	header, _, _ := strings.Cut(raw, "\r\n\r\n")

	var value string
	var found bool
	for _, line := range strings.Split(header, "\r\n") {
		if found {
			// A folded continuation line begins with whitespace.
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				value += " " + strings.TrimSpace(line)
				continue
			}
			break
		}
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(name)+":") {
			value = strings.TrimSpace(line[len(name)+1:])
			found = true
		}
	}
	return value
}

// A non-ASCII Subject has to leave as an RFC 2047 encoded-word. Raw UTF-8 in a
// header is non-conformant, and a strict receiver shows mojibake or rejects the
// message outright (issue #146).
func TestSerialize_EncodesNonASCIISubject(t *testing.T) {
	want := "Überweisung für Kaffee"
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:    pipeline.Addresses([]pipeline.Address{{Address: "alice@example.com"}}...),
			To:      pipeline.Addresses([]pipeline.Address{{Address: "bob@example.com"}}...),
			Subject: want,
			Date:    "Mon, 01 Jan 2024 12:00:00 +0000",
		},
		Body: pipeline.Body{ContentType: "text/plain; charset=utf-8", Content: "hi"},
	}

	raw, err := Serialize(email)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	out := string(raw)

	if strings.Contains(out, "Subject: Überweisung") {
		t.Error("the subject was written as raw UTF-8 rather than an encoded-word")
	}
	decoded, err := new(stdmime.WordDecoder).DecodeHeader(headerValue(t, out, "Subject"))
	if err != nil {
		t.Fatalf("decoding the Subject header: %v", err)
	}
	if decoded != want {
		t.Errorf("Subject round-tripped to %q, want %q", decoded, want)
	}
}

// The same holds for a display name, which must also stay parseable as an
// address. Quoting it with %q produces a Go string literal, not an RFC 5322
// quoted-string, and leaves non-ASCII bytes raw.
func TestSerialize_EncodesNonASCIIDisplayName(t *testing.T) {
	wantName := "Renée Müller"
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:    pipeline.Addresses([]pipeline.Address{{Name: wantName, Address: "renee@example.org"}}...),
			To:      pipeline.Addresses([]pipeline.Address{{Address: "bob@example.com"}}...),
			Subject: "Hallo",
			Date:    "Mon, 01 Jan 2024 12:00:00 +0000",
		},
		Body: pipeline.Body{ContentType: "text/plain; charset=utf-8", Content: "hi"},
	}

	raw, err := Serialize(email)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	out := string(raw)

	from := headerValue(t, out, "From")
	if strings.Contains(from, "Renée") {
		t.Errorf("the display name was written as raw UTF-8: %q", from)
	}
	addr, err := mail.ParseAddress(from)
	if err != nil {
		t.Fatalf("parsing From %q: %v", from, err)
	}
	if addr.Name != wantName || addr.Address != "renee@example.org" {
		t.Errorf("From round-tripped to %q <%s>, want %q <renee@example.org>",
			addr.Name, addr.Address, wantName)
	}
}

// RFC 5322 §2.1.1 caps a line at 998 octets excluding CRLF, so a long header
// has to be folded rather than emitted as one enormous line.
func TestSerialize_FoldsLongHeaders(t *testing.T) {
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:    pipeline.Addresses([]pipeline.Address{{Address: "alice@example.com"}}...),
			To:      pipeline.Addresses([]pipeline.Address{{Address: "bob@example.com"}}...),
			Subject: strings.TrimSpace(strings.Repeat("quarterly figures review ", 60)),
			Date:    "Mon, 01 Jan 2024 12:00:00 +0000",
		},
		Body: pipeline.Body{ContentType: "text/plain; charset=utf-8", Content: "hi"},
	}

	raw, err := Serialize(email)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	for i, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Errorf("line %d is %d octets, over the 998 limit: %.80q...", i, len(line), line)
		}
	}
}

// A header value carrying CRLF must not start a new header line. Values in
// Headers.Extra are set by pipeline transforms rather than read back from a
// parsed message, so they never pass through the parser's sanitising.
func TestSerialize_ExtraHeaderCannotInjectAHeader(t *testing.T) {
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:    pipeline.Addresses([]pipeline.Address{{Address: "alice@example.com"}}...),
			To:      pipeline.Addresses([]pipeline.Address{{Address: "bob@example.com"}}...),
			Subject: "Hello",
			Date:    "Mon, 01 Jan 2024 12:00:00 +0000",
			Extra:   map[string]string{"X-Thing": "ok\r\nBcc: mallory@evil.test"},
		},
		Body: pipeline.Body{ContentType: "text/plain; charset=utf-8", Content: "hi"},
	}

	raw, err := Serialize(email)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("an Extra header value injected a Bcc line:\n%s", string(raw))
		}
	}
}

// A Subject with a line break in it reads back on one line. Encoded as it stood,
// the break would not start a header in these bytes, but it would be there again
// for whatever decoded the Subject (#1594).
func TestSerialize_SubjectLosesItsLineBreaks(t *testing.T) {
	for name, subject := range map[string]string{
		"LF":          "Hello\nBcc: someone@example.com",
		"CRLF":        "Hello\r\nBcc: someone@example.com",
		"CR":          "Hello\rBcc: someone@example.com",
		"non-ASCII":   "Grüße\nBcc: someone@example.com",
		"long folded": strings.Repeat("Grüße ", 40) + "\nBcc: someone@example.com",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := Serialize(&pipeline.EmailJSON{
				Headers: pipeline.Headers{
					From:    pipeline.Addresses([]pipeline.Address{{Address: "a@example.com"}}...),
					Subject: subject,
				},
				Body: pipeline.Body{ContentType: "text/plain", Content: "hi"},
			})
			if err != nil {
				t.Fatal(err)
			}
			msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			if bcc := msg.Header["Bcc"]; len(bcc) != 0 {
				t.Errorf("a Subject added a header: Bcc: %q", bcc)
			}
			decoded, err := new(stdmime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
			if err != nil {
				t.Fatalf("decode the Subject: %v", err)
			}
			if strings.ContainsAny(decoded, "\r\n") {
				t.Errorf("the Subject decodes to %q, with a line break", decoded)
			}
			if !strings.Contains(decoded, "Bcc: someone@example.com") {
				t.Errorf("the Subject decodes to %q, without its text", decoded)
			}
		})
	}
}
