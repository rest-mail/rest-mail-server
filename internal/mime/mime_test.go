package mime

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// simplePlainTextMessage is a minimal RFC 2822 text/plain email.
const simplePlainTextMessage = "From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>\r\n" +
	"Subject: Hello\r\n" +
	"Date: Mon, 01 Jan 2024 12:00:00 +0000\r\n" +
	"Message-ID: <msg001@example.com>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hi Bob,\r\n" +
	"\r\n" +
	"This is a plain text email.\r\n"

func TestParse_SimplePlainText(t *testing.T) {
	email, err := Parse([]byte(simplePlainTextMessage))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	// From
	if len(email.Headers.From) != 1 {
		t.Fatalf("expected 1 From address, got %d", len(email.Headers.From))
	}
	if email.Headers.From[0].Address != "alice@example.com" {
		t.Errorf("From address = %q, want %q", email.Headers.From[0].Address, "alice@example.com")
	}
	if email.Headers.From[0].Name != "Alice" {
		t.Errorf("From name = %q, want %q", email.Headers.From[0].Name, "Alice")
	}

	// To
	if len(email.Headers.To) != 1 {
		t.Fatalf("expected 1 To address, got %d", len(email.Headers.To))
	}
	if email.Headers.To[0].Address != "bob@example.com" {
		t.Errorf("To address = %q, want %q", email.Headers.To[0].Address, "bob@example.com")
	}
	if email.Headers.To[0].Name != "Bob" {
		t.Errorf("To name = %q, want %q", email.Headers.To[0].Name, "Bob")
	}

	// Subject
	if email.Headers.Subject != "Hello" {
		t.Errorf("Subject = %q, want %q", email.Headers.Subject, "Hello")
	}

	// Date
	if email.Headers.Date != "Mon, 01 Jan 2024 12:00:00 +0000" {
		t.Errorf("Date = %q, want %q", email.Headers.Date, "Mon, 01 Jan 2024 12:00:00 +0000")
	}

	// Message-ID
	if email.Headers.MessageID != "<msg001@example.com>" {
		t.Errorf("MessageID = %q, want %q", email.Headers.MessageID, "<msg001@example.com>")
	}

	// Body
	if email.Body.ContentType != "text/plain" {
		t.Errorf("Body ContentType = %q, want %q", email.Body.ContentType, "text/plain")
	}
	if !strings.Contains(email.Body.Content, "This is a plain text email.") {
		t.Errorf("Body Content does not contain expected text, got %q", email.Body.Content)
	}

	// No attachments
	if len(email.Attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(email.Attachments))
	}
	if len(email.Inline) != 0 {
		t.Errorf("expected 0 inline attachments, got %d", len(email.Inline))
	}
}

func TestParse_MultipartAlternative(t *testing.T) {
	msg := "From: sender@example.com\r\n" +
		"To: recipient@example.com\r\n" +
		"Subject: Multipart Test\r\n" +
		"Date: Tue, 02 Jan 2024 10:00:00 +0000\r\n" +
		"Message-ID: <msg002@example.com>\r\n" +
		"Content-Type: multipart/alternative; boundary=\"boundary123\"\r\n" +
		"\r\n" +
		"--boundary123\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Plain text version of the email.\r\n" +
		"--boundary123\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><body><p>HTML version of the email.</p></body></html>\r\n" +
		"--boundary123--\r\n"

	email, err := Parse([]byte(msg))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	// The top-level body should be multipart/alternative
	if email.Body.ContentType != "multipart/alternative" {
		t.Errorf("Body ContentType = %q, want %q", email.Body.ContentType, "multipart/alternative")
	}

	// Should have 2 body parts
	if len(email.Body.Parts) != 2 {
		t.Fatalf("expected 2 body parts, got %d", len(email.Body.Parts))
	}

	// First part: text/plain
	if email.Body.Parts[0].ContentType != "text/plain" {
		t.Errorf("Part[0] ContentType = %q, want %q", email.Body.Parts[0].ContentType, "text/plain")
	}
	if !strings.Contains(email.Body.Parts[0].Content, "Plain text version") {
		t.Errorf("Part[0] Content does not contain expected text, got %q", email.Body.Parts[0].Content)
	}

	// Second part: text/html
	if email.Body.Parts[1].ContentType != "text/html" {
		t.Errorf("Part[1] ContentType = %q, want %q", email.Body.Parts[1].ContentType, "text/html")
	}
	if !strings.Contains(email.Body.Parts[1].Content, "HTML version") {
		t.Errorf("Part[1] Content does not contain expected text, got %q", email.Body.Parts[1].Content)
	}

	// No attachments
	if len(email.Attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(email.Attachments))
	}
}

