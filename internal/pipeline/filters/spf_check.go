package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/rest-mail/go-spf"
	"github.com/restmail/restmail/internal/pipeline"
)

// spfCheckFilter checks the sending host against the SPF policy (RFC 7208) of
// the message's HELO and MAIL FROM identities, using
// github.com/rest-mail/go-spf. It records each result as an AuthResult for
// dmarc_check and the other filters that act on it, and writes the matching
// Authentication-Results entry for downstream scripts. By default SPF alone
// never rejects and DMARC decides; fail_action can make an SPF fail of the MAIL
// FROM identity reject or quarantine the message.
type spfCheckFilter struct {
	// failAction is what an SPF fail of the MAIL FROM identity does: continue
	// ("tag", the default), reject or quarantine.
	failAction pipeline.Action
}

// spfCheckConfig is the filter's configuration.
type spfCheckConfig struct {
	// FailAction is "tag" (or empty), "reject" or "quarantine". Only a fail
	// result acts: softfail, neutral and the error results always continue
	// (RFC 7208 §8.5–8.7).
	FailAction string `json:"fail_action"`
}

// spfTimeout bounds one SPF evaluation, DNS included, so a slow or hostile
// authoritative server cannot stall the pipeline. It is a package var so tests
// can shorten it.
var spfTimeout = 10 * time.Second

// spfResolver answers the SPF evaluation's DNS queries. It is a package var so
// tests can stub DNS.
var spfResolver spf.Resolver = net.DefaultResolver

func init() {
	pipeline.DefaultRegistry.Register("spf_check", NewSPFCheck)
}

func NewSPFCheck(config []byte) (pipeline.Filter, error) {
	var cfg spfCheckConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}
	f := &spfCheckFilter{failAction: pipeline.ActionContinue}
	switch cfg.FailAction {
	case "", "tag":
	case "reject":
		f.failAction = pipeline.ActionReject
	case "quarantine":
		f.failAction = pipeline.ActionQuarantine
	default:
		return nil, fmt.Errorf("spf_check: fail_action %q is not tag, reject or quarantine", cfg.FailAction)
	}
	return f, nil
}

func (f *spfCheckFilter) Name() string              { return "spf_check" }
func (f *spfCheckFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *spfCheckFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	clientIP := email.Envelope.ClientIP
	mailFrom := email.Envelope.MailFrom
	helo := email.Envelope.Helo
	if clientIP == "" {
		return spfContinue(spf.ResultNone, "no client IP to check"), nil
	}
	// The identity is the MAIL FROM address, or postmaster@<HELO> for the null
	// reverse-path (RFC 7208 §2.4). With neither there is nothing to check; the
	// header From is not an SPF identity (§2.2).
	if mailFrom == "" && helo == "" {
		return spfContinue(spf.ResultNone, "no MAIL FROM or HELO to check"), nil
	}

	// An unparseable address is the zero netip.Addr, which go-spf answers with
	// permerror.
	ip, _ := netip.ParseAddr(clientIP)
	opts := &spf.Options{Resolver: spfResolver, Timeout: spfTimeout}

	// The HELO identity is checked on its own as well (RFC 7208 §2.3) when it is
	// a host name: go-spf answers anything else, an address literal for
	// instance, with none. For the null reverse-path the MAIL FROM check below
	// already is a check of the HELO identity, so it is not repeated.
	var results []pipeline.AuthResult
	detail := ""
	if mailFrom != "" && pipeline.IsHostName(helo) {
		hr := spf.CheckHELO(ctx, ip, helo, opts)
		results = append(results, pipeline.AuthResult{
			Method: pipeline.AuthSPF, Result: string(hr.Result), Property: pipeline.PropHelo, Domain: helo,
		})
		detail = fmt.Sprintf("; HELO %s: %s (%s)", helo, hr.Result, hr.Reason)
	}

	resp := spf.CheckMailFrom(ctx, ip, mailFrom, helo, opts)
	domain := spfAuthDomain(mailFrom, helo)
	results = append(results, pipeline.AuthResult{
		Method: pipeline.AuthSPF, Result: string(resp.Result), Property: pipeline.PropMailFrom, Domain: domain,
	})
	detail = resp.Reason + detail

	pipeline.AuthResultsFromContext(ctx).Add(results...)

	// The same verdicts as an Authentication-Results entry, for downstream
	// scripts (Sieve, JavaScript, webhooks). No filter reads it back.
	if email.Headers.Raw == nil {
		email.Headers.Raw = make(map[string][]string)
	}
	email.Headers.Raw["Authentication-Results"] = append(
		email.Headers.Raw["Authentication-Results"],
		pipeline.AuthResultsValue(results),
	)

	res := spfContinue(resp.Result, detail)
	if resp.Result == spf.ResultFail && f.failAction != pipeline.ActionContinue {
		res.Action = f.failAction
		if f.failAction == pipeline.ActionReject {
			res.RejectMsg = "550 5.7.23 SPF check failed"
			if domain != "" {
				res.RejectMsg += " for " + domain
			}
		}
	}
	return res, nil
}

// spfContinue is the filter's outcome unless fail_action acts: the message
// continues, with the MAIL FROM result and how it was reached in the log.
func spfContinue(result spf.Result, detail string) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log:    pipeline.FilterLog{Filter: "spf_check", Result: string(result), Detail: detail},
	}
}

// spfAuthDomain returns the domain SPF checked, which is the MAIL FROM domain
// or, for the null reverse-path, the HELO name. It is recorded only when it is a
// plain host name: "victim.example;x.attacker.example" can pass SPF on the
// attacker's DNS, and is not victim.example's to claim.
func spfAuthDomain(mailFrom, helo string) string {
	domain := helo
	if mailFrom != "" {
		domain = mailFrom[strings.LastIndexByte(mailFrom, '@')+1:]
	}
	if !pipeline.IsHostName(domain) {
		return ""
	}
	return domain
}
