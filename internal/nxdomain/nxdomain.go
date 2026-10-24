// Package nxdomain tells a domain that does not exist from one that exists
// with no records of the type asked for. RFC 8020 makes an NXDOMAIN answer
// mean that nothing exists at or below the name, and DMARC's np= policy
// applies to exactly such a domain (RFC 9989 §3.2.13). Go's resolver reports
// NXDOMAIN and an empty answer alike ("no such host"), so this asks the
// system's DNS servers itself and reads the response code.
package nxdomain

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// resolvConf is where the system names its DNS servers.
const resolvConf = "/etc/resolv.conf"

// defaultServers are asked when resolv.conf names none, as the C library and
// Go's own resolver do.
var defaultServers = []string{"127.0.0.1:53", "[::1]:53"}

// Client asks recursive DNS servers whether names exist.
type Client struct {
	// Servers are the DNS servers asked, in order, as host:port. A server
	// that does not answer, or answers with a failure, passes the question
	// to the next.
	Servers []string
	// Timeout bounds each server's answer; zero is five seconds.
	Timeout time.Duration
}

// System is a Client for the DNS servers /etc/resolv.conf names, read now.
func System() *Client {
	return &Client{Servers: serversFrom(resolvConf)}
}

// Exists reports whether domain exists: false when a server answers NXDOMAIN
// (RFC 8020), true when it answers with success, whether or not it has
// records. When no server gives either answer, the last failure is returned.
func (c *Client) Exists(ctx context.Context, domain string) (bool, error) {
	name, err := dnsmessage.NewName(strings.TrimSuffix(domain, ".") + ".")
	if err != nil {
		return false, fmt.Errorf("nxdomain: %q: %w", domain, err)
	}
	question := dnsmessage.Question{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}
	lastErr := errors.New("nxdomain: no DNS servers")
	for _, server := range c.Servers {
		rcode, err := c.ask(ctx, server, question)
		switch {
		case err != nil:
			lastErr = err
		case rcode == dnsmessage.RCodeSuccess:
			return true, nil
		case rcode == dnsmessage.RCodeNameError:
			return false, nil
		default:
			lastErr = fmt.Errorf("nxdomain: %s answered %s for %s", server, rcode, name)
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
	}
	return false, lastErr
}

// ask sends question to server over UDP and returns the response code of its
// answer. A response with another ID or question is not the answer, and is
// passed over. A truncated answer still carries the code, so TCP is not needed.
func (c *Client) ask(ctx context.Context, server string, question dnsmessage.Question) (dnsmessage.RCode, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", server)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	var idBytes [2]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return 0, err
	}
	id := binary.BigEndian.Uint16(idBytes[:])
	query := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{question},
	}
	wire, err := query.Pack()
	if err != nil {
		return 0, err
	}
	if _, err := conn.Write(wire); err != nil {
		return 0, err
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return 0, err
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		if err != nil || !h.Response || h.ID != id {
			continue
		}
		if got, err := p.Question(); err != nil || !sameQuestion(got, question) {
			continue
		}
		return h.RCode, nil
	}
}

// sameQuestion compares the question of an answer with the one asked, the
// name without regard to case (RFC 4343).
func sameQuestion(a, b dnsmessage.Question) bool {
	return a.Type == b.Type && a.Class == b.Class && strings.EqualFold(a.Name.String(), b.Name.String())
}

// serversFrom reads the nameserver lines of a resolv.conf file, each as
// host:port, and gives the default servers when there are none.
func serversFrom(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return defaultServers
	}
	defer f.Close()
	var servers []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		if ip := net.ParseIP(strings.SplitN(fields[1], "%", 2)[0]); ip != nil {
			servers = append(servers, net.JoinHostPort(fields[1], "53"))
		}
	}
	if len(servers) == 0 {
		return defaultServers
	}
	return servers
}