func TestParse_WithAttachment(t *testing.T) {
	// Create base64-encoded attachment content
	attachmentData := []byte("Hello, this is a test file content.")
	b64Content := base64.StdEncoding.EncodeToString(attachmentData)

	msg := "From: sender@example.com\r\n" +
		"To: recipient@example.com\r\n" +
		"Subject: With Attachment\r\n" +
		"Date: Wed, 03 Jan 2024 08:00:00 +0000\r\n" +
		"Message-ID: <msg003@example.com>\r\n" +
		"Content-Type: multipart/mixed; boundary=\"mixedboundary\"\r\n" +
		"\r\n" +
		"--mixedboundary\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Please see attached file.\r\n" +
		"--mixedboundary\r\n" +
		"Content-Type: application/octet-stream; name=\"test.txt\"\r\n" +
		"Content-Disposition: attachment; filename=\"test.txt\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		b64Content + "\r\n" +
		"--mixedboundary--\r\n"

	email, err := Parse([]byte(msg))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	// Body part should be text/plain
	if len(email.Body.Parts) < 1 {
		t.Fatal("expected at least 1 body part")
	}
	foundText := false
	for _, p := range email.Body.Parts {
		if p.ContentType == "text/plain" && strings.Contains(p.Content, "Please see attached file.") {
			foundText = true
		}
	}
	if !foundText {
		t.Error("did not find expected text/plain body part")
	}

	// Attachment
	if len(email.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(email.Attachments))
	}
	att := email.Attachments[0]
	if att.Filename != "test.txt" {
		t.Errorf("Attachment filename = %q, want %q", att.Filename, "test.txt")
	}
	if att.ContentType != "application/octet-stream" {
		t.Errorf("Attachment content type = %q, want %q", att.ContentType, "application/octet-stream")
	}
	if att.Disposition != "attachment" {
		t.Errorf("Attachment disposition = %q, want %q", att.Disposition, "attachment")
	}
	if att.Size != int64(len(attachmentData)) {
		t.Errorf("Attachment size = %d, want %d", att.Size, len(attachmentData))
	}

	// Verify attachment content can be decoded back
	decoded, err := base64.StdEncoding.DecodeString(att.Content)
	if err != nil {
		t.Fatalf("failed to decode attachment content: %v", err)
	}
	if string(decoded) != string(attachmentData) {
		t.Errorf("decoded attachment content = %q, want %q", string(decoded), string(attachmentData))
	}
}

func TestParse_HeaderDecoding(t *testing.T) {
	// RFC 2047 encoded subject: "Tes\xc3\xa9 Subject" in UTF-8 base64
	// "Test Subject" encoded as UTF-8 base64: =?UTF-8?B?VGVzdCBTdWJqZWN0?=
	// Let's use a subject with non-ASCII characters.
	// "Cafe\xcc\x81" (Cafe with combining acute accent) = "Café"
	encodedSubject := "=?UTF-8?B?Q2Fmw6k=?="

	msg := "From: sender@example.com\r\n" +
		"To: recipient@example.com\r\n" +
		"Subject: " + encodedSubject + "\r\n" +
		"Date: Thu, 04 Jan 2024 14:00:00 +0000\r\n" +
		"Message-ID: <msg004@example.com>\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Body text.\r\n"

	email, err := Parse([]byte(msg))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	// The decoded subject should be "Café"
	expected := "Caf\u00e9"
	if email.Headers.Subject != expected {
		t.Errorf("Subject = %q, want %q", email.Headers.Subject, expected)
	}
}

