package pop3

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// storedRawWithAttachment is a pristine multipart/mixed message carrying a file
// attachment, To/Cc, a custom header and a DKIM-Signature — exactly the sort of
// content the old field-by-field rebuild discarded.
const storedRawWithAttachment = "DKIM-Signature: v=1; a=rsa-sha256; d=example.com; s=sel; bh=abc; b=def\r\n" +
	"From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>\r\n" +
	"Cc: Carol <carol@example.com>\r\n" +
	"Subject: Report\r\n" +
	"X-Custom-Header: keep-me\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"See attached.\r\n" +
	"--BOUND\r\n" +
	"Content-Type: application/pdf; name=\"report.pdf\"\r\n" +
	"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"JVBERi0xLjQKJcc= \r\n" +
	"--BOUND--\r\n"

// newRawTestMailbox wires a mailbox to an httptest server that serves the given
// raw body (verbatim) at /api/v1/messages/{id}/raw, or 404 when raw is empty.
func newRawTestMailbox(t *testing.T, raw string) *mailbox {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if raw == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "message/rfc822")
		_, _ = w.Write([]byte(raw))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &mailbox{api: apiclient.New(srv.URL), token: "tok"}
}

func sampleDetail(id uint) apiclient.MessageDetail {
	return apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			ID:         id,
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Report",
			ReceivedAt: time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC),
			MessageID:  "abc@example.com",
		},
		BodyText: "See attached.",
	}
}

// TestRawMessage_ServesStoredVerbatim proves RETR/TOP serve the stored original
// byte-for-byte, preserving the attachment, To/Cc, custom header and
// DKIM-Signature that buildRawMessage would otherwise drop.
func TestRawMessage_ServesStoredVerbatim(t *testing.T) {
	m := newRawTestMailbox(t, storedRawWithAttachment)

	got := m.rawMessage(sampleDetail(42))

	if got != storedRawWithAttachment {
		t.Fatalf("stored raw not served verbatim:\n got %q\nwant %q", got, storedRawWithAttachment)
	}
	for _, needle := range []string{
		"DKIM-Signature: v=1;",
		"To: Bob <bob@example.com>",
		"Cc: Carol <carol@example.com>",
		"X-Custom-Header: keep-me",
		"Content-Disposition: attachment; filename=\"report.pdf\"",
		"JVBERi0xLjQKJcc=",
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("verbatim raw missing %q", needle)
		}
	}
	if strings.Contains(got, "=_restmail_") {
		t.Errorf("served message looks rebuilt, not verbatim: %s", got)
	}
}

// TestRawMessage_FallsBackWhenNoStoredRaw proves that when the server has no
// stored raw (404), rawMessage falls back to the buildRawMessage reconstruction.
func TestRawMessage_FallsBackWhenNoStoredRaw(t *testing.T) {
	m := newRawTestMailbox(t, "") // 404 -> no stored raw

	detail := sampleDetail(99)
	got := m.rawMessage(detail)

	want := buildRawMessage(detail)
	if got != want {
		t.Fatalf("fallback mismatch:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "report.pdf") {
		t.Errorf("fallback unexpectedly contains an attachment: %s", got)
	}
	if !strings.Contains(got, "From: Alice <alice@example.com>\r\n") {
		t.Errorf("fallback missing reconstructed From header: %s", got)
	}
}

// TestMessages_SizePrefersRawSizeWithSizeBytesFallback proves the maildrop
// listing (which drives POP3 STAT/LIST octet counts) reports the exact
// stored-raw size when the server recorded one, and falls back to the legacy
// size_bytes for messages without a stored raw.
func TestMessages_SizePrefersRawSizeWithSizeBytesFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/accounts/3/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The API pages newest-first; the client reverses to oldest-first.
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": 2, "subject": "no raw", "size_bytes": 44},
				{"id": 1, "subject": "with raw", "size_bytes": 10, "raw_size": 3210}
			],
			"pagination": {"cursor": "", "has_more": false, "total": 2}
		}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	m := &mailbox{api: apiclient.New(srv.URL), token: "tok", accountID: 3}
	msgs, err := m.Messages()
	if err != nil {
		t.Fatalf("Messages() error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Size != 3210 {
		t.Errorf("message 1 Size = %d, want 3210 (raw_size must win over size_bytes)", msgs[0].Size)
	}
	if msgs[1].Size != 44 {
		t.Errorf("message 2 Size = %d, want 44 (fallback to size_bytes when raw_size absent)", msgs[1].Size)
	}
}
