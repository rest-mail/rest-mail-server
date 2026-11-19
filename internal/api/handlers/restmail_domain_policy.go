package handlers

import (
	"context"
	"encoding/json"
	"strings"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// domainVerdict is one recipient domain's inbound pipeline, run over that domain's recipients
// alone, and what it decided for them. A RESTMAIL delivery to several domains runs one of these
// per domain, so the order a sender lists recipients in never chooses whose policy applies.
type domainVerdict struct {
	domain string
	to     []string
	cfg    *pipeline.PipelineConfig
	// email is the message as the domain's pipeline left it, which each of its recipients' own
	// Sieve scripts start from (#787).
	email *pipeline.EmailJSON
	// result is nil when no pipeline ran: the domain has none, or there is no engine.
	result *pipeline.ExecutionResult
	trace  traceInputs

	// What a domain whose pipeline let the message through carries into storage.
	auth         []pipeline.AuthResult
	rawMessage   string
	storedAuth   []byte
	suppressKeep bool
	delivered    *traceInputs
}

// action is the verdict for this domain's recipients. Any action other than reject, defer,
// discard and quarantine lets the message through.
func (v *domainVerdict) action() pipeline.Action {
	if v.result == nil {
		return pipeline.ActionContinue
	}
	switch v.result.FinalAction {
	case pipeline.ActionReject, pipeline.ActionDefer, pipeline.ActionDiscard, pipeline.ActionQuarantine:
		return v.result.FinalAction
	}
	return pipeline.ActionContinue
}

// recordAs records this domain's pipeline run with outcome, when a pipeline ran.
func (v *domainVerdict) recordAs(ctx context.Context, h *RestmailHandler, outcome string) {
	if v.result == nil {
		return
	}
	v.trace.Outcome = outcome
	h.recordTrace(ctx, buildTrace(v.trace))
}

// recipientDomains splits recipients by the domain each one's own policy belongs to, in the order
// the domains first appear. That is the domain of the canonical address, the same identity policy
// as delivery, so an equivalent spelling cannot reach a different pipeline; an address with no
// canonical form takes the domain of the mailbox it resolved to.
func recipientDomains(to []string, recipients map[string]restmailRecipient) (domains []string, byDomain map[string][]string) {
	byDomain = map[string][]string{}
	for _, rcpt := range to {
		domain := ""
		if canonical, err := rmail.CanonicalMailboxAddress(rcpt); err == nil {
			_, domain, _ = strings.Cut(canonical, "@")
		} else if recipient, ok := recipients[rcpt]; ok {
			_, domain, _ = strings.Cut(recipient.mailbox.Address, "@")
		}
		if _, seen := byDomain[domain]; !seen {
			domains = append(domains, domain)
		}
		byDomain[domain] = append(byDomain[domain], rcpt)
	}
	return domains, byDomain
}

// separateCopies answers n copies of parsed that share nothing, taken before any pipeline has
// run, so one domain's filters cannot change the message another domain's pipeline judges. A
// parsed message carries nothing JSON leaves out: Body.TransferEncoding is only ever set on a
// message the server writes itself.
func separateCopies(parsed *pipeline.EmailJSON, n int) ([]*pipeline.EmailJSON, error) {
	copies := make([]*pipeline.EmailJSON, n)
	if parsed == nil || n == 0 {
		return copies, nil
	}
	copies[0] = parsed
	if n == 1 {
		return copies, nil
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	for i := 1; i < n; i++ {
		var copied pipeline.EmailJSON
		if err := json.Unmarshal(encoded, &copied); err != nil {
			return nil, err
		}
		copies[i] = &copied
	}
	return copies, nil
}
