package filters

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// blockedDialIP reports whether an outbound webhook/notification connection to
// ip must be refused. Filter action URLs (the "webhook" and "duplicate"
// filters) are operator-supplied but only require pipelines:write, so a
// limited admin — or a compromised one — could otherwise point them at
// server-side SSRF escalation targets. The guard blocks the two classes that
// are never a legitimate destination in this deployment:
//
//   - loopback (127.0.0.0/8, ::1) — reaching the API's own admin routes bound
//     on localhost, bypassing network-level admin gates;
//   - link-local (169.254.0.0/16, fe80::/10) — cloud instance-metadata
//     services (169.254.169.254, fd00:ec2::254) that hand out IAM credentials.
//
// Private RFC1918 ranges are intentionally ALLOWED: in this stack filters
// legitimately call sibling containers over the internal bridge network.
func blockedDialIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

// guardedDialControl is a net.Dialer.Control hook. It runs after DNS
// resolution with the concrete IP:port about to be dialed, so it also defeats
// DNS rebinding — a hostname that resolves to a public IP when the filter is
// configured but to a blocked IP at request time.
func guardedDialControl(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("ssrf guard: bad dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("ssrf guard: non-IP dial address %q", host)
	}
	if blockedDialIP(ip) {
		return fmt.Errorf("ssrf guard: refusing to connect to %s", ip)
	}
	return nil
}

// newGuardedHTTPClient returns an *http.Client that refuses to connect to
// loopback/link-local/unspecified addresses (see blockedDialIP) on the initial
// request and on every redirect hop.
func newGuardedHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   guardedDialControl,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			Proxy:               http.ProxyFromEnvironment,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("ssrf guard: stopped after 5 redirects")
			}
			return nil
		},
	}
}
