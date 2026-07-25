package filters

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rest-mail/go-dmarc"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

// captureDMARC records this message's DMARC evaluation for later RFC 7489
// aggregate (rua) reporting. Only inbound mail whose From domain publishes a
// DMARC record is recorded; the reporter aggregates these rows per period. A DB
// handle is available on the pipeline context; absent one (e.g. the test/preview
// path), capture is a no-op.
func captureDMARC(ctx context.Context, email *pipeline.EmailJSON, domain, policy string, spfPass, spfAligned, dkimPass, dkimAligned bool, disposition string) {
	db := pipeline.DBFromContext(ctx)
	if db == nil || email.Envelope.Direction == "outbound" {
		return
	}
	result := func(pass bool) string {
		if pass {
			return "pass"
		}
		return "fail"
	}
	headerFrom := ""
	if len(email.Headers.From) > 0 {
		headerFrom = email.Headers.From[0].Address
	}
	_ = db.Create(&models.DMARCAggregateRecord{
		Domain:      domain,
		SourceIP:    email.Envelope.ClientIP,
		HeaderFrom:  headerFrom,
		Disposition: disposition,
		Policy:      policy,
		DKIMResult:  result(dkimPass),
		DKIMAligned: dkimAligned,
		SPFResult:   result(spfPass),
		SPFAligned:  spfAligned,
	}).Error
}

// dmarcCheckFilter evaluates DMARC policy using SPF and DKIM results.
type dmarcCheckFilter struct {
	// trustedSealers is the set of ARC sealing domains (lower-cased) whose
	// passing ARC chain is permitted to override a DMARC failure. Empty by
	// default, which keeps ARC purely informational: no sealer is trusted, so an
	// ARC "pass" never rescues a DMARC reject/quarantine (see #178). Bound to the
	// deployment's allowlist in routes.go via NewDMARCCheckWithSealers.
	trustedSealers map[string]struct{}
}

func init() {
	pipeline.DefaultRegistry.Register("dmarc_check", NewDMARCCheck)
}

func NewDMARCCheck(_ []byte) (pipeline.Filter, error) {
	return &dmarcCheckFilter{}, nil
}

// NewDMARCCheckWithSealers returns a factory that binds the trusted-ARC-sealer
// allowlist to the dmarc_check filter. Only a passing ARC chain whose most
// recent ARC-Seal was signed by one of these domains (its d= tag) may override a
// DMARC failure (RFC 8617 makes ARC meaningful only when it comes from a sealer
// you trust). An empty list leaves ARC informational — the secure default.
func NewDMARCCheckWithSealers(sealers []string) pipeline.FilterFactory {
	set := make(map[string]struct{}, len(sealers))
	for _, s := range sealers {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			set[s] = struct{}{}
		}
	}
	return func(_ []byte) (pipeline.Filter, error) {
		return &dmarcCheckFilter{trustedSealers: set}, nil
	}
}

// sealerTrusted reports whether the given ARC sealing domain is in the
// configured trusted-sealer allowlist. An empty domain or an empty allowlist is
// never trusted, so ARC stays informational by default.
func (f *dmarcCheckFilter) sealerTrusted(sealer string) bool {
	if sealer == "" || len(f.trustedSealers) == 0 {
		return false
	}
	_, ok := f.trustedSealers[strings.ToLower(strings.TrimSpace(sealer))]
	return ok
}

