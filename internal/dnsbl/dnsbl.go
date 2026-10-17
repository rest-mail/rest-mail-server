// Package dnsbl checks one sending IP against one explicitly configured
// IP-address DNS blocklist (RFC 5782) and reports what the answer means.
//
// It is read-only and opt-in: nothing here queries anything until a caller
// supplies an IP, a Provider and a Resolver. There are no built-in providers
// and no default resolver. A Monitor checks the IPs and providers it is given
// on a schedule and keeps the listing state in memory only. The API runs one
// only when the operator's config file (LoadConfigFile) enables it, and
// reports through a MetricsObserver: logs and Prometheus metrics, nothing
// durable. Durable listing events are a later slice of #148.
//
// An A answer is not taken to mean "listed". RFC 5782 §2.3 only says list
// values SHOULD be in 127.0.0.0/8, and providers use parts of that range for
// errors: Spamhaus answers 127.255.255.252 (typing error in the zone name),
// 127.255.255.254 (query via a public/open resolver) and 127.255.255.255
// (excessive queries), and says these must not be read as a listing
// (https://www.spamhaus.org/faqs/dnsbl-usage/). So a Provider names its
// listing codes and its error codes explicitly, and anything else is
// indeterminate, never listed and never clean.
//
// Supported: IPv4 lookups (RFC 5782 §2.1, octet-reversed), IPv6 lookups
// (§2.4, nibble-reversed) only for a Provider that declares IPv6 support, and
// the optional TXT reason (§2.1). An IPv4-mapped IPv6 address is checked as
// the IPv4 address it carries. Not supported: domain-name lists (RHSBL/DBL,
// §3), whitelists (DNSWL, §2.2), bitmask-encoded answers, and zones that embed
// an access key (Spamhaus DQS): a key is a secret and does not belong in a
// Provider.
package dnsbl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Status is the outcome of one check.
type Status string

const (
	// StatusListed means the provider returned only configured listing codes.
	StatusListed Status = "listed"
	// StatusNotListed means the name does not exist in the zone (NXDOMAIN or
	// no A record): the provider's authoritative "not listed" (RFC 5782 §2.1).
	StatusNotListed Status = "not_listed"
	// StatusProviderError means the provider answered with one of its
	// configured error codes (refused, rate limited, misconfigured). It says
	// nothing about the IP.
	StatusProviderError Status = "provider_error"
	// StatusIndeterminate means no trustworthy answer: a timeout, a
	// cancellation, SERVFAIL or another resolver failure, or an A answer that
	// is neither a configured listing code nor a configured error code.
	StatusIndeterminate Status = "indeterminate"
	// StatusUnsupported means the provider cannot answer for this address,
	// e.g. an IPv6 address against a zone that does not declare IPv6.
	StatusUnsupported Status = "unsupported"
)

// Resolver is the part of *net.Resolver a check needs. Callers pass one
// explicitly (net.DefaultResolver in production); tests pass a fake.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

var _ Resolver = (*net.Resolver)(nil)

// Provider is one blocklist zone and the meaning of its answers. All fields
// except IPv6 are required; see Validate.
type Provider struct {
	// Name labels the provider in results and logs, e.g. "spamhaus-zen".
	Name string
	// Zone is the DNS zone queried, e.g. "zen.spamhaus.org".
	Zone string
	// Listed are the A answers that mean the IP is listed, e.g.
	// 127.0.0.2/32. Each must be inside 127.0.0.0/8.
	Listed []netip.Prefix
	// Errors are the A answers that mean the query itself failed, e.g.
	// 127.255.255.0/24 for Spamhaus. They must not overlap Listed.
	Errors []netip.Prefix
	// IPv6 says the zone answers nibble-format IPv6 queries (RFC 5782 §2.4).
	// When false an IPv6 address is reported unsupported, never clean.
	IPv6 bool
}

// Errors returned for input that cannot be checked at all.
var (
	ErrInvalidIP       = errors.New("dnsbl: invalid IP address")
	ErrNotPublicIP     = errors.New("dnsbl: IP address is not a public unicast address")
	ErrInvalidProvider = errors.New("dnsbl: invalid provider")
)

var loopback4 = netip.MustParsePrefix("127.0.0.0/8")

