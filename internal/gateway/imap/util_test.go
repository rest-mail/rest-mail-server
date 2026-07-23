package imap

import (
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

	if !strings.Contains(result, "From: Alice <alice@example.com>\r\n") {
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
