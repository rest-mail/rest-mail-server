package handlers

import (
	"fmt"
	"io"
	stdmime "mime"
	"mime/multipart"
	"strings"
	"testing"
	"time"
)

func testOutboundDate() time.Time {
	return time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
}

func testOutboundMessage() outboundMessage {
	return outboundMessage{
		FromName:  "Alice",
		From:      "alice@example.com",
		To:        []string{"bob@example.com"},
		Subject:   "Hello",
		BodyText:  "hi",
		MessageID: "<abc@example.com>",
		Date:      testOutboundDate(),
	}
}

// outboundHeaderValue returns the unfolded value of a header from a rendered
// message, or "" when it is absent.
func outboundHeaderValue(t *testing.T, raw, name string) string {
	t.Helper()
	header, _, _ := strings.Cut(raw, "\r\n\r\n")

	var value string
	var found bool
	for _, line := range strings.Split(header, "\r\n") {
		if found {
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

// A non-ASCII Subject has to leave as an RFC 2047 encoded-word; raw UTF-8 in a
// header is non-conformant and a strict receiver mangles or rejects it (#146).
func TestBuildOutboundRaw_EncodesNonASCIISubject(t *testing.T) {
	m := testOutboundMessage()
	m.Subject = "Überweisung für Kaffee"

	raw, err := buildOutboundRaw(m)
	if err != nil {
		t.Fatalf("buildOutboundRaw: %v", err)
	}

	if strings.Contains(raw, "Subject: Überweisung") {
		t.Error("the subject was written as raw UTF-8 rather than an encoded-word")
	}
	decoded, err := new(stdmime.WordDecoder).DecodeHeader(outboundHeaderValue(t, raw, "Subject"))
	if err != nil {
		t.Fatalf("decoding the Subject header: %v", err)
	}
	if decoded != m.Subject {
		t.Errorf("Subject round-tripped to %q, want %q", decoded, m.Subject)
	}
}

// RFC 5322 §2.1.1 caps a line at 998 octets excluding CRLF.
func TestBuildOutboundRaw_FoldsLongHeaders(t *testing.T) {
	m := testOutboundMessage()
	m.Subject = strings.TrimSpace(strings.Repeat("quarterly figures review ", 60))

	raw, err := buildOutboundRaw(m)
	if err != nil {
		t.Fatalf("buildOutboundRaw: %v", err)
	}

	for i, line := range strings.Split(raw, "\r\n") {
		if len(line) > 998 {
			t.Errorf("line %d is %d octets, over the 998 limit: %.80q...", i, len(line), line)
		}
	}
}

// Extra headers come from pipeline transforms, so they never pass through the
// parser's CR/LF sanitising. A value carrying CRLF must not become a header.
func TestBuildOutboundRaw_ExtraHeaderCannotInjectAHeader(t *testing.T) {
	m := testOutboundMessage()
	m.Extra = map[string]string{"X-Thing": "ok\r\nBcc: mallory@evil.test"}

	raw, err := buildOutboundRaw(m)
	if err != nil {
		t.Fatalf("buildOutboundRaw: %v", err)
	}

	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("an Extra header value injected a Bcc line:\n%s", raw)
		}
	}
}

// The multipart boundary was derived from the send time — the same value the
// message publishes in its own Date header — and was never checked against the
// parts. A sender who computes it can close the multipart early and hide
// everything after it from a client.
func TestBuildOutboundRaw_BodyCannotForgeTheBoundary(t *testing.T) {
	m := testOutboundMessage()
	forged := fmt.Sprintf("=_restmail_%d", m.Date.UnixNano())
	m.BodyText = "before\r\n--" + forged + "--\r\nafter"
	m.BodyHTML = "<p>html</p>"

	raw, err := buildOutboundRaw(m)
	if err != nil {
		t.Fatalf("buildOutboundRaw: %v", err)
	}

	_, body, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatalf("no header/body separator in:\n%s", raw)
	}
	// Read Content-Type through the unfolding helper: with a random boundary the
	// header is long enough to be folded onto a continuation line.
	contentType := outboundHeaderValue(t, raw, "Content-Type")
	_, params, err := stdmime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("Content-Type %q: %v", contentType, err)
	}

	r := multipart.NewReader(strings.NewReader(body), params["boundary"])
	var parts int
	for {
		p, err := r.NextPart()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(p)
		if parts == 0 && !strings.Contains(string(data), "after") {
			t.Errorf("the text part was cut short at the forged boundary: %q", data)
		}
		parts++
	}
	if parts != 2 {
		t.Errorf("read %d parts, want 2 (text and HTML)", parts)
	}
}
