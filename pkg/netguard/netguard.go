// Package netguard decides which IP addresses a connection whose destination
// someone else chose must never reach: an MX or RESTMAIL host a remote domain
// advertises, or a one-click unsubscribe target a sender wrote (#2105).
//
// Such a destination should only ever be a public internet host. Every other
// class can reach something that trusts the server's own network position: its
// own loopback routes, a cloud instance-metadata service, a private relay or
// admin API, or a carrier's internal network.
package netguard

import "net"

// nonPublicNets are the shared, reserved and wrapper ranges the net.IP class
// methods do not report.
var nonPublicNets = func() []*net.IPNet {
	var nets []*net.IPNet
	for _, cidr := range []string{
		"0.0.0.0/8",       // this network
		"100.64.0.0/10",   // carrier-grade NAT (RFC 6598); one cloud's metadata service is 100.100.100.200
		"192.0.0.0/24",    // IETF protocol assignments
		"198.18.0.0/15",   // benchmarking
		"240.0.0.0/4",     // reserved, and the broadcast address
		"64:ff9b::/96",    // NAT64, which reaches an IPv4 address the checks above did not see
		"64:ff9b:1::/48",  // local-use NAT64
		"2002::/16",       // 6to4, the same
		"fec0::/10",       // deprecated site-local
		"100::/64",        // discard
		"2001:db8::/32",   // documentation
		"192.0.2.0/24",    // documentation
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
	} {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(err)
		}
		nets = append(nets, block)
	}
	return nets
}()

// NonPublic reports whether ip is anything but a public unicast address:
// loopback, link-local, private or unique-local, unspecified, multicast, or one
// of the shared, reserved, documentation and IPv4-wrapping IPv6 ranges. An
// IPv4-mapped IPv6 address is judged by the IPv4 address it carries.
func NonPublic(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	for _, block := range nonPublicNets {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}
