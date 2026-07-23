package handlers

import (
	"context"
	"log/slog"
	"net"
	"strings"

	dkim "github.com/rest-mail/go-dkim"
)

// RestmailDeliverAuthConfig is the wiring surface for the OSI-3 server-to-server
// authentication gate on POST /restmail/messages. routes.go builds it from
// internal/config.
type RestmailDeliverAuthConfig struct {
	// Enabled turns the gate on (default true). When false the endpoint reverts
	// to legacy behavior (accept any well-formed delivery).
	Enabled bool
	// Strict requires EVERY delivery to come from a trusted peer OR carry a DKIM
	// signature aligned with the From domain. Default false: only deliveries
	// claiming a locally-hosted From domain must authenticate.
	Strict bool
	// TrustedCIDRs are source networks whose deliveries bypass the DKIM check.
	TrustedCIDRs []string
}

// restmailDeliverAuth authenticates an inbound RESTMAIL delivery before it is
// accepted (OSI-3). Without it any host can POST a spoofed-From message straight
// into a local mailbox (BEC/CEO-fraud): the endpoint is unauthenticated and the
// handler's "verified by DKIM/SPF" claim was never enforced on this path.
//
// It is transport-independent — it never trusts the peer's *claimed* identity,
// only (a) the source network (a configured trusted peer / internal proxy), or
// (b) a DKIM signature on the raw message that verifies AND aligns with the From
// domain. That is the same authentication real server-to-server mail relies on,
// and it survives the RESTMAIL HTTPS hop because the signature travels in the
// raw message.
type restmailDeliverAuth struct {
	enabled     bool
	strict      bool
	trustedNets []*net.IPNet
}

// newRestmailDeliverAuth builds the gate from config, parsing the trusted CIDRs
// (bare IPs are accepted as /32 or /128). An unparseable entry is dropped with a
// warning — a bad allowlist entry must never widen trust.
func newRestmailDeliverAuth(cfg RestmailDeliverAuthConfig) *restmailDeliverAuth {
	a := &restmailDeliverAuth{enabled: cfg.Enabled, strict: cfg.Strict}
	for _, entry := range cfg.TrustedCIDRs {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(entry); err == nil {
			a.trustedNets = append(a.trustedNets, n)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			a.trustedNets = append(a.trustedNets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		slog.Warn("restmail: ignoring invalid RESTMAIL_DELIVER_TRUSTED_CIDRS entry", "value", entry)
	}
	return a
}

// trusted reports whether clientIP falls inside a configured trusted peer network.
func (a *restmailDeliverAuth) trusted(clientIP string) bool {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}
	for _, n := range a.trustedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// authorize decides whether to accept a delivery, returning (accept, reason).
// clientIP is the source address; fromDomain the claimed From domain; fromLocal
// whether fromDomain is locally hosted; dkimAligned whether the raw message
// carries a verified DKIM signature aligned with fromDomain.
//
//   - disabled                          -> accept (legacy)
//   - trusted source network            -> accept
//   - verified + aligned DKIM           -> accept
//   - strict mode, neither of the above -> REJECT (closed federation)
//   - From domain locally hosted        -> REJECT (internal spoofing / BEC)
//   - otherwise (external, non-strict)  -> accept (ordinary inbound; the
//     per-domain pipeline still applies its own SPF/DKIM/DMARC policy)
func (a *restmailDeliverAuth) authorize(clientIP, fromDomain string, fromLocal, dkimAligned bool) (bool, string) {
	if !a.enabled {
		return true, "delivery auth disabled"
	}
	if a.trusted(clientIP) {
		return true, "trusted peer"
	}
	if dkimAligned {
		return true, "dkim aligned"
	}
	if a.strict {
		return false, "strict mode: aligned DKIM or trusted peer required"
	}
	if fromLocal {
		return false, "unauthenticated delivery claiming a locally-hosted From domain"
	}
	return true, "external sender (non-strict)"
}

// dkimAlignedWith reports whether raw carries at least one DKIM signature that
// verifies (pass) and whose signing domain aligns with fromDomain. It reuses the
// same go-dkim verifier the inbound pipeline's dkim_verify filter uses.
func dkimAlignedWith(ctx context.Context, raw, fromDomain string) bool {
	if raw == "" || fromDomain == "" {
		return false
	}
	for _, r := range dkim.Verify(ctx, []byte(raw), nil) {
		if r.Result == dkim.ResultPass && domainsAligned(r.Domain, fromDomain) {
			return true
		}
	}
	return false
}

// domainsAligned implements a relaxed, DMARC-style alignment check: the domains
// are equal, or one is a subdomain of the other (same organizational domain).
func domainsAligned(a, b string) bool {
	a = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(a), "."))
	b = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(b), "."))
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}