func TestSerialize_SimplePlainText(t *testing.T) {
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Name: "Alice", Address: "alice@example.com"}},
			To:        []pipeline.Address{{Name: "Bob", Address: "bob@example.com"}},
			Subject:   "Test Subject",
			Date:      "Mon, 01 Jan 2024 12:00:00 +0000",
			MessageID: "<serialize001@example.com>",
		},
		Body: pipeline.Body{
			ContentType: "text/plain; charset=utf-8",
			Content:     "Hello Bob, this is a test email.",
		},
	}

	raw, err := Serialize(email)
	if err != nil {
		t.Fatalf("Serialize returned error: %v", err)
	}

	output := string(raw)

	// Check that expected headers appear
	if !strings.Contains(output, "From:") {
		t.Error("output missing From header")
	}
	if !strings.Contains(output, "alice@example.com") {
		t.Error("output missing alice@example.com in From")
	}
	if !strings.Contains(output, "To:") {
		t.Error("output missing To header")
	}
	if !strings.Contains(output, "bob@example.com") {
		t.Error("output missing bob@example.com in To")
	}
	if !strings.Contains(output, "Subject: Test Subject") {
		t.Error("output missing Subject header")
	}
	if !strings.Contains(output, "Date: Mon, 01 Jan 2024 12:00:00 +0000") {
		t.Error("output missing Date header")
	}
	if !strings.Contains(output, "Message-ID: <serialize001@example.com>") {
		t.Error("output missing Message-ID header")
	}
	if !strings.Contains(output, "MIME-Version: 1.0") {
		t.Error("output missing MIME-Version header")
	}
	if !strings.Contains(output, "Content-Type: text/plain; charset=utf-8") {
		t.Error("output missing Content-Type header")
	}

	// Body should appear after the blank line separator
	if !strings.Contains(output, "Hello Bob, this is a test email.") {
		t.Error("output missing body content")
	}

	// Verify header/body separation (CRLFCRLF)
	if !strings.Contains(output, "\r\n\r\n") {
		t.Error("output missing header/body separator (CRLFCRLF)")
	}
}

func TestSerialize_WithAttachments(t *testing.T) {
	attachmentContent := base64.StdEncoding.EncodeToString([]byte("attachment data"))

	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Address: "sender@example.com"}},
			To:        []pipeline.Address{{Address: "recipient@example.com"}},
			Subject:   "With Attachment",
			Date:      "Fri, 05 Jan 2024 09:00:00 +0000",
			MessageID: "<serialize002@example.com>",
		},
		Body: pipeline.Body{
			ContentType: "text/plain; charset=utf-8",
			Content:     "See attachment.",
		},
		Attachments: []pipeline.Attachment{
			{
				Filename:    "report.pdf",
				ContentType: "application/pdf",
				Size:        int64(len("attachment data")),
				Disposition: "attachment",
				Content:     attachmentContent,
			},
		},
	}

	raw, err := Serialize(email)
	if err != nil {
		t.Fatalf("Serialize returned error: %v", err)
	}

	output := string(raw)

	// Should be multipart/mixed
	if !strings.Contains(output, "Content-Type: multipart/mixed") {
		t.Error("output missing multipart/mixed Content-Type")
	}

	// Should contain boundary markers
	if !strings.Contains(output, "boundary=") {
		t.Error("output missing boundary parameter")
	}

	// Should contain body text
	if !strings.Contains(output, "See attachment.") {
		t.Error("output missing body text")
	}

	// Should contain attachment headers
	if !strings.Contains(output, "Content-Type: application/pdf") {
		t.Error("output missing attachment Content-Type")
	}
	if !strings.Contains(output, "Content-Transfer-Encoding: base64") {
		t.Error("output missing Content-Transfer-Encoding for attachment")
	}
	if !strings.Contains(output, "report.pdf") {
		t.Error("output missing attachment filename")
	}
	if !strings.Contains(output, "Content-Disposition: attachment") {
		t.Error("output missing Content-Disposition for attachment")
	}

	// Should contain the base64 content
	if !strings.Contains(output, attachmentContent) {
		t.Error("output missing base64 attachment content")
	}
}