// Validate reports whether p is complete and self-consistent.
func (p Provider) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidProvider)
	}
	if err := validateZone(p.Zone); err != nil {
		return fmt.Errorf("%w %q: %v", ErrInvalidProvider, p.Name, err)
	}
	if len(p.Listed) == 0 {
		return fmt.Errorf("%w %q: at least one listing code is required", ErrInvalidProvider, p.Name)
	}
	for _, set := range [][]netip.Prefix{p.Listed, p.Errors} {
		for _, code := range set {
			if !code.IsValid() || !code.Addr().Is4() || !loopback4.Contains(code.Addr()) || code.Bits() < loopback4.Bits() {
				return fmt.Errorf("%w %q: answer code %s is not inside 127.0.0.0/8", ErrInvalidProvider, p.Name, code)
			}
		}
	}
	for _, l := range p.Listed {
		for _, e := range p.Errors {
			if l.Overlaps(e) {
				return fmt.Errorf("%w %q: listing code %s overlaps error code %s", ErrInvalidProvider, p.Name, l, e)
			}
		}
	}
	return nil
}

// validateZone accepts a lower-case LDH domain name of at least two labels,
// with or without a trailing dot, short enough to take a 32-nibble IPv6
// prefix (64 bytes) within the 253-byte name limit.
func validateZone(zone string) error {
	z := strings.TrimSuffix(zone, ".")
	if z == "" {
		return errors.New("zone is required")
	}
	if len(z) > 253-64 {
		return errors.New("zone is too long")
	}
	labels := strings.Split(z, ".")
	if len(labels) < 2 {
		return fmt.Errorf("zone %q needs at least two labels", zone)
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("zone %q has an invalid label %q", zone, label)
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return fmt.Errorf("zone %q has an invalid label %q (lower-case letters, digits and hyphens only)", zone, label)
			}
		}
	}
	return nil
}

// Result is the evidence for one check: what was asked, of whom, what came
// back and what it means.
type Result struct {
	IP       netip.Addr   // the address checked (IPv4-mapped addresses unmapped)
	Provider string       // Provider.Name
	Zone     string       // Provider.Zone, without a trailing dot
	Query    string       // the absolute DNS name queried, with a trailing dot; empty when unsupported
	Status   Status       // what the answer means
	Codes    []netip.Addr // the A answers, sorted; empty when none came back
	Reason   string       // the TXT reason for a listing, when the provider gave one
	Detail   string       // why the status is indeterminate, provider_error or unsupported, or why a listing has no reason
}

// LogValue groups the result for structured logs.
func (r Result) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("ip", r.IP.String()),
		slog.String("provider", r.Provider),
		slog.String("zone", r.Zone),
		slog.String("query", r.Query),
		slog.String("status", string(r.Status)),
	}
	if len(r.Codes) > 0 {
		codes := make([]string, len(r.Codes))
		for i, c := range r.Codes {
			codes[i] = c.String()
		}
		attrs = append(attrs, slog.String("codes", strings.Join(codes, ",")))
	}
	if r.Reason != "" {
		attrs = append(attrs, slog.String("reason", r.Reason))
	}
	if r.Detail != "" {
		attrs = append(attrs, slog.String("detail", r.Detail))
	}
	return slog.GroupValue(attrs...)
}

// Checker runs checks through one Resolver.
type Checker struct {
	resolver Resolver
	timeout  time.Duration
}

// NewChecker returns a Checker that queries through r. A positive timeout
// bounds each Check (the A query and the TXT query together) on top of any
// deadline the caller's context already carries.
func NewChecker(r Resolver, timeout time.Duration) (*Checker, error) {
	if r == nil {
		return nil, errors.New("dnsbl: a resolver is required")
	}
	return &Checker{resolver: r, timeout: timeout}, nil
}

