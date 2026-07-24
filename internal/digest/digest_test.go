package digest

import (
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// TestRenderDigestEscapesHTML verifies that attacker-controlled message fields
// (sender, subject, quarantine reason) interpolated into the digest HTML body
// are HTML-escaped, so injected markup cannot execute in any HTML-rendering
// consumer (IMAP client, future UI). This is the stored-XSS regression guard.
func TestRenderDigestEscapesHTML(t *testing.T) {
	received := time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)
	items := []models.Quarantine{
		{
			Sender:           `"><img src=x onerror=alert(1)>`,
			Subject:          `<script>alert('xss')</script>`,
			QuarantineReason: `<b>spam</b>`,
			BodyPreview:      "hello",
			ReceivedAt:       received,
		},
	}

	_, bodyHTML := renderDigest("owner@example.com", received, items)

	// The raw attacker markup must NOT appear verbatim in the HTML body.
	rawPayloads := []string{
		`<script>alert('xss')</script>`,
		`<img src=x onerror=alert(1)>`,
		`"><img`,
		`<b>spam</b>`,
	}
	for _, p := range rawPayloads {
		if strings.Contains(bodyHTML, p) {
			t.Errorf("digest HTML contains unescaped payload %q\n---\n%s", p, bodyHTML)
		}
	}

	// The escaped forms must be present, proving the values were rendered
	// (escaped) rather than dropped.
	escapedForms := []string{
		"&lt;script&gt;",
		"&lt;img",
		"&lt;b&gt;spam&lt;/b&gt;",
	}
	for _, e := range escapedForms {
		if !strings.Contains(bodyHTML, e) {
			t.Errorf("digest HTML missing expected escaped form %q\n---\n%s", e, bodyHTML)
		}
	}
}

// TestRenderDigestStripsPlaintextNewlines verifies that CR/LF characters in
// attacker-controlled fields are stripped from the plain-text body, so an
// attacker cannot inject newlines to forge additional digest entries.
func TestRenderDigestStripsPlaintextNewlines(t *testing.T) {
	received := time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)
	items := []models.Quarantine{
		{
			Sender:           "attacker@evil.test",
			Subject:          "real subject\r\n2. From: forged@evil.test\r\n   Subject: FORGED ENTRY",
			QuarantineReason: "spam",
			ReceivedAt:       received,
		},
	}

	bodyText, _ := renderDigest("owner@example.com", received, items)

	// The forged "2. From:" must not appear on its own line — the CR/LF that
	// would have started a new (forged) entry must be stripped.
	if strings.Contains(bodyText, "\n2. From:") {
		t.Errorf("plain-text body contains newline-injected forged entry:\n%s", bodyText)
	}
	// No raw CR/LF from the attacker field should survive in the body: every
	// legitimate line ends in "\n" and none of the injected control chars leak.
	if strings.Contains(bodyText, "\r") {
		t.Errorf("plain-text body contains a raw CR from an attacker field:\n%s", bodyText)
	}
	// The sanitized subject stays on a single "Subject:" line.
	if !strings.Contains(bodyText, "   Subject: real subject  2. From: forged@evil.test     Subject: FORGED ENTRY\n") {
		t.Errorf("plain-text subject was not collapsed onto one line:\n%s", bodyText)
	}
}
