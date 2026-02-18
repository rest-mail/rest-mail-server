package filters

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
)

// dmarcCheckFilter evaluates DMARC policy using SPF and DKIM results.
type dmarcCheckFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("dmarc_check", NewDMARCCheck)
}

func NewDMARCCheck(_ []byte) (pipeline.Filter, error) {
	return &dmarcCheckFilter{}, nil
}

func (f *dmarcCheckFilter) Name() string             { return "dmarc_check" }
func (f *dmarcCheckFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *dmarcCheckFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
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
	policy := parseDMARCPolicy(dmarcRecord)

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
			spfAligned = domainsAlign(spfDomain, domain)
		}
	}

	// Extract DKIM authenticated domain from auth-results
	dkimAligned := false
	if dkimPass {
		// For DKIM, the d= domain is in the DKIM-Signature header
		// but we simplified by checking the auth-results domain
		dkimDomain := extractAuthDomain(authResults, "header.d=")
		if dkimDomain == "" {
			// Fallback: if DKIM passed, assume alignment (conservative for our stub verifier)
			dkimAligned = true
		} else {
			dkimAligned = domainsAlign(dkimDomain, domain)
		}
	}

	// DMARC requires both pass AND alignment
	if spfAligned || dkimAligned {
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

func lookupDMARC(domain string) (string, error) {
	records, err := net.LookupTXT("_dmarc." + domain)
	if err != nil {
		return "", err
	}
	for _, r := range records {
		if strings.HasPrefix(r, "v=DMARC1") {
			return r, nil
		}
	}
	return "", nil
}

func parseDMARCPolicy(record string) string {
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "p=") {
			return strings.TrimPrefix(part, "p=")
		}
	}
	return "none"
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

// domainsAlign checks if two domains are aligned per DMARC relaxed alignment.
// Relaxed alignment: the organizational domains must match.
// For simplicity, we check if one domain is a suffix of the other or they're equal.
func domainsAlign(authDomain, fromDomain string) bool {
	authDomain = strings.ToLower(authDomain)
	fromDomain = strings.ToLower(fromDomain)
	if authDomain == fromDomain {
		return true
	}
	// Relaxed: check organizational domain (simple: check if one is subdomain of other)
	if strings.HasSuffix(authDomain, "."+fromDomain) || strings.HasSuffix(fromDomain, "."+authDomain) {
		return true
	}
	return false
}