func (f *dmarcCheckFilter) Name() string              { return "dmarc_check" }
func (f *dmarcCheckFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *dmarcCheckFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	// Get the From domain
	if len(email.Headers.From) == 0 {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "none",
				Detail: "no From header",
			},
		}, nil
	}

	fromAddr := email.Headers.From[0].Address
	parts := strings.SplitN(fromAddr, "@", 2)
	if len(parts) != 2 {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "none",
				Detail: "invalid From address",
			},
		}, nil
	}
	domain := parts[1]

	// Look up DMARC record
	dmarcRecord, err := lookupDMARC(domain)
	if err != nil || dmarcRecord == "" {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "none",
				Detail: fmt.Sprintf("no DMARC record for %s", domain),
			},
		}, nil
	}

	// Parse DMARC policy. A malformed record (bad/duplicate p= tag) has no valid
	// policy: per RFC 7489 §6.6.3 treat it as if none were published — i.e. no
	// enforcement — rather than applying an unintended disposition.
	policy, err := dmarc.ParsePolicy(dmarcRecord)
	if err != nil {
		slog.Warn("dmarc_check: malformed DMARC record, treating as no policy",
			"domain", domain, "error", err)
		policy = "none"
	}

	// Check Authentication-Results from both Extra and Raw maps
	authResults := ""
	if email.Headers.Extra != nil {
		authResults = email.Headers.Extra["Authentication-Results"]
	}
	// Also check Raw headers for results added by SPF/DKIM filters
	if email.Headers.Raw != nil {
		for _, ar := range email.Headers.Raw["Authentication-Results"] {
			if authResults != "" {
				authResults += "; "
			}
			authResults += ar
		}
	}

	spfPass := strings.Contains(authResults, "spf=pass")
	dkimPass := strings.Contains(authResults, "dkim=pass")

	// Extract SPF authenticated domain from auth-results
	spfAligned := false
	if spfPass {
		// Look for smtp.mailfrom= in auth results
		spfDomain := extractAuthDomain(authResults, "smtp.mailfrom=")
		if spfDomain != "" {
			spfAligned = dmarc.Aligned(spfDomain, domain)
		}
	}

	// DMARC DKIM alignment: the signature's d= domain (recorded as header.d= in
	// Authentication-Results by dkim_verify) must match — or organizationally
	// align with — the From domain. A DKIM pass with no identifiable signing
	// domain does NOT establish alignment: assuming it would let a signature
	// valid for an unrelated domain satisfy DMARC for this one. The real verifier
	// always emits header.d= on pass, so this never rejects legitimately-aligned
	// mail; it only removes the old stub-era "assume aligned" shortcut.
	dkimAligned := false
	if dkimPass {
		if dkimDomain := extractAuthDomain(authResults, "header.d="); dkimDomain != "" {
			dkimAligned = dmarc.Aligned(dkimDomain, domain)
		}
	}

	// ARC override status: a valid ARC chain lets us honor the original
	// authentication on forwarded mail (RFC 8617 §5.2). The verdict and the
	// sealing domain are taken ONLY from the arc_status / arc_sealer metadata
	// written by the local arc_verify filter — never from an "arc=pass" substring
	// in Authentication-Results, which on an inbound message is attacker-controlled
	// and was a second DMARC-bypass vector (any message merely containing
	// "arc=pass" would trigger the override even when arc_verify never ran).
	arcStatus := ""
	arcSealer := ""
	if email.Metadata != nil {
		arcStatus = email.Metadata["arc_status"]
		arcSealer = email.Metadata["arc_sealer"]
	}

	// An ARC "pass" may override a DMARC failure ONLY when the sealing domain (the
	// d= of the most recent ARC-Seal, recorded by arc_verify) is in the configured
	// trusted-sealer allowlist. RFC 8617 makes ARC meaningful only when it comes
	// from a sealer you trust: without this gate any attacker who runs their own
	// ARC sealer could seal spoofed mail and launder it past the From domain's
	// p=reject/quarantine (#178). The allowlist is empty by default, so ARC stays
	// purely informational unless a sealer is explicitly trusted.
	arcOverride := arcStatus == "pass" && f.sealerTrusted(arcSealer)

	aligned := spfAligned || dkimAligned

	// Disposition actually applied to this message, for aggregate (rua) reports:
	// "none" when DMARC passes or a trusted-sealer ARC override applies, else the
	// published policy.
	disposition := "none"
	if !aligned && !arcOverride && (policy == "reject" || policy == "quarantine") {
		disposition = policy
	}
	captureDMARC(ctx, email, domain, policy, spfPass, spfAligned, dkimPass, dkimAligned, disposition)

	// DMARC requires both pass AND alignment.
	if aligned {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "pass",
				Detail: fmt.Sprintf("policy=%s spf_pass=%v spf_aligned=%v dkim_pass=%v dkim_aligned=%v", policy, spfPass, spfAligned, dkimPass, dkimAligned),
			},
		}, nil
	}

	if arcOverride {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "pass",
				Detail: fmt.Sprintf("policy=%s dmarc=fail but arc=pass from trusted sealer %s (ARC override) domain=%s", policy, arcSealer, domain),
			},
		}, nil
	}

	// DMARC failed — apply policy
	switch policy {
	case "reject":
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeAction,
			Action:    pipeline.ActionReject,
			RejectMsg: fmt.Sprintf("550 DMARC policy reject for %s", domain),
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "fail",
				Detail: fmt.Sprintf("policy=reject domain=%s", domain),
			},
		}, nil
	case "quarantine":
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionQuarantine,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "fail",
				Detail: fmt.Sprintf("policy=quarantine domain=%s", domain),
			},
		}, nil
	default: // "none" or unknown
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "fail",
				Detail: fmt.Sprintf("policy=none domain=%s (no enforcement)", domain),
			},
		}, nil
	}
}

// lookupDMARC is a package var so tests can stub DNS. It fetches the domain's
// DMARC record via the dmarc library using system DNS.
var lookupDMARC = func(domain string) (string, error) {
	return dmarc.Lookup(domain, nil)
}

// extractAuthDomain extracts a domain from Authentication-Results for a given key.
// e.g., for key "smtp.mailfrom=", extracts the domain from "spf=pass (matched ...) smtp.mailfrom=user@example.com"
func extractAuthDomain(authResults, key string) string {
	idx := strings.Index(authResults, key)
	if idx < 0 {
		return ""
	}
	rest := authResults[idx+len(key):]
	// Extract until space, semicolon, or end
	end := strings.IndexAny(rest, " ;,")
	if end >= 0 {
		rest = rest[:end]
	}
	// If it's an email address, extract the domain
	if atIdx := strings.LastIndex(rest, "@"); atIdx >= 0 {
		return rest[atIdx+1:]
	}
	return rest
}
