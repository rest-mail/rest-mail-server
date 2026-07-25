package filters

// Item 1 (issue #201): vacation-responder correctness.
//   (a) suppress a reply to ANY Auto-Submitted value other than "no", not just
//       the two exact strings auto-replied / auto-generated;
//   (b) sanitize the Message-ID before it flows into In-Reply-To / References so
//       a CRLF in it cannot inject headers into the outbound reply;
//   (c) suppress replies to SPF/DMARC-failing senders (backscatter) when that
//       signal is present in the message's Authentication-Results.

import (
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

func vacFilter() *vacationFilter {
	return &vacationFilter{cfg: vacationConfig{Days: 7}}
}

func emailWithRaw(name string, values ...string) *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			Raw: map[string][]string{name: values},
		},
	}
}

// ── (a) Auto-Submitted ≠ no ──────────────────────────────────────────

func TestVacationSuppress_AutoSubmittedVariants(t *testing.T) {
	f := vacFilter()
	suppressed := []string{
		"auto-replied",
		"auto-generated",
		"auto-notified",              // previously missed
		"auto-generated (some text)", // parameterized; previously missed
		"AUTO-REPLIED",               // case-insensitive
	}
	for _, v := range suppressed {
		email := emailWithRaw("Auto-Submitted", v)
		if reason := f.shouldSuppress("real@sender.example", email); reason == "" {
			t.Errorf("Auto-Submitted: %q should be suppressed, was not", v)
		}
	}

	// "no" must NOT suppress — it is an ordinary human-originated message.
	email := emailWithRaw("Auto-Submitted", "no")
	if reason := f.shouldSuppress("real@sender.example", email); reason != "" {
		t.Errorf("Auto-Submitted: no should NOT be suppressed, got %q", reason)
	}
}

// ── (b) header-injection sanitization ────────────────────────────────

func TestBuildVacationReply_SanitizesMessageID(t *testing.T) {
	// A Message-ID carrying a CRLF + a forged header (the RESTMAIL req.MessageID
	// path is attacker-influenced). It must not appear as a header in the reply.
	evil := "<abc@x>\r\nBcc: attacker@evil.example"
	reply := buildVacationReply("me@example.com", "them@example.com", "Out of office", "away", evil)

	// The injected text must NOT appear as its own header line (the whole point of
	// the attack). It is harmless folded into the In-Reply-To value.
	for _, line := range strings.Split(reply, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Errorf("CRLF header injection produced a Bcc header line:\n%s", reply)
		}
	}
	// The value that reached In-Reply-To must carry no raw CR/LF.
	if strings.ContainsAny(sanitizeHeaderValue(evil), "\r\n") {
		t.Error("sanitizeHeaderValue left CR/LF in the value")
	}
}

// ── (c) SPF/DMARC-failing sender → backscatter suppression ───────────

func TestVacationSuppress_AuthFailingSender(t *testing.T) {
	f := vacFilter()

	cases := map[string]bool{ // authResults → wantSuppressed
		"spf=fail (bad) smtp.mailfrom=x@y": true,
		"spf=softfail smtp.mailfrom=x@y":   true,
		"dmarc=fail policy=reject":         true,
		"spf=pass; dkim=pass; dmarc=pass":  false,
		"spf=none":                         false,
	}
	for ar, want := range cases {
		email := emailWithRaw("Authentication-Results", ar)
		got := f.shouldSuppress("real@sender.example", email) != ""
		if got != want {
			t.Errorf("AR %q: suppressed=%v, want %v", ar, got, want)
		}
	}
}
