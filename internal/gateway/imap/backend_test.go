package imap

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

// TestRawMessage_ServesStoredVerbatim proves that when a stored raw exists it is
// returned byte-for-byte, preserving the attachment, To/Cc, custom header and
// DKIM-Signature that buildRawMessage would otherwise drop.
func TestRawMessage_ServesStoredVerbatim(t *testing.T) {
	m := newRawTestMailbox(t, storedRawWithAttachment)

	got := m.rawMessage(sampleDetail(42))

	if got != storedRawWithAttachment {
		t.Fatalf("stored raw not served verbatim:\n got %q\nwant %q", got, storedRawWithAttachment)
	}
	// Spot-check the pieces the lossy rebuild used to discard.
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
	// The rebuild would have injected a synthetic boundary; the stored original
	// must not be rebuilt.
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
	// The reconstruction cannot carry the attachment or DKIM-Signature.
	if strings.Contains(got, "report.pdf") {
		t.Errorf("fallback unexpectedly contains an attachment: %s", got)
	}
	if !strings.Contains(got, "From: Alice <alice@example.com>\r\n") {
		t.Errorf("fallback missing reconstructed From header: %s", got)
	}
}

// TestToMessage_SizePrefersRawSize proves RFC822.SIZE reports the exact octet
// count of the stored raw message when the server recorded one — RFC 3501
// requires the size to match the transmitted bytes exactly, and size_bytes is
// a quota heuristic that does not.
func TestToMessage_SizePrefersRawSize(t *testing.T) {
	msg := toMessage(apiclient.MessageSummary{ID: 7, SizeBytes: 55, RawSize: len(storedRawWithAttachment)})
	if msg.Size != len(storedRawWithAttachment) {
		t.Fatalf("Size = %d, want %d (raw_size must win over size_bytes)", msg.Size, len(storedRawWithAttachment))
	}
}

// TestToMessage_SizeFallsBackToSizeBytes proves messages the server has no
// stored raw for (raw_size 0, e.g. locally-composed items served via the
// rebuilt fallback) keep reporting the legacy size_bytes value.
func TestToMessage_SizeFallsBackToSizeBytes(t *testing.T) {
	msg := toMessage(apiclient.MessageSummary{ID: 8, SizeBytes: 55})
	if msg.Size != 55 {
		t.Fatalf("Size = %d, want 55 (fallback to size_bytes when raw_size absent)", msg.Size)
	}
}
