package imap

import (
	"bytes"
	"math"
	"testing"

	imapsrv "github.com/rest-mail/go-imap"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// TestToMessage_ClampsOutOfRangeUID proves the seq/UID mapping uses the same
// clamp as APPENDUID/COPYUID (toUID): a message ID past the 32-bit UID space
// must clamp to 0 rather than silently wrapping via a direct uint32() cast to a
// small, wrong UID that could collide with a real low UID (RFC 3501 §2.3.1.1).
func TestToMessage_ClampsOutOfRangeUID(t *testing.T) {
	if uint64(^uint(0)) <= math.MaxUint32 {
		t.Skip("uint is 32-bit here; no value exceeds the uint32 UID space")
	}
	// Low 32 bits are non-zero (100), so a direct uint32() cast wraps to 100
	// while the clamp yields 0 — the value distinguishes wrap from clamp.
	big := uint(math.MaxUint32) + 101
	msg := toMessage(apiclient.MessageSummary{ID: big})
	if msg.UID != 0 {
		t.Fatalf("toMessage UID = %d, want 0 (out-of-range ID must clamp, not wrap)", msg.UID)
	}
}

// TestNormalizeToCRLF covers the line-ending normalization applied at APPEND
// ingest: bare LF becomes CRLF, existing CRLF is left intact (idempotent), and
// the transform does not double-up CRLF.
func TestNormalizeToCRLF(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare LF", "a\nb\nc", "a\r\nb\r\nc"},
		{"already CRLF", "a\r\nb\r\nc", "a\r\nb\r\nc"},
		{"mixed", "a\r\nb\nc", "a\r\nb\r\nc"},
		{"trailing LF", "a\n", "a\r\n"},
		{"no line endings", "abc", "abc"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(normalizeToCRLF([]byte(c.in))); got != c.want {
				t.Errorf("normalizeToCRLF(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestAppendUID_NormalizesBareLFToCRLF proves an APPEND'd message with bare-LF
// line endings is stored CRLF-normalized. LF-only raw would otherwise break POP3
// RETR/TOP framing (go-pop3 splits on CRLF and does not dot-stuff bare-LF
// lines), risking premature termination of the retrieved message.
func TestAppendUID_NormalizesBareLFToCRLF(t *testing.T) {
	api := newFakeAPI()
	m := newUnitMailbox(t, api)

	raw := "Subject: Hi\nFrom: a@b.test\n\nline one\n.\nline three\n"
	if _, err := m.AppendUID("INBOX", imapsrv.FlagUpdate{}, []byte(raw)); err != nil {
		t.Fatalf("AppendUID: %v", err)
	}

	stored := api.lastDeliver(t).RawMessage
	// No bare LF may remain: every '\n' must be preceded by '\r'.
	for i, b := range stored {
		if b == '\n' && (i == 0 || stored[i-1] != '\r') {
			t.Fatalf("stored raw contains a bare LF at offset %d: %q", i, stored)
		}
	}
	if !bytes.Equal(stored, []byte(normalizeToCRLF([]byte(raw)))) {
		t.Errorf("stored raw = %q, want CRLF-normalized form", stored)
	}
}
