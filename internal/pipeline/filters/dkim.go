package filters

import (
	"context"
	"fmt"
	"strings"

	"github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/pipeline"
)

// dkimVerifyFilter verifies DKIM signatures on inbound messages. It records one
// AuthResult per signature, with the signature's d=, for dmarc_check and the
// other filters that act on it, and writes the matching Authentication-Results
// entry for downstream scripts.
type dkimVerifyFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("dkim_verify", NewDKIMVerify)
}

func NewDKIMVerify(_ []byte) (pipeline.Filter, error) {
	return &dkimVerifyFilter{}, nil
}

func (f *dkimVerifyFilter) Name() string              { return "dkim_verify" }
func (f *dkimVerifyFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *dkimVerifyFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email
	// Clone the header map before appending: `modified := *email` shares it
	// with the caller's original, so mutating it in place would leak this
	// filter's Authentication-Results back onto the input (issue #201).
	modified.Headers.Raw = cloneRawHeaders(email.Headers.Raw)

	// DKIM must be verified against the exact signed bytes. The raw message is
	// threaded through the pipeline as metadata by the inbound handlers; a
	// parsed/reconstructed EmailJSON cannot reproduce the signer's
	// canonicalization, so verification requires the raw source.
	raw := ""
	if email.Metadata != nil {
		raw = email.Metadata["raw_message"]
	}

	result := dkim.ResultNone
	detail := "no DKIM signature present"
	verdicts := []pipeline.AuthResult{{Method: pipeline.AuthDKIM, Result: dkim.ResultNone}}

	if raw != "" {
		results := dkim.Verify(ctx, []byte(raw), nil)
		if len(results) > 0 {
			result, detail, verdicts = summarizeDKIM(results)
		}
	} else if hasRawHeader(email.Headers.Raw, "Dkim-Signature") {
		// Signature present but we never received the raw source to verify it.
		result = dkim.ResultNeutral
		detail = "DKIM signature present but raw message unavailable for verification"
		verdicts = []pipeline.AuthResult{{Method: pipeline.AuthDKIM, Result: dkim.ResultNeutral}}
	}

	pipeline.AuthResultsFromContext(ctx).Add(verdicts...)

	// The same verdicts as an Authentication-Results entry, for downstream
	// scripts. The map was cloned above, so this never touches the caller's
	// original.
	modified.Headers.Raw["Authentication-Results"] = append(
		modified.Headers.Raw["Authentication-Results"],
		pipeline.AuthResultsValue(verdicts),
	)

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

// summarizeDKIM reduces one-or-more signature verdicts to an overall result,
// a log detail, and one AuthResult per signature carrying its d= domain. The
// overall result is the strongest: pass > fail > temperror > permerror >
// neutral > none.
func summarizeDKIM(results []dkim.VerifyResult) (overall, detail string, verdicts []pipeline.AuthResult) {
	rank := map[string]int{
		dkim.ResultNone: 0, dkim.ResultNeutral: 1, dkim.ResultPermError: 2,
		dkim.ResultTempError: 3, dkim.ResultFail: 4, dkim.ResultPass: 5,
	}
	overall = dkim.ResultNone
	var details []string
	for _, r := range results {
		verdicts = append(verdicts, pipeline.AuthResult{
			Method: pipeline.AuthDKIM, Result: r.Result, Property: pipeline.PropHeaderD, Domain: r.Domain,
		})
		details = append(details, fmt.Sprintf("d=%s s=%s %s: %s", r.Domain, r.Selector, r.Result, r.Reason))
		if rank[r.Result] > rank[overall] {
			overall = r.Result
		}
	}
	detail = strings.Join(details, " | ")
	return overall, detail, verdicts
}

// hasRawHeader reports whether a canonical (Header-Case) header name has a
// non-empty value in the parsed raw-header map.
func hasRawHeader(raw map[string][]string, name string) bool {
	if raw == nil {
		return false
	}
	vals, ok := raw[name]
	return ok && len(vals) > 0
}
