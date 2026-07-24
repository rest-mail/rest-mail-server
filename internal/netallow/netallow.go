// Package netallow provides a small IP CIDR allowlist plus a trusted-proxy-aware
// real-client-IP derivation. It is shared by the API's /metrics route gate and
// the protocol gateways' metrics servers (OSI-12) so the two subsystems apply
// identical, non-duplicated allowlisting and identical spoofing-resistant peer
// derivation.
//
// It is intentionally dependency-free (standard net / net/http only) so both the
// internal/api and internal/gateway trees — and internal/config — can import it
// without introducing an import cycle.
package netallow

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// DefaultInternalCIDRs is the default metrics allowlist: IPv4 and IPv6 loopback
// plus the RFC1918 private ranges and the IPv6 unique-local block. It mirrors how
// an in-cluster Prometheus reaches a /metrics endpoint (a container-network,
// RFC1918 source address), so the default keeps internal scraping working while
// denying public peers. Callers must not mutate the returned slice.
var DefaultInternalCIDRs = []string{
	"127.0.0.0/8",    // IPv4 loopback
	"::1/128",        // IPv6 loopback
	"10.0.0.0/8",     // RFC1918
	"172.16.0.0/12",  // RFC1918
	"192.168.0.0/16", // RFC1918
	"fc00::/7",       // IPv6 unique local (RFC 4193)
}

// Allowlist is an immutable set of CIDR networks. The zero value (no networks)
// denies every address — fail-closed by construction.
type Allowlist struct {
	nets []*net.IPNet
}

// New parses cidrs into an Allowlist. Bare IP addresses are accepted (treated as
// /32 or /128). An unparseable entry is dropped with a warning rather than
// aborting: a single malformed allowlist entry must never silently widen access,
// and it must not take the whole gate down either. subsystem names the caller in
// the warning log.
func New(subsystem string, cidrs []string) *Allowlist {
	a := &Allowlist{}
	for _, entry := range cidrs {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(entry); err == nil {
			a.nets = append(a.nets, n)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			a.nets = append(a.nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		slog.Warn("netallow: ignoring invalid CIDR entry", "subsystem", subsystem, "value", entry)
	}
	return a
}

// Allowed reports whether ip falls within any allowlisted network. A nil ip is
// never allowed — an undeterminable peer fails closed.
func (a *Allowlist) Allowed(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range a.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Empty reports whether the allowlist contains no networks (deny-all).
func (a *Allowlist) Empty() bool { return len(a.nets) == 0 }

// RealClientIP derives the genuine client IP of r, resistant to a spoofable
// X-Forwarded-For. It starts from the direct TCP peer (r.RemoteAddr) and only
// consults X-Forwarded-For when that direct peer is itself a trusted proxy; then
// it walks the header right-to-left and returns the closest address that is NOT a
// trusted proxy (the real origin behind a proxy chain). An untrusted peer's
// forwarded header is ignored entirely, so a public client cannot forge an
// internal source address. Returns nil when no IP can be determined, in which
// case the caller must deny.
func RealClientIP(r *http.Request, trustedProxies *Allowlist) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(strings.TrimSpace(host))
	if peer == nil {
		return nil
	}
	if trustedProxies != nil && trustedProxies.Allowed(peer) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				cand := net.ParseIP(strings.TrimSpace(parts[i]))
				if cand == nil {
					continue
				}
				if trustedProxies.Allowed(cand) {
					continue // skip chained trusted proxies
				}
				return cand
			}
		}
	}
	return peer
}