// Check asks p whether ip is listed. The error is non-nil only for input that
// cannot be checked (an invalid or non-public IP, an invalid Provider); every
// DNS outcome, including failures, is a Result.
func (c *Checker) Check(ctx context.Context, ip netip.Addr, p Provider) (Result, error) {
	if !ip.IsValid() || ip.Zone() != "" {
		return Result{}, fmt.Errorf("%w: %q", ErrInvalidIP, ip.String())
	}
	ip = ip.Unmap()
	if !isPublicUnicast(ip) {
		return Result{}, fmt.Errorf("%w: %s", ErrNotPublicIP, ip)
	}
	if err := p.Validate(); err != nil {
		return Result{}, err
	}
	zone := strings.TrimSuffix(p.Zone, ".")
	res := Result{IP: ip, Provider: p.Name, Zone: zone}
	if ip.Is6() && !p.IPv6 {
		res.Status = StatusUnsupported
		res.Detail = "provider does not declare IPv6 support"
		return res, nil
	}
	res.Query = QueryName(ip, zone)

	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	answers, err := c.resolver.LookupNetIP(ctx, "ip4", res.Query)
	if err != nil {
		res.Status, res.Detail = classifyLookupError(ctx, err)
		return res, nil
	}
	for _, a := range answers {
		res.Codes = append(res.Codes, a.Unmap())
	}
	slices.SortFunc(res.Codes, netip.Addr.Compare)
	res.Codes = slices.Compact(res.Codes)

	res.Status, res.Detail = interpret(res.Codes, p)
	if res.Status != StatusListed {
		return res, nil
	}

	// The reason is optional (RFC 5782 §2.1: clients MAY check the TXT
	// record); failing to get it never undoes the listing.
	txts, err := c.resolver.LookupTXT(ctx, res.Query)
	switch {
	case err != nil:
		res.Detail = "listing reason unavailable: " + lookupErrorText(ctx, err)
	case len(txts) == 0:
		res.Detail = "provider gave no listing reason"
	default:
		res.Reason = strings.Join(txts, " ")
	}
	return res, nil
}

// interpret maps the A answers to a status. Error codes win: a provider that
// says the query failed has not said the IP is listed. Any answer outside both
// configured sets makes the whole answer untrustworthy (a hijacking resolver,
// a provider change), so it is indeterminate rather than listed.
func interpret(codes []netip.Addr, p Provider) (Status, string) {
	if len(codes) == 0 {
		return StatusIndeterminate, "resolver returned no answer and no error"
	}
	var unexpected []string
	for _, code := range codes {
		if containsAddr(p.Errors, code) {
			return StatusProviderError, fmt.Sprintf("provider error code %s", code)
		}
		if !containsAddr(p.Listed, code) {
			unexpected = append(unexpected, code.String())
		}
	}
	if len(unexpected) > 0 {
		return StatusIndeterminate, "unexpected answer " + strings.Join(unexpected, ",")
	}
	return StatusListed, ""
}

func containsAddr(set []netip.Prefix, a netip.Addr) bool {
	for _, p := range set {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// classifyLookupError tells an authoritative negative from a failure. The
// caller's cancellation or deadline is checked first so that an abandoned
// query is never reported as clean.
func classifyLookupError(ctx context.Context, err error) (Status, string) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return StatusIndeterminate, lookupErrorText(ctx, err)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound && !dnsErr.IsTimeout {
		return StatusNotListed, ""
	}
	return StatusIndeterminate, lookupErrorText(ctx, err)
}

func lookupErrorText(ctx context.Context, err error) string {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "lookup abandoned: " + ctxErr.Error()
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsTimeout {
		return "lookup timed out: " + err.Error()
	}
	return "lookup failed: " + err.Error()
}

// isPublicUnicast rejects addresses no public blocklist can list, so a
// misconfigured sending IP is an error rather than a reassuring "not listed".
func isPublicUnicast(ip netip.Addr) bool {
	return !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsMulticast() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsInterfaceLocalMulticast() && !ip.IsPrivate()
}

// QueryName builds the absolute name to query for ip in zone: the IPv4
// octets reversed (RFC 5782 §2.1) or the 32 IPv6 nibbles reversed (§2.4),
// then the zone and a trailing dot so no resolver search domain is appended.
// An IPv4-mapped IPv6 address is queried as its IPv4 address.
func QueryName(ip netip.Addr, zone string) string {
	ip = ip.Unmap()
	zone = strings.TrimSuffix(zone, ".")
	var b strings.Builder
	if ip.Is4() {
		a := ip.As4()
		fmt.Fprintf(&b, "%d.%d.%d.%d.", a[3], a[2], a[1], a[0])
	} else {
		const hex = "0123456789abcdef"
		a := ip.As16()
		for i := len(a) - 1; i >= 0; i-- {
			b.WriteByte(hex[a[i]&0x0f])
			b.WriteByte('.')
			b.WriteByte(hex[a[i]>>4])
			b.WriteByte('.')
		}
	}
	b.WriteString(zone)
	b.WriteByte('.')
	return b.String()
}
