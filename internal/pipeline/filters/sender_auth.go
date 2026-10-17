package filters

import (
	"context"
	"strings"

	"github.com/rest-mail/go-dmarc"
	"github.com/restmail/restmail/internal/pipeline"
)

// senderAuthenticated reports whether the message carries a locally-verified,
// DMARC-aligned authentication for domain: an SPF pass of the MAIL FROM
// identity whose domain aligns with domain, or a DKIM pass whose signing (d=)
// domain aligns with domain. It reads only the verdicts spf_check and
// dkim_verify recorded for this run, never Authentication-Results text; with
// none recorded the sender is unauthenticated. Alignment uses the same relaxed
// organizational-domain rule (dmarc.Aligned) the dmarc_check filter applies.
//
// This is the gate an allow/blocklist or contact-whitelist match must pass
// before it may skip spam/greylist scanning. Without it a spoofed sender who
// fully controls the unauthenticated envelope-from (or header From) could reuse
// a victim's allowlist or contact entry — for any domain lacking a strict DMARC
// policy — to bypass scanning entirely (see #177).
func senderAuthenticated(ctx context.Context, domain string) bool {
	domain = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(domain), "@"))
	if domain == "" {
		return false
	}
	auth := pipeline.AuthResultsFromContext(ctx)
	verdicts := append(auth.Find(pipeline.AuthSPF, pipeline.PropMailFrom), auth.Find(pipeline.AuthDKIM, pipeline.PropHeaderD)...)
	for _, r := range verdicts {
		if r.Result == "pass" && r.Domain != "" && dmarc.Aligned(r.Domain, domain) {
			return true
		}
	}
	return false
}
