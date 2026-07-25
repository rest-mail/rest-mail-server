package handlers

import (
	"strings"
	"testing"

	rmime "github.com/restmail/restmail/internal/mime"
)

// TestBuildInboundEmail_PopulatesRawHeaders proves the RESTMAIL inbound path
// builds its pipeline message WITH the real headers (Headers.Raw) parsed from
// the raw message, not a header-blind message reconstructed from a few
// structured fields.
//
// Regression guard for #187: Headers.Raw was never populated on this path, so
// header-dependent filters (arc_verify, header_validate, dmarc_check's local
// Authentication-Results, vacation's RFC 3834 auto-reply suppression) saw an
// empty header set — the exact headers those filters key off (DKIM-Signature,
// Authentication-Results, Auto-Submitted, ...) were invisible. It also asserts
// the envelope carries the connecting peer's client IP so spf_check/dmarc_check
// have a genuine local verdict rather than "none".
func TestBuildInboundEmail_PopulatesRawHeaders(t *testing.T) {
	raw := "From: sender@remote.test\r\n" +
		"To: user@local.test\r\n" +
		"Subject: Hi\r\n" +
		"Message-Id: <abc@remote.test>\r\n" +
		"DKIM-Signature: v=1; a=rsa-sha256; d=remote.test; s=sel; bh=x; b=y\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"\r\n" +
		"hello body\r\n"

	parsed, err := rmime.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse raw: %v", err)
	}

	email := buildInboundEmail(
		parsed,
		"sender@remote.test",
		[]string{"user@local.test"},
		"Hi", "", "<abc@remote.test>",
		"hello body", "",
		raw,
		"203.0.113.9",
	)

	if len(email.Headers.Raw) == 0 {
		t.Fatalf("Headers.Raw is empty: header-dependent filters (DKIM/DMARC/SPF, arc_verify, header_validate, vacation) cannot see the real headers")
	}

	// The specific headers those filters key off must be present (matched
	// case-insensitively — go-message stores them under canonical MIME keys, e.g.
	// "Dkim-Signature").
	for _, key := range []string{"DKIM-Signature", "Auto-Submitted"} {
		if !hasRawHeader(email.Headers.Raw, key) {
			t.Errorf("Headers.Raw missing %q; got keys %v", key, rawKeys(email.Headers.Raw))
		}
	}

	// The envelope must carry the connecting RESTMAIL peer's IP so spf_check /
	// dmarc_check can produce a genuine local verdict.
	if email.Envelope.ClientIP != "203.0.113.9" {
		t.Errorf("Envelope.ClientIP = %q, want %q", email.Envelope.ClientIP, "203.0.113.9")
	}

	// The envelope stays inbound and threads the raw source for DKIM canonicalization.
	if email.Envelope.Direction != "inbound" {
		t.Errorf("Envelope.Direction = %q, want inbound", email.Envelope.Direction)
	}
	if email.Metadata["raw_message"] != raw {
		t.Errorf("Metadata[raw_message] not threaded through for DKIM verification")
	}
}

// TestBuildInboundEmail_FallsBackWhenNoRaw covers the no-parseable-raw path:
// with parsed == nil the message is built from the structured request fields.
func TestBuildInboundEmail_FallsBackWhenNoRaw(t *testing.T) {
	email := buildInboundEmail(
		nil,
		"sender@remote.test",
		[]string{"user@local.test"},
		"Hi", "", "<abc@remote.test>",
		"hello body", "",
		"",
		"203.0.113.9",
	)
	if len(email.Headers.From) == 0 || email.Headers.From[0].Address != "sender@remote.test" {
		t.Errorf("From not populated from structured fields: %+v", email.Headers.From)
	}
	if len(email.Headers.To) == 0 || email.Headers.To[0].Address != "user@local.test" {
		t.Errorf("To not populated from structured fields: %+v", email.Headers.To)
	}
	if email.Headers.Subject != "Hi" {
		t.Errorf("Subject = %q, want Hi", email.Headers.Subject)
	}
	if email.Envelope.ClientIP != "203.0.113.9" {
		t.Errorf("Envelope.ClientIP = %q, want 203.0.113.9", email.Envelope.ClientIP)
	}
}

func rawKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func hasRawHeader(m map[string][]string, key string) bool {
	for k, v := range m {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return true
		}
	}
	return false
}
