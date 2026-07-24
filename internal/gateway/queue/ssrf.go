package queue

import (
	"fmt"
	"net"
	"syscall"
)

// blockedOutboundIP reports whether outbound delivery must refuse a connection
// to ip. Unlike the webhook-filter guard (which allows RFC1918 so filters can
// call sibling containers), outbound mail delivery has no legitimate reason to
// dial anything but a public internet MX or a public RESTMAIL endpoint, so the
// guard here is stricter: every non-public class is blocked.
//
//   - loopback (127.0.0.0/8, ::1) — the gateway's own admin/API routes;
//   - link-local (169.254.0.0/16, fe80::/10) — cloud instance-metadata
//     services (169.254.169.254) that hand out IAM credentials;
//   - private / unique-local (RFC1918 10/8, 172.16/12, 192.168/16 and IPv6
//     fc00::/7) — internal relays and admin APIs that trust the gateway's IP;
//   - unspecified (0.0.0.0, ::).
//
// The block is bypassed only when the worker was explicitly opted in to private
// destinations (SetAllowPrivateDestinations) for a dev/testbed that delivers
// between containers on a private bridge network.
func blockedOutboundIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified()
}

// outboundDialControl returns a net.Dialer.Control hook that refuses to connect
// to any non-public address (see blockedOutboundIP). It runs after DNS
// resolution with the concrete IP:port about to be dialed, so it also defeats
// DNS rebinding: a hostname (or an attacker-advertised MX / RESTMAIL host) that
// resolves to a public IP at check time but a blocked IP at dial time is still
// caught, because the check is on the address actually being dialed.
//
// When allowPrivate is true the guard is disabled — the explicit dev/testbed
// opt-in — and every destination is permitted.
func outboundDialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_ string, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("outbound ssrf guard: bad dial address %q: %w", address, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("outbound ssrf guard: non-IP dial address %q", host)
		}
		if blockedOutboundIP(ip) {
			return fmt.Errorf("outbound ssrf guard: refusing to connect to non-public address %s", ip)
		}
		return nil
	}
}
