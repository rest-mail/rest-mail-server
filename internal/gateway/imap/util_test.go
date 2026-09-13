package imap

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// ---------------------------------------------------------------------------
// buildRawMessage
// ---------------------------------------------------------------------------

func TestBuildRawMessage_TextOnly(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Test",
			ReceivedAt: fixedTime,
			MessageID:  "abc123@example.com",
		},
		BodyText: "Hello, world!",
	}
	result := buildRawMessage(msg)

	// The display name is rendered by net/mail, which quotes it rather than
	// pasting it in raw — the same rendering that encodes a non-ASCII name and
	// drops the empty gap when there is no name at all.
	if !strings.Contains(result, "From: \"Alice\" <alice@example.com>\r\n") {
		t.Errorf("missing From header in: %s", result)
	}
	if !strings.Contains(result, "Subject: Test\r\n") {
		t.Errorf("missing Subject header in: %s", result)
	}
	if !strings.Contains(result, "Message-ID: <abc123@example.com>\r\n") {
		t.Errorf("missing Message-ID header in: %s", result)
	}
	if !strings.Contains(result, "Content-Type: text/plain; charset=utf-8\r\n") {
		t.Errorf("missing Content-Type in: %s", result)
	}
	if !strings.Contains(result, "Hello, world!") {
		t.Errorf("missing body text in: %s", result)
	}
	// Should NOT be multipart
	if strings.Contains(result, "multipart") {
		t.Errorf("text-only message should not be multipart: %s", result)
	}
}

func TestBuildRawMessage_HTMLOnly(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "HTML Test",
			ReceivedAt: fixedTime,
		},
		BodyHTML: "<p>Hello</p>",
	}
	result := buildRawMessage(msg)

	if !strings.Contains(result, "Content-Type: text/html; charset=utf-8\r\n") {
		t.Errorf("missing text/html Content-Type in: %s", result)
	}
	if !strings.Contains(result, "<p>Hello</p>") {
		t.Errorf("missing HTML body in: %s", result)
	}
	if strings.Contains(result, "multipart") {
		t.Errorf("HTML-only message should not be multipart: %s", result)
	}
}

func TestBuildRawMessage_Multipart(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Multi",
			ReceivedAt: fixedTime,
		},
		BodyText: "Hello text",
		BodyHTML: "<p>Hello HTML</p>",
	}
	result := buildRawMessage(msg)

	if !strings.Contains(result, "multipart/alternative") {
		t.Errorf("should be multipart/alternative: %s", result)
	}
	if !strings.Contains(result, "Hello text") {
		t.Errorf("missing text body in multipart: %s", result)
	}
	if !strings.Contains(result, "<p>Hello HTML</p>") {
		t.Errorf("missing HTML body in multipart: %s", result)
	}
	// Should contain boundary markers
	if !strings.Contains(result, "--=_restmail_") {
		t.Errorf("missing boundary marker in: %s", result)
	}
}

func TestBuildRawMessage_NoMessageID(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "No MID",
			ReceivedAt: fixedTime,
		},
		BodyText: "body",
	}
	result := buildRawMessage(msg)

	if strings.Contains(result, "Message-ID:") {
		t.Errorf("should not contain Message-ID header when empty: %s", result)
	}
}

func TestBuildRawMessage_InReplyTo(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Re: Test",
			ReceivedAt: fixedTime,
		},
		BodyText:  "reply",
		InReplyTo: "orig@example.com",
	}
	result := buildRawMessage(msg)

	if !strings.Contains(result, "In-Reply-To: <orig@example.com>\r\n") {
		t.Errorf("missing In-Reply-To header in: %s", result)
	}
}

func TestBuildRawMessage_MIMEVersion(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Mime",
			ReceivedAt: fixedTime,
		},
		BodyText: "body",
	}
	result := buildRawMessage(msg)
	if !strings.Contains(result, "MIME-Version: 1.0\r\n") {
		t.Errorf("missing MIME-Version header in: %s", result)
	}
}

// A non-ASCII Subject has to leave as an RFC 2047 encoded-word. Raw UTF-8 in a
// header is non-conformant, and a strict client shows mojibake instead of the
// subject the sender typed (issue #299).
func TestBuildRawMessage_NonASCIISubjectIsEncoded(t *testing.T) {
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Überweisung für Kaffee",
			ReceivedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		},
		BodyText: "body",
	}
	result := buildRawMessage(msg)

	if strings.Contains(result, "Subject: Überweisung") {
		t.Error("the subject was written as raw UTF-8 rather than an RFC 2047 encoded-word")
	}
	if !strings.Contains(strings.ToLower(result), "subject: =?utf-8?") {
		t.Errorf("no encoded-word in: %s", result)
	}
}

// The multipart boundary must not be one the body can contain. The builder
// derived it from the receipt timestamp, which is carried in the Date header of
// the very message being built, so a sender able to guess it could close the
// multipart early and hide content from the part that follows.
func TestBuildRawMessage_BodyCannotForgeTheBoundary(t *testing.T) {
	ts := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	forged := fmt.Sprintf("--=_restmail_%d", ts.UnixNano())
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Forged",
			ReceivedAt: ts,
		},
		BodyText: "before\r\n" + forged + "--\r\nafter",
		BodyHTML: "<p>html</p>",
	}
	result := buildRawMessage(msg)

	header, body, ok := strings.Cut(result, "\r\n\r\n")
	if !ok {
		t.Fatalf("no header/body separator in: %s", result)
	}
	var ctype string
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-type:") {
			ctype = strings.TrimSpace(line[len("content-type:"):])
		}
	}
	_, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		t.Fatalf("Content-Type %q: %v", ctype, err)
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

// A message with no sender name must not produce "From:  <addr>" with the empty
// display name left as a gap before the angle bracket.
func TestBuildRawMessage_EmptySenderName(t *testing.T) {
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			ReceivedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			Subject:    "No name",
		},
		BodyText: "body",
	}
	result := buildRawMessage(msg)

	if strings.Contains(result, "From:  <") {
		t.Errorf("empty sender name left a double space in: %s", result)
	}
	if !strings.Contains(result, "From: <alice@example.com>\r\n") {
		t.Errorf("missing bare-address From header in: %s", result)
	}
}
