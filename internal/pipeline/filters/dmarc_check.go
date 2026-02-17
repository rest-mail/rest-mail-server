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

	// Check Authentication-Results for SPF and DKIM outcomes
	authResults := ""
	if email.Headers.Extra != nil {
		authResults = email.Headers.Extra["Authentication-Results"]
	}

	spfPass := strings.Contains(authResults, "spf=pass")
	dkimPass := strings.Contains(authResults, "dkim=pass")

	// DMARC requires alignment: either SPF or DKIM must pass AND align with From domain
	if spfPass || dkimPass {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "dmarc_check",
				Result: "pass",
				Detail: fmt.Sprintf("policy=%s spf=%v dkim=%v", policy, spfPass, dkimPass),
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
