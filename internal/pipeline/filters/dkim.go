package filters

import (
	"context"

	"github.com/restmail/restmail/internal/pipeline"
)

// dkimVerifyFilter verifies DKIM signatures on inbound messages.
// This adds Authentication-Results headers to the email.
type dkimVerifyFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("dkim_verify", NewDKIMVerify)
	pipeline.DefaultRegistry.Register("dkim_sign", NewDKIMSign)
}

func NewDKIMVerify(_ []byte) (pipeline.Filter, error) {
	return &dkimVerifyFilter{}, nil
}

func (f *dkimVerifyFilter) Name() string             { return "dkim_verify" }
func (f *dkimVerifyFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *dkimVerifyFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email

	// Check for DKIM-Signature header
	dkimSig := ""
	if raw := email.Headers.Raw; raw != nil {
		if sigs, ok := raw["Dkim-Signature"]; ok && len(sigs) > 0 {
			dkimSig = sigs[0]
		}
	}

	result := "none"
	detail := "no DKIM signature present"

	if dkimSig != "" {
		// In production, this would perform full DKIM verification:
		// 1. Parse the DKIM-Signature header (d=, s=, b=, bh=, h=)
		// 2. Look up the public key via DNS (selector._domainkey.domain TXT)
		// 3. Verify the body hash
		// 4. Verify the signature over canonicalized headers
		//
		// For now, we mark it as present but unverified.
		result = "neutral"
		detail = "DKIM signature present (verification pending crypto implementation)"
	}

	// Add Authentication-Results header
	if modified.Headers.Extra == nil {
		modified.Headers.Extra = make(map[string]string)
	}
	modified.Headers.Extra["Authentication-Results"] = "restmail; dkim=" + result

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: &modified,
		Log: pipeline.FilterLog{
			Filter: "dkim_verify",
			Result: result,
			Detail: detail,
		},
	}, nil
}

// dkimSignFilter signs outbound messages with the domain's DKIM key.
type dkimSignFilter struct{}

func NewDKIMSign(_ []byte) (pipeline.Filter, error) {
	return &dkimSignFilter{}, nil
}

func (f *dkimSignFilter) Name() string             { return "dkim_sign" }
func (f *dkimSignFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *dkimSignFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email

	// In production, this would:
	// 1. Look up the domain's DKIM private key from the database
	// 2. Canonicalize headers and body (relaxed/relaxed)
	// 3. Compute body hash (SHA-256)
	// 4. Sign the selected headers + body hash
	// 5. Add the DKIM-Signature header
	//
	// For now, we pass through without signing.

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: &modified,
		Log: pipeline.FilterLog{
			Filter: "dkim_sign",
			Result: "skipped",
			Detail: "DKIM signing pending key management implementation",
		},
	}, nil
}
