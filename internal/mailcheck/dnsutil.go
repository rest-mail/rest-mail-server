package mailcheck

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/miekg/dns"
)

// systemResolver returns the first nameserver from /etc/resolv.conf or falls back to 8.8.8.8.
func systemResolver() string {
	config, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err == nil && len(config.Servers) > 0 {
		return net.JoinHostPort(config.Servers[0], config.Port)
	}
	return "8.8.8.8:53"
}

// QueryDNS sends a DNS query for the given name and record type using miekg/dns.
// Returns the response message or an error.
func QueryDNS(name string, qtype uint16, timeout time.Duration) (*dns.Msg, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(name), qtype)
	msg.RecursionDesired = true

	client := &dns.Client{
		Timeout: timeout,
	}

	resp, _, err := client.Exchange(msg, systemResolver())
	if err != nil {
		return nil, err
	}
	if resp.Rcode != dns.RcodeSuccess {
		return resp, fmt.Errorf("DNS query for %s type %d returned %s", name, qtype, dns.RcodeToString[resp.Rcode])
	}
	return resp, nil
}

// QueryDNSSEC sends a DNS query with the DO (DNSSEC OK) flag set.
// Returns the response and whether the AD (Authenticated Data) flag is set.
func QueryDNSSEC(name string, qtype uint16, timeout time.Duration) (*dns.Msg, bool, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(name), qtype)
	msg.RecursionDesired = true
	msg.SetEdns0(4096, true) // Enable DNSSEC OK flag

	client := &dns.Client{
		Timeout: timeout,
	}

	resp, _, err := client.Exchange(msg, systemResolver())
	if err != nil {
		return nil, false, err
	}

	return resp, resp.AuthenticatedData, nil
}

// httpClient is a simple HTTP client wrapper with configurable timeout.
type httpClient struct {
	Timeout time.Duration
}

// Get fetches a URL and returns the response body as a string.
func (c *httpClient) Get(url string) (string, error) {
	client := &http.Client{Timeout: c.Timeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // 64KB max
	if err != nil {
		return "", err
	}
	return string(body), nil
}
