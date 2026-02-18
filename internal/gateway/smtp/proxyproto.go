package smtp

import (
	"fmt"
	"log/slog"
	"net"

	proxyproto "github.com/pires/go-proxyproto"
)

// WrapWithProxyProtocol wraps a net.Listener with PROXY protocol (v1/v2) support.
// Connections from trustedCIDRs are expected to send a PROXY header; others pass through.
func WrapWithProxyProtocol(listener net.Listener, trustedCIDRs []string) (net.Listener, error) {
	nets, err := parseCIDRs(trustedCIDRs)
	if err != nil {
		return nil, fmt.Errorf("proxyproto: %w", err)
	}

	policy := func(upstream net.Addr) (proxyproto.Policy, error) {
		ip := extractAddrIP(upstream)
		if ip == nil {
			return proxyproto.IGNORE, nil
		}
		for _, n := range nets {
			if n.Contains(ip) {
				return proxyproto.USE, nil
			}
		}
		// Not from a trusted proxy -- ignore any PROXY header.
		return proxyproto.IGNORE, nil
	}

	proxyListener := &proxyproto.Listener{
		Listener: listener,
		Policy:   policy,
	}

	slog.Info("smtp: PROXY protocol enabled", "trusted_cidrs", trustedCIDRs)
	return proxyListener, nil
}

// parseCIDRs parses a slice of CIDR strings into *net.IPNet values.
func parseCIDRs(cidrs []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipNet)
	}
	return nets, nil
}

// extractAddrIP extracts the IP from a net.Addr.
func extractAddrIP(addr net.Addr) net.IP {
	switch a := addr.(type) {
	case *net.TCPAddr:
		return a.IP
	case *net.UDPAddr:
		return a.IP
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return net.ParseIP(addr.String())
		}
		return net.ParseIP(host)
	}
}
