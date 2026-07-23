package filters

import (
	"context"
	"fmt"
	"strings"

	"github.com/rest-mail/dmarc"
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
type dmarcCheckFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("dmarc_check", NewDMARCCheck)
}

func NewDMARCCheck(_ []byte) (pipeline.Filter, error) {
	return &dmarcCheckFilter{}, nil
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

	// Parse DMARC policy
	policy := dmarc.ParsePolicy(dmarcRecord)

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
	// authentication on forwarded mail (RFC 8617 §5.2).
	arcStatus := ""
	if email.Metadata != nil {
		arcStatus = email.Metadata["arc_status"]
	}
	if arcStatus == "" && strings.Contains(authResults, "arc=pass") {
		arcStatus = "pass"
	}

	aligned := spfAligned || dkimAligned

	// Disposition actually applied to this message, for aggregate (rua) reports:
	// "none" when DMARC passes or an ARC override applies, else the published policy.
	disposition := "none"
	if !aligned && arcStatus != "pass" && (policy == "reject" || policy == "quarantine") {
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

	if arcStatus == "pass" {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "pass",
				Detail: fmt.Sprintf("policy=%s dmarc=fail but arc=pass (ARC override) domain=%s", policy, domain),
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
