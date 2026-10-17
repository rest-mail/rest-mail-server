package filters

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/rest-mail/go-spf"
	"github.com/restmail/restmail/internal/pipeline"
)

// spfCheckFilter checks the sending host against the SPF policy (RFC 7208) of
// the message's MAIL FROM identity, using github.com/rest-mail/go-spf, and
// records the result in Authentication-Results for dmarc_check and the other
// filters that read it. SPF alone never rejects: DMARC decides.
type spfCheckFilter struct{}

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

func NewSPFCheck(_ []byte) (pipeline.Filter, error) {
	return &spfCheckFilter{}, nil
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
	resp := spf.CheckMailFrom(ctx, ip, mailFrom, helo, &spf.Options{Resolver: spfResolver, Timeout: spfTimeout})

	authResult := "restmail; spf=" + string(resp.Result)
	if domain := spfAuthDomain(mailFrom, helo); domain != "" {
		authResult += " smtp.mailfrom=" + domain
	}

	// Write Authentication-Results header for downstream filters (DMARC).
	if email.Headers.Raw == nil {
		email.Headers.Raw = make(map[string][]string)
	}
	email.Headers.Raw["Authentication-Results"] = append(
		email.Headers.Raw["Authentication-Results"],
		authResult,
	)

	if email.Headers.Extra == nil {
		email.Headers.Extra = make(map[string]string)
	}
	if existing := email.Headers.Extra["Authentication-Results"]; existing != "" {
		email.Headers.Extra["Authentication-Results"] = existing + "; " + authResult
	} else {
		email.Headers.Extra["Authentication-Results"] = authResult
	}

	return spfContinue(resp.Result, resp.Reason), nil
}

// spfContinue is the filter's only outcome: the message continues, with the SPF
// result and how it was reached in the log.
func spfContinue(result spf.Result, detail string) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log:    pipeline.FilterLog{Filter: "spf_check", Result: string(result), Detail: detail},
	}
}

// spfAuthDomain returns the smtp.mailfrom value to record: the domain SPF
// checked, which is the MAIL FROM domain or, for the null reverse-path, the
// HELO name. RFC 8601 lets the local-part be left out, and it is: dmarc_check
// and senderAuthenticated read SPF verdicts by scanning Authentication-Results
// text, so a local-part such as "spf=pass" would read as a pass on a message
// that failed. For the same reason the domain is recorded only when it is a
// plain host name ("victim.example;x.attacker.example" can pass SPF on the
// attacker's DNS and would read as victim.example), and go-spf's Reason, which
// quotes the record, goes to the log rather than the header.
func spfAuthDomain(mailFrom, helo string) string {
	domain := helo
	if mailFrom != "" {
		domain = mailFrom[strings.LastIndexByte(mailFrom, '@')+1:]
	}
	if !isHostName(domain) {
		return ""
	}
	return domain
}

// isHostName reports whether name is dot-separated labels of ASCII letters,
// digits and hyphens.
func isHostName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
