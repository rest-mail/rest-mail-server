package queue

import (
	"fmt"
	"net"
	"syscall"

	"github.com/rest-mail/rest-mail-server/pkg/netguard"
)

// blockedOutboundIP reports whether outbound delivery must refuse a connection
// to ip. Unlike the webhook-filter guard (which allows RFC1918 so filters can
// call sibling containers), outbound mail delivery has no legitimate reason to
// dial anything but a public internet MX or a public RESTMAIL endpoint, and a
// remote domain chooses both, so every non-public class netguard knows is
// blocked: loopback (the gateway's own routes), link-local and carrier-grade NAT
// (cloud instance-metadata services), private and unique-local (internal relays
// and admin APIs), and the NAT64 and 6to4 prefixes that would reach any of
// those through an IPv6 address.
//
// The block is bypassed only when the worker was explicitly opted in to private
// destinations (SetAllowPrivateDestinations) for a dev/testbed that delivers
// between containers on a private bridge network.
func blockedOutboundIP(ip net.IP) bool {
	return netguard.NonPublic(ip)
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
