package pop3

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// ── parseCommand ─────────────────────────────────────────────────────

func TestParseCommand_UserCommand(t *testing.T) {
	cmd, arg := parseCommand("USER foo")
	if cmd != "USER" {
		t.Errorf("expected cmd USER, got %q", cmd)
	}
	if arg != "foo" {
		t.Errorf("expected arg foo, got %q", arg)
	}
}

func TestParseCommand_PassCommand(t *testing.T) {
	cmd, arg := parseCommand("PASS bar")
	if cmd != "PASS" {
		t.Errorf("expected cmd PASS, got %q", cmd)
	}
	if arg != "bar" {
		t.Errorf("expected arg bar, got %q", arg)
	}
}

func TestParseCommand_QuitNoArg(t *testing.T) {
	cmd, arg := parseCommand("QUIT")
	if cmd != "QUIT" {
		t.Errorf("expected cmd QUIT, got %q", cmd)
	}
	if arg != "" {
		t.Errorf("expected empty arg, got %q", arg)
	}
}

func TestParseCommand_EmptyString(t *testing.T) {
	cmd, arg := parseCommand("")
	if cmd != "" {
		t.Errorf("expected empty cmd, got %q", cmd)
	}
	if arg != "" {
		t.Errorf("expected empty arg, got %q", arg)
	}
}

func TestParseCommand_StatNoArg(t *testing.T) {
	cmd, arg := parseCommand("STAT")
	if cmd != "STAT" {
		t.Errorf("expected cmd STAT, got %q", cmd)
	}
	if arg != "" {
		t.Errorf("expected empty arg, got %q", arg)
	}
}

func TestParseCommand_RetrWithNumber(t *testing.T) {
	cmd, arg := parseCommand("RETR 1")
	if cmd != "RETR" {
		t.Errorf("expected cmd RETR, got %q", cmd)
	}
	if arg != "1" {
		t.Errorf("expected arg 1, got %q", arg)
	}
}

func TestParseCommand_CaseInsensitivity(t *testing.T) {
	cmd, arg := parseCommand("user alice")
	if cmd != "USER" {
		t.Errorf("expected cmd USER (uppercased), got %q", cmd)
	}
	if arg != "alice" {
		t.Errorf("expected arg alice, got %q", arg)
	}
}

func TestParseCommand_MixedCase(t *testing.T) {
	cmd, arg := parseCommand("rEtR 5")
	if cmd != "RETR" {
		t.Errorf("expected cmd RETR (uppercased), got %q", cmd)
	}
	if arg != "5" {
		t.Errorf("expected arg 5, got %q", arg)
	}
}

func TestParseCommand_ArgWithSpaces(t *testing.T) {
	cmd, arg := parseCommand("PASS my secret password")
	if cmd != "PASS" {
		t.Errorf("expected cmd PASS, got %q", cmd)
	}
	if arg != "my secret password" {
		t.Errorf("expected arg 'my secret password', got %q", arg)
	}
}

// ── buildRawMessage ──────────────────────────────────────────────────

func newTestTime() time.Time {
	return time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)
}

func TestBuildRawMessage_TextOnly(t *testing.T) {
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Hello",
			ReceivedAt: newTestTime(),
		},
		BodyText: "Hello, world!",
	}

	raw := buildRawMessage(msg)

	assertContains(t, raw, "From: Alice <alice@example.com>\r\n")
	assertContains(t, raw, "Subject: Hello\r\n")
	assertContains(t, raw, "MIME-Version: 1.0\r\n")
	assertContains(t, raw, "Content-Type: text/plain; charset=utf-8\r\n")
	assertContains(t, raw, "Hello, world!")

	// Should NOT be multipart
	if strings.Contains(raw, "multipart") {
		t.Error("text-only message should not contain multipart")
	}
}

func TestBuildRawMessage_HTMLOnly(t *testing.T) {
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "bob@example.com",
			SenderName: "Bob",
			Subject:    "HTML Test",
			ReceivedAt: newTestTime(),
		},
		BodyHTML: "<p>Hello</p>",
	}

	raw := buildRawMessage(msg)

	assertContains(t, raw, "From: Bob <bob@example.com>\r\n")
	assertContains(t, raw, "Content-Type: text/html; charset=utf-8\r\n")
	assertContains(t, raw, "<p>Hello</p>")

	if strings.Contains(raw, "multipart") {
		t.Error("html-only message should not contain multipart")
	}
}

func TestBuildRawMessage_MultipartTextAndHTML(t *testing.T) {
	ts := newTestTime()
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "carol@example.com",
			SenderName: "Carol",
			Subject:    "Both",
			ReceivedAt: ts,
		},
		BodyText: "Plain text",
		BodyHTML: "<p>Rich text</p>",
	}

	raw := buildRawMessage(msg)

	boundary := fmt.Sprintf("=_restmail_%d", ts.UnixNano())
	assertContains(t, raw, fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"", boundary))
	assertContains(t, raw, "--"+boundary+"\r\n")
	assertContains(t, raw, "Content-Type: text/plain; charset=utf-8\r\n")
	assertContains(t, raw, "Plain text")
	assertContains(t, raw, "Content-Type: text/html; charset=utf-8\r\n")
	assertContains(t, raw, "<p>Rich text</p>")
	assertContains(t, raw, "--"+boundary+"--\r\n")
}

func TestBuildRawMessage_WithMessageID(t *testing.T) {
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "dave@example.com",
			SenderName: "Dave",
			Subject:    "ID Test",
			ReceivedAt: newTestTime(),
			MessageID:  "abc123@example.com",
		},
		BodyText: "body",
	}

	raw := buildRawMessage(msg)
	assertContains(t, raw, "Message-ID: <abc123@example.com>\r\n")
}

func TestBuildRawMessage_WithoutMessageID(t *testing.T) {
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "eve@example.com",
			SenderName: "Eve",
			Subject:    "No ID",
			ReceivedAt: newTestTime(),
		},
		BodyText: "body",
	}

	raw := buildRawMessage(msg)
	if strings.Contains(raw, "Message-ID:") {
		t.Error("message without MessageID should not have Message-ID header")
	}
}

func TestBuildRawMessage_WithInReplyTo(t *testing.T) {
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "frank@example.com",
			SenderName: "Frank",
			Subject:    "Re: Thread",
			ReceivedAt: newTestTime(),
		},
		BodyText:  "reply body",
		InReplyTo: "original123@example.com",
	}

	raw := buildRawMessage(msg)
	assertContains(t, raw, "In-Reply-To: <original123@example.com>\r\n")
}

func TestBuildRawMessage_WithoutInReplyTo(t *testing.T) {
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "grace@example.com",
			SenderName: "Grace",
			Subject:    "New Thread",
			ReceivedAt: newTestTime(),
		},
		BodyText: "first message",
	}

	raw := buildRawMessage(msg)
	if strings.Contains(raw, "In-Reply-To:") {
		t.Error("message without InReplyTo should not have In-Reply-To header")
	}
}

func TestBuildRawMessage_DateFormat(t *testing.T) {
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "hank@example.com",
			SenderName: "Hank",
			Subject:    "Date Check",
			ReceivedAt: newTestTime(),
		},
		BodyText: "body",
	}

	raw := buildRawMessage(msg)
	assertContains(t, raw, "Date: Sat, 15 Mar 2025 10:30:00 +0000\r\n")
}

// ── helper ───────────────────────────────────────────────────────────

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q, but it did not.\nFull output:\n%s", needle, haystack)
	}
}
