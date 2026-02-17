package filters

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
)

// spfCheckFilter verifies sender IP against SPF DNS records.
type spfCheckFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("spf_check", NewSPFCheck)
}

func NewSPFCheck(_ []byte) (pipeline.Filter, error) {
	return &spfCheckFilter{}, nil
}

func (f *spfCheckFilter) Name() string             { return "spf_check" }
func (f *spfCheckFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *spfCheckFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	clientIP := email.Envelope.ClientIP
	sender := email.Envelope.MailFrom
	if sender == "" && len(email.Headers.From) > 0 {
		sender = email.Headers.From[0].Address
	}

	if clientIP == "" || sender == "" {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "spf_check",
				Result: "none",
				Detail: "no client IP or sender to check",
			},
		}, nil
	}

	parts := strings.SplitN(sender, "@", 2)
	if len(parts) != 2 {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "spf_check",
				Result: "none",
				Detail: "invalid sender format",
			},
		}, nil
	}
	domain := parts[1]

	// Look up SPF record
	result, detail := checkSPF(ctx, clientIP, domain)

	// Write Authentication-Results header for downstream filters (DMARC)
	authResult := fmt.Sprintf("spf=%s (%s) smtp.mailfrom=%s", result, detail, sender)
	if email.Headers.Raw == nil {
		email.Headers.Raw = make(map[string][]string)
	}
	// Append to existing Authentication-Results rather than overwriting
	email.Headers.Raw["Authentication-Results"] = append(
		email.Headers.Raw["Authentication-Results"],
		authResult,
	)

	// SPF alone doesn't reject (DMARC decides)
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "spf_check",
			Result: result,
			Detail: detail,
		},
	}, nil
}

// checkSPF performs a basic SPF check. In production, use a proper SPF library.
func checkSPF(_ context.Context, clientIP, domain string) (string, string) {
	// Look up TXT records for SPF
	records, err := net.LookupTXT(domain)
	if err != nil {
		return "temperror", fmt.Sprintf("DNS lookup failed: %v", err)
	}

	var spfRecord string
	for _, txt := range records {
		if strings.HasPrefix(txt, "v=spf1 ") {
			spfRecord = txt
			break
		}
	}

	if spfRecord == "" {
		return "none", "no SPF record found"
	}

	ip := net.ParseIP(clientIP)
	if ip == nil {
		return "permerror", "invalid client IP"
	}

	// Parse SPF mechanisms (simplified)
	mechanisms := strings.Fields(spfRecord)
	for _, mech := range mechanisms[1:] { // skip "v=spf1"
		qualifier := '+'
		mechStr := mech
		if len(mech) > 0 && (mech[0] == '+' || mech[0] == '-' || mech[0] == '~' || mech[0] == '?') {
			qualifier = rune(mech[0])
			mechStr = mech[1:]
		}

		matched := false

		if mechStr == "all" {
			matched = true
		} else if strings.HasPrefix(mechStr, "ip4:") {
			cidr := mechStr[4:]
			if !strings.Contains(cidr, "/") {
				cidr += "/32"
			}
			_, network, err := net.ParseCIDR(cidr)
			if err == nil && network.Contains(ip) {
				matched = true
			}
		} else if strings.HasPrefix(mechStr, "ip6:") {
			cidr := mechStr[4:]
			if !strings.Contains(cidr, "/") {
				cidr += "/128"
			}
			_, network, err := net.ParseCIDR(cidr)
			if err == nil && network.Contains(ip) {
				matched = true
			}
		} else if strings.HasPrefix(mechStr, "a") {
			lookupDomain := domain
			if strings.HasPrefix(mechStr, "a:") {
				lookupDomain = mechStr[2:]
			}
			addrs, err := net.LookupHost(lookupDomain)
			if err == nil {
				for _, a := range addrs {
					if a == clientIP {
						matched = true
						break
					}
				}
			}
		} else if strings.HasPrefix(mechStr, "mx") {
			lookupDomain := domain
			if strings.HasPrefix(mechStr, "mx:") {
				lookupDomain = mechStr[3:]
			}
			mxRecords, err := net.LookupMX(lookupDomain)
			if err == nil {
				for _, mx := range mxRecords {
					addrs, err := net.LookupHost(mx.Host)
					if err == nil {
						for _, a := range addrs {
							if a == clientIP {
								matched = true
								break
							}
						}
					}
				}
			}
		}

		if matched {
			switch qualifier {
			case '+':
				return "pass", fmt.Sprintf("matched %s", mech)
			case '-':
				return "fail", fmt.Sprintf("matched %s", mech)
			case '~':
				return "softfail", fmt.Sprintf("matched %s", mech)
			case '?':
				return "neutral", fmt.Sprintf("matched %s", mech)
			}
		}
	}

	return "neutral", "no mechanism matched"
}
