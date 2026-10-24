package filters

// Item 1 (issue #201): vacation-responder correctness.
//   (a) suppress a reply to ANY Auto-Submitted value other than "no", not just
//       the two exact strings auto-replied / auto-generated;
//   (b) sanitize the Message-ID before it flows into In-Reply-To / References so
//       a CRLF in it cannot inject headers into the outbound reply;
//   (c) suppress replies to SPF/DMARC-failing senders (backscatter) when
//       spf_check or dmarc_check recorded that verdict.

import (
	"context"
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
		if reason := f.shouldSuppress(context.Background(), "real@sender.example", email); reason == "" {
			t.Errorf("Auto-Submitted: %q should be suppressed, was not", v)
		}
	}

	// "no" must NOT suppress — it is an ordinary human-originated message.
	email := emailWithRaw("Auto-Submitted", "no")
	if reason := f.shouldSuppress(context.Background(), "real@sender.example", email); reason != "" {
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

	spfMailFrom := func(result string) pipeline.AuthResult {
		return pipeline.AuthResult{Method: pipeline.AuthSPF, Result: result, Property: pipeline.PropMailFrom, Domain: "y.example"}
	}
	spfHelo := func(result string) pipeline.AuthResult {
		return pipeline.AuthResult{Method: pipeline.AuthSPF, Result: result, Property: pipeline.PropHelo, Domain: "mx.y.example"}
	}
	dkim := pipeline.AuthResult{Method: pipeline.AuthDKIM, Result: "pass", Property: pipeline.PropHeaderD, Domain: "y.example"}
	dmarc := func(result string) pipeline.AuthResult {
		return pipeline.AuthResult{Method: pipeline.AuthDMARC, Result: result, Property: pipeline.PropHeaderFrom, Domain: "y.example"}
	}
	cases := []struct {
		name     string
		verdicts []pipeline.AuthResult
		want     bool // suppressed
	}{
		{"spf fail", []pipeline.AuthResult{spfMailFrom("fail")}, true},
		{"spf softfail", []pipeline.AuthResult{spfMailFrom("softfail")}, true},
		{"helo fail", []pipeline.AuthResult{spfHelo("fail"), spfMailFrom("pass")}, true},
		{"dmarc fail", []pipeline.AuthResult{spfMailFrom("pass"), dmarc("fail")}, true},
		{"all pass", []pipeline.AuthResult{spfMailFrom("pass"), dkim, dmarc("pass")}, false},
		{"spf none", []pipeline.AuthResult{spfMailFrom("none")}, false},
		{"no verdicts", nil, false},
	}
	for _, c := range cases {
		got := f.shouldSuppress(authCtx(c.verdicts...), "real@sender.example", &pipeline.EmailJSON{}) != ""
		if got != c.want {
			t.Errorf("%s: suppressed=%v, want %v", c.name, got, c.want)
		}
	}

	// A failure written only as Authentication-Results text, by the sender or a
	// script, is not a verdict: the reply is not held for it.
	email := emailWithRaw("Authentication-Results", "restmail; spf=fail smtp.mailfrom=y.example; dmarc=fail")
	if reason := f.shouldSuppress(authCtx(spfMailFrom("pass")), "real@sender.example", email); reason != "" {
		t.Errorf("Authentication-Results text suppressed the reply: %s", reason)
	}
}

// ── #716: the envelope sender only ───────────────────────────────────

// A bounce comes with a null reverse path and an ordinary From:, and gets no
// reply (RFC 3834 §2, RFC 5230 §4.2). The filter has no database: reaching a
// lookup would panic, so the refusal comes first.
func TestVacation_NullEnvelopeSenderGetsNoReply(t *testing.T) {
	for _, mailFrom := range []string{"", "<>", "  "} {
		email := &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{MailFrom: mailFrom, RcptTo: []string{"dana@hosted.test"}},
			Headers:  pipeline.Headers{From: []pipeline.Address{{Address: "postmaster@example.org"}}},
		}
		res, err := vacFilter().Execute(context.Background(), email)
		if err != nil {
			t.Fatalf("MAIL FROM %q: %v", mailFrom, err)
		}
		if res.Log.Result != "skip" || res.Log.Detail != "empty or null sender" {
			t.Errorf("MAIL FROM %q: %+v, want skipped for a null sender", mailFrom, res.Log)
		}
	}
}
