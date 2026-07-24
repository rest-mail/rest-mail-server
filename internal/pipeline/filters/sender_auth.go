package filters

import (
	"strings"

	"github.com/rest-mail/go-dmarc"
	"github.com/restmail/restmail/internal/pipeline"
)

// collectLocalAuthResults returns the Authentication-Results this server's own
// spf_check / dkim filters wrote for the message, concatenated. It reads only
// the Extra and Raw["Authentication-Results"] slots those filters populate —
// never an inbound-supplied header, which the MIME ingress rehomes to
// X-Original-Authentication-Results before any filter runs (see #162). With no
// local verdict the result is empty, which every caller treats as
// "unauthenticated".
func collectLocalAuthResults(email *pipeline.EmailJSON) string {
	authResults := ""
	if email.Headers.Extra != nil {
		authResults = email.Headers.Extra["Authentication-Results"]
	}
	if email.Headers.Raw != nil {
		for _, ar := range email.Headers.Raw["Authentication-Results"] {
			if authResults != "" {
				authResults += "; "
			}
			authResults += ar
		}
	}
	return authResults
}

// senderAuthenticated reports whether the message carries a locally-verified,
// DMARC-aligned authentication for domain: an SPF pass whose smtp.mailfrom
// domain aligns with domain, or a DKIM pass whose signing (header.d=) domain
// aligns with domain. Alignment uses the same relaxed organizational-domain rule
// (dmarc.Aligned) the dmarc_check filter applies.
//
// This is the gate an allow/blocklist or contact-whitelist match must pass
// before it may skip spam/greylist scanning. Without it a spoofed sender who
// fully controls the unauthenticated envelope-from (or header From) could reuse
// a victim's allowlist or contact entry — for any domain lacking a strict DMARC
// policy — to bypass scanning entirely (see #177).
func senderAuthenticated(email *pipeline.EmailJSON, domain string) bool {
	domain = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(domain), "@"))
	if domain == "" {
		return false
	}
	ar := collectLocalAuthResults(email)
	if strings.Contains(ar, "spf=pass") {
		if d := extractAuthDomain(ar, "smtp.mailfrom="); d != "" && dmarc.Aligned(d, domain) {
			return true
		}
	}
	if strings.Contains(ar, "dkim=pass") {
		if d := extractAuthDomain(ar, "header.d="); d != "" && dmarc.Aligned(d, domain) {
			return true
		}
	}
	return false
}