func TestEnvelopeFromEmail(t *testing.T) {
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From: []pipeline.Address{
				{Name: "Sender", Address: "sender@example.com"},
			},
			To: []pipeline.Address{
				{Name: "Recipient One", Address: "one@example.com"},
				{Name: "Recipient Two", Address: "two@example.com"},
			},
			Cc: []pipeline.Address{
				{Name: "CC User", Address: "cc@example.com"},
			},
		},
	}

	env := EnvelopeFromEmail(email)

	// MailFrom should be the first From address
	if env.MailFrom != "sender@example.com" {
		t.Errorf("MailFrom = %q, want %q", env.MailFrom, "sender@example.com")
	}

	// RcptTo should include all To + Cc addresses
	expectedRcptTo := []string{"one@example.com", "two@example.com", "cc@example.com"}
	if len(env.RcptTo) != len(expectedRcptTo) {
		t.Fatalf("RcptTo length = %d, want %d", len(env.RcptTo), len(expectedRcptTo))
	}
	for i, want := range expectedRcptTo {
		if env.RcptTo[i] != want {
			t.Errorf("RcptTo[%d] = %q, want %q", i, env.RcptTo[i], want)
		}
	}
}

func TestEnvelopeFromEmail_PreservesExistingEnvelope(t *testing.T) {
	// When the envelope already has MailFrom and RcptTo set, they should be preserved.
	email := &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{
			MailFrom: "existing@example.com",
			RcptTo:   []string{"already@example.com"},
		},
		Headers: pipeline.Headers{
			From: []pipeline.Address{
				{Address: "header-from@example.com"},
			},
			To: []pipeline.Address{
				{Address: "header-to@example.com"},
			},
		},
	}

	env := EnvelopeFromEmail(email)

	if env.MailFrom != "existing@example.com" {
		t.Errorf("MailFrom = %q, want %q (should preserve existing)", env.MailFrom, "existing@example.com")
	}
	if len(env.RcptTo) != 1 || env.RcptTo[0] != "already@example.com" {
		t.Errorf("RcptTo = %v, want [already@example.com] (should preserve existing)", env.RcptTo)
	}
}

func TestEstimateSize(t *testing.T) {
	bodyContent := "This is the body of the email message."
	attachmentData := []byte("binary attachment data here")
	b64Attachment := base64.StdEncoding.EncodeToString(attachmentData)

	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From: []pipeline.Address{
				{Name: "Test Sender", Address: "sender@example.com"},
			},
			Subject: "Size Estimation Test",
		},
		Body: pipeline.Body{
			ContentType: "text/plain",
			Content:     bodyContent,
		},
		Attachments: []pipeline.Attachment{
			{
				Filename:    "data.bin",
				ContentType: "application/octet-stream",
				Size:        int64(len(attachmentData)),
				Content:     b64Attachment,
			},
		},
	}

	size := EstimateSize(email)

	if size <= 0 {
		t.Fatalf("EstimateSize returned %d, expected positive value", size)
	}

	// The size should include at minimum the body content length
	if size < int64(len(bodyContent)) {
		t.Errorf("EstimateSize = %d, expected at least body length %d", size, len(bodyContent))
	}

	// The size should include the decoded attachment size (not the base64 size)
	expectedMinSize := int64(len(bodyContent)) + int64(len(attachmentData))
	if size < expectedMinSize {
		t.Errorf("EstimateSize = %d, expected at least %d (body + attachment)", size, expectedMinSize)
	}
}

func TestEstimateSize_WithParts(t *testing.T) {
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:    []pipeline.Address{{Address: "a@b.com"}},
			Subject: "Sub",
		},
		Body: pipeline.Body{
			ContentType: "multipart/alternative",
			Parts: []pipeline.Body{
				{ContentType: "text/plain", Content: "plain text part"},
				{ContentType: "text/html", Content: "<p>html part</p>"},
			},
		},
	}

	size := EstimateSize(email)

	// Should account for both parts
	minExpected := int64(len("plain text part") + len("<p>html part</p>"))
	if size < minExpected {
		t.Errorf("EstimateSize = %d, expected at least %d for both parts", size, minExpected)
	}
}

func TestEstimateSize_AttachmentFallbackToSizeField(t *testing.T) {
	// When attachment Content is empty, EstimateSize should fall back to the Size field.
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:    []pipeline.Address{{Address: "a@b.com"}},
			Subject: "Sub",
		},
		Body: pipeline.Body{
			ContentType: "text/plain",
			Content:     "body",
		},
		Attachments: []pipeline.Attachment{
			{
				Filename:    "big.zip",
				ContentType: "application/zip",
				Size:        1048576, // 1 MB
				Content:     "",      // already extracted, no inline content
			},
		},
	}

	size := EstimateSize(email)

	if size < 1048576 {
		t.Errorf("EstimateSize = %d, expected at least 1048576 (attachment Size field)", size)
	}
}
