package main

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// sourcePool hands out local addresses in turn. The gateways allow 20
// connections per client IP, and a load is meant to look like many clients, so
// one generator on one address would measure that limit and nothing else.
// A nil pool leaves the choice to the kernel.
type sourcePool struct {
	addrs []net.IP
	next  atomic.Uint64
}

// parseSources reads "10.99.201.1-25": from that address to the one with 25 as
// its last octet. A single address has no range.
func parseSources(spec string) (*sourcePool, error) {
	if spec == "" {
		return nil, nil
	}
	first, last, ranged := strings.Cut(spec, "-")
	addr, err := netip.ParseAddr(first)
	if err != nil || !addr.Is4() {
		return nil, fmt.Errorf("source addresses %q: want an IPv4 address or a range like 10.99.201.1-25", spec)
	}
	octets := addr.As4()
	end := int(octets[3])
	if ranged {
		if end, err = strconv.Atoi(last); err != nil || end < int(octets[3]) || end > 254 {
			return nil, fmt.Errorf("source addresses %q: the range must end at a last octet from %d to 254", spec, octets[3])
		}
	}
	p := &sourcePool{}
	for o := int(octets[3]); o <= end; o++ {
		p.addrs = append(p.addrs, net.IPv4(octets[0], octets[1], octets[2], byte(o)))
	}
	return p, nil
}

// dialer is a dialer from the next address in the pool.
func (p *sourcePool) dialer(timeout time.Duration) *net.Dialer {
	d := &net.Dialer{Timeout: timeout}
	if p != nil {
		i := (p.next.Add(1) - 1) % uint64(len(p.addrs))
		d.LocalAddr = &net.TCPAddr{IP: p.addrs[i]}
	}
	return d
}
