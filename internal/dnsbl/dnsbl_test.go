package dnsbl

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeResolver answers from fixed tables and records every query. No test in
// this file touches the network.
type fakeResolver struct {
	mu      sync.Mutex
	a       map[string][]netip.Addr
	aErr    map[string]error
	txt     map[string][]string
	txtErr  map[string]error
	block   bool // wait for the context instead of answering
	queries []string
}

func (f *fakeResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	f.queries = append(f.queries, "A "+network+" "+host)
	f.mu.Unlock()
	if network != "ip4" {
		return nil, errors.New("fake: unexpected network " + network)
	}
	if f.block {
		<-ctx.Done()
		return nil, &net.DNSError{Err: ctx.Err().Error(), Name: host, IsTimeout: errors.Is(ctx.Err(), context.DeadlineExceeded)}
	}
	if err, ok := f.aErr[host]; ok {
		return nil, err
	}
	if addrs, ok := f.a[host]; ok {
		return addrs, nil
	}
	return nil, nxdomain(host)
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	f.queries = append(f.queries, "TXT "+name)
	f.mu.Unlock()
	if err, ok := f.txtErr[name]; ok {
		return nil, err
	}
	if txt, ok := f.txt[name]; ok {
		return txt, nil
	}
	return nil, nxdomain(name)
}

func (f *fakeResolver) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

func nxdomain(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func servfail(name string) error {
	return &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

// spamhausLike mirrors how Spamhaus ZEN documents its answers: listing codes
// in 127.0.0.2-11 and errors in 127.255.255.0/24
// (https://www.spamhaus.org/faqs/dnsbl-usage/). The zone is fictional.
func spamhausLike() Provider {
	return Provider{
		Name: "zen-like",
		Zone: "zen.dnsbl.example",
		Listed: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.2/31"), // 2-3: SBL, CSS
			netip.MustParsePrefix("127.0.0.4/30"), // 4-7: XBL
			netip.MustParsePrefix("127.0.0.9/32"), // DROP
			netip.MustParsePrefix("127.0.0.10/31"),
		},
		Errors: []netip.Prefix{netip.MustParsePrefix("127.255.255.0/24")},
	}
}

const (
	sendingIP = "192.0.2.99"
	query4    = "99.2.0.192.zen.dnsbl.example."
)

func check(t *testing.T, r *fakeResolver, ip string, p Provider) Result {
	t.Helper()
	c, err := NewChecker(r, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Check(context.Background(), netip.MustParseAddr(ip), p)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return res
}

func TestQueryName(t *testing.T) {
	cases := []struct{ ip, zone, want string }{
		// RFC 5782 §2.1 and §2.4 examples.
		{"192.0.2.99", "bad.example.com", "99.2.0.192.bad.example.com."},
		{"2001:db8:abc:123::42", "ugly.example.com", "2.4.0.0.0.0.0.0.0.0.0.0.0.0.0.0.3.2.1.0.c.b.a.0.8.b.d.0.1.0.0.2.ugly.example.com."},
		{"::ffff:192.0.2.99", "bad.example.com.", "99.2.0.192.bad.example.com."},
	}
	for _, tc := range cases {
		if got := QueryName(netip.MustParseAddr(tc.ip), tc.zone); got != tc.want {
			t.Errorf("QueryName(%s, %s) = %q, want %q", tc.ip, tc.zone, got, tc.want)
		}
	}
}

func TestListedWithReason(t *testing.T) {
	r := &fakeResolver{
		a:   map[string][]netip.Addr{query4: addrs("127.0.0.4", "127.0.0.2")},
		txt: map[string][]string{query4: {"Listed by SBL, see https://check.dnsbl.example/"}},
	}
	res := check(t, r, sendingIP, spamhausLike())
	want := Result{
		IP: netip.MustParseAddr(sendingIP), Provider: "zen-like", Zone: "zen.dnsbl.example", Query: query4,
		Status: StatusListed, Codes: addrs("127.0.0.2", "127.0.0.4"),
		Reason: "Listed by SBL, see https://check.dnsbl.example/",
	}
	if res.IP != want.IP || res.Provider != want.Provider || res.Zone != want.Zone || res.Query != want.Query ||
		res.Status != want.Status || !equalAddrs(res.Codes, want.Codes) || res.Reason != want.Reason || res.Detail != "" {
		t.Fatalf("got %+v\nwant %+v", res, want)
	}
}

func TestListingSurvivesReasonFailure(t *testing.T) {
	cases := map[string]struct {
		r      *fakeResolver
		detail string
	}{
		"txt servfail": {
			r:      &fakeResolver{a: map[string][]netip.Addr{query4: addrs("127.0.0.2")}, txtErr: map[string]error{query4: servfail(query4)}},
			detail: "listing reason unavailable",
		},
		"no txt record": {
			r:      &fakeResolver{a: map[string][]netip.Addr{query4: addrs("127.0.0.2")}},
			detail: "listing reason unavailable",
		},
		"empty txt": {
			r:      &fakeResolver{a: map[string][]netip.Addr{query4: addrs("127.0.0.2")}, txt: map[string][]string{query4: {}}},
			detail: "no listing reason",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := check(t, tc.r, sendingIP, spamhausLike())
			if res.Status != StatusListed || res.Reason != "" || !strings.Contains(res.Detail, tc.detail) {
				t.Fatalf("got status=%s reason=%q detail=%q, want listed with no reason and detail %q", res.Status, res.Reason, res.Detail, tc.detail)
			}
		})
	}
}

func TestNXDOMAINIsNotListed(t *testing.T) {
	r := &fakeResolver{}
	res := check(t, r, sendingIP, spamhausLike())
	if res.Status != StatusNotListed || len(res.Codes) != 0 || res.Detail != "" {
		t.Fatalf("got %+v, want not_listed", res)
	}
	if got := r.calls(); len(got) != 1 || got[0] != "A ip4 "+query4 {
		t.Fatalf("queries = %v, want only the A query (no TXT for a clean IP)", got)
	}
}

func TestResolverFailuresAreIndeterminate(t *testing.T) {
	cases := map[string]error{
		"servfail":                servfail(query4),
		"dns timeout":             &net.DNSError{Err: "i/o timeout", Name: query4, IsTimeout: true},
		"not found but timed out": &net.DNSError{Err: "no such host", Name: query4, IsNotFound: true, IsTimeout: true},
		"plain error":             errors.New("connection refused"),
		"wrapped deadline":        &net.DNSError{Err: "lookup", Name: query4, UnwrapErr: context.DeadlineExceeded},
	}
	for name, lookupErr := range cases {
		t.Run(name, func(t *testing.T) {
			r := &fakeResolver{aErr: map[string]error{query4: lookupErr}}
			res := check(t, r, sendingIP, spamhausLike())
			if res.Status != StatusIndeterminate || res.Detail == "" {
				t.Fatalf("got status=%s detail=%q, want indeterminate with a detail", res.Status, res.Detail)
			}
		})
	}
}

func TestDeadlineAndCancellationAreIndeterminate(t *testing.T) {
	run := func(t *testing.T, ctx context.Context, timeout time.Duration) Result {
		t.Helper()
		c, err := NewChecker(&fakeResolver{block: true}, timeout)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan Result, 1)
		go func() {
			res, err := c.Check(ctx, netip.MustParseAddr(sendingIP), spamhausLike())
			if err != nil {
				t.Errorf("Check: %v", err)
			}
			done <- res
		}()
		select {
		case res := <-done:
			return res
		case <-time.After(5 * time.Second):
			t.Fatal("Check did not honour its deadline or cancellation")
			return Result{}
		}
	}

	t.Run("checker timeout", func(t *testing.T) {
		res := run(t, context.Background(), 20*time.Millisecond)
		if res.Status != StatusIndeterminate || !strings.Contains(res.Detail, "deadline exceeded") {
			t.Fatalf("got status=%s detail=%q", res.Status, res.Detail)
		}
	})
	t.Run("caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		res := run(t, ctx, 0)
		if res.Status != StatusIndeterminate || !strings.Contains(res.Detail, "deadline exceeded") {
			t.Fatalf("got status=%s detail=%q", res.Status, res.Detail)
		}
	})
	t.Run("caller cancels", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)
		res := run(t, ctx, time.Minute)
		if res.Status != StatusIndeterminate || !strings.Contains(res.Detail, "canceled") {
			t.Fatalf("got status=%s detail=%q", res.Status, res.Detail)
		}
	})
	t.Run("not found after cancellation is not clean", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c, _ := NewChecker(&fakeResolver{}, 0) // answers NXDOMAIN regardless of ctx
		res, err := c.Check(ctx, netip.MustParseAddr(sendingIP), spamhausLike())
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusIndeterminate {
			t.Fatalf("got status=%s, want indeterminate for an abandoned query", res.Status)
		}
	})
}

func TestProviderErrorCodesAreNeverListings(t *testing.T) {
	// 127.255.255.252 typing error, .254 open/public resolver, .255 excessive
	// queries; and an error alongside a listing code is still an error.
	for _, answer := range [][]netip.Addr{
		addrs("127.255.255.252"),
		addrs("127.255.255.254"),
		addrs("127.255.255.255"),
		addrs("127.0.0.2", "127.255.255.254"),
	} {
		r := &fakeResolver{a: map[string][]netip.Addr{query4: answer}}
		res := check(t, r, sendingIP, spamhausLike())
		if res.Status != StatusProviderError || !strings.Contains(res.Detail, "127.255.255.") {
			t.Errorf("answer %v: got status=%s detail=%q, want provider_error", answer, res.Status, res.Detail)
		}
		for _, q := range r.calls() {
			if strings.HasPrefix(q, "TXT") {
				t.Errorf("answer %v: fetched a listing reason for a provider error", answer)
			}
		}
	}
}

func TestUnexpectedAnswersAreIndeterminate(t *testing.T) {
	noErrors := spamhausLike()
	noErrors.Errors = nil
	cases := map[string]struct {
		p      Provider
		answer []netip.Addr
	}{
		"unconfigured 127 code":              {spamhausLike(), addrs("127.0.0.99")},
		"hijacked nxdomain":                  {spamhausLike(), addrs("198.51.100.7")},
		"listing mixed with unknown":         {spamhausLike(), addrs("127.0.0.2", "198.51.100.7")},
		"error code not configured as error": {noErrors, addrs("127.255.255.254")},
		"empty answer":                       {spamhausLike(), nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := &fakeResolver{a: map[string][]netip.Addr{query4: tc.answer}}
			res := check(t, r, sendingIP, tc.p)
			if res.Status != StatusIndeterminate || res.Detail == "" {
				t.Fatalf("got status=%s detail=%q, want indeterminate", res.Status, res.Detail)
			}
		})
	}
}

func TestMappedAnswerIsUnmapped(t *testing.T) {
	r := &fakeResolver{a: map[string][]netip.Addr{query4: addrs("::ffff:127.0.0.2")}}
	res := check(t, r, sendingIP, spamhausLike())
	if res.Status != StatusListed || !equalAddrs(res.Codes, addrs("127.0.0.2")) {
		t.Fatalf("got status=%s codes=%v, want listed with 127.0.0.2", res.Status, res.Codes)
	}
}

func TestIPv4MappedIPIsCheckedAsIPv4(t *testing.T) {
	r := &fakeResolver{}
	res := check(t, r, "::ffff:192.0.2.99", spamhausLike())
	if res.IP != netip.MustParseAddr(sendingIP) || res.Query != query4 || res.Status != StatusNotListed {
		t.Fatalf("got %+v, want the IPv4 query for %s", res, sendingIP)
	}
}

func TestIPv6(t *testing.T) {
	const ip6 = "2001:db8:abc:123::42"
	q6 := "2.4.0.0.0.0.0.0.0.0.0.0.0.0.0.0.3.2.1.0.c.b.a.0.8.b.d.0.1.0.0.2.zen.dnsbl.example."

	t.Run("unsupported is not clean", func(t *testing.T) {
		r := &fakeResolver{}
		res := check(t, r, ip6, spamhausLike())
		if res.Status != StatusUnsupported || res.Query != "" || res.Detail == "" {
			t.Fatalf("got %+v, want unsupported", res)
		}
		if got := r.calls(); len(got) != 0 {
			t.Fatalf("queried %v for an unsupported address", got)
		}
	})
	t.Run("supported", func(t *testing.T) {
		p := spamhausLike()
		p.IPv6 = true
		r := &fakeResolver{a: map[string][]netip.Addr{q6: addrs("127.0.0.2")}, txt: map[string][]string{q6: {"listed"}}}
		res := check(t, r, ip6, p)
		if res.Status != StatusListed || res.Query != q6 || res.Reason != "listed" {
			t.Fatalf("got %+v, want listed via %s", res, q6)
		}
	})
}

func TestMalformedInputIsRejected(t *testing.T) {
	c, err := NewChecker(&fakeResolver{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ips := map[string]struct {
		ip   netip.Addr
		want error
	}{
		"zero":        {netip.Addr{}, ErrInvalidIP},
		"zoned":       {netip.MustParseAddr("2001:db8::1%eth0"), ErrInvalidIP},
		"private":     {netip.MustParseAddr("10.1.2.3"), ErrNotPublicIP},
		"loopback":    {netip.MustParseAddr("127.0.0.2"), ErrNotPublicIP},
		"v6 loopback": {netip.MustParseAddr("::1"), ErrNotPublicIP},
		"unspecified": {netip.MustParseAddr("0.0.0.0"), ErrNotPublicIP},
		"link local":  {netip.MustParseAddr("fe80::1"), ErrNotPublicIP},
		"multicast":   {netip.MustParseAddr("224.0.0.1"), ErrNotPublicIP},
		"ula":         {netip.MustParseAddr("fd00::1"), ErrNotPublicIP},
		"mapped priv": {netip.MustParseAddr("::ffff:192.168.1.1"), ErrNotPublicIP},
	}
	for name, tc := range ips {
		if _, err := c.Check(context.Background(), tc.ip, spamhausLike()); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}

	mutate := func(f func(*Provider)) Provider { p := spamhausLike(); f(&p); return p }
	providers := map[string]Provider{
		"no name":            mutate(func(p *Provider) { p.Name = " " }),
		"no zone":            mutate(func(p *Provider) { p.Zone = "" }),
		"single label":       mutate(func(p *Provider) { p.Zone = "example" }),
		"empty label":        mutate(func(p *Provider) { p.Zone = "zen..example" }),
		"bad character":      mutate(func(p *Provider) { p.Zone = "zen_list.example" }),
		"upper case":         mutate(func(p *Provider) { p.Zone = "Zen.Example" }),
		"hyphen edge":        mutate(func(p *Provider) { p.Zone = "-zen.example" }),
		"injected name":      mutate(func(p *Provider) { p.Zone = "zen.example 127.0.0.1" }),
		"too long":           mutate(func(p *Provider) { p.Zone = strings.Repeat("a.", 100) + "example" }),
		"no listing codes":   mutate(func(p *Provider) { p.Listed = nil }),
		"code outside 127/8": mutate(func(p *Provider) { p.Listed = []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")} }),
		"code wider than /8": mutate(func(p *Provider) {
			p.Listed, p.Errors = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/7")}, nil
		}),
		"ipv6 code":    mutate(func(p *Provider) { p.Listed = []netip.Prefix{netip.MustParsePrefix("::ffff:127.0.0.2/128")} }),
		"invalid code": mutate(func(p *Provider) { p.Listed = []netip.Prefix{{}} }),
		"error outside 127/8": mutate(func(p *Provider) {
			p.Errors = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
		}),
		"listing overlaps error": mutate(func(p *Provider) {
			p.Errors = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/24")}
		}),
	}
	for name, p := range providers {
		if _, err := c.Check(context.Background(), netip.MustParseAddr(sendingIP), p); !errors.Is(err, ErrInvalidProvider) {
			t.Errorf("%s: err = %v, want ErrInvalidProvider", name, err)
		}
	}
	if got := c.resolver.(*fakeResolver).calls(); len(got) != 0 {
		t.Fatalf("rejected input still queried %v", got)
	}
	if err := spamhausLike().Validate(); err != nil {
		t.Fatalf("valid provider rejected: %v", err)
	}
	trailing := mutate(func(p *Provider) { p.Zone = "zen.dnsbl.example." })
	if err := trailing.Validate(); err != nil {
		t.Fatalf("trailing-dot zone rejected: %v", err)
	}
}

func TestNewCheckerNeedsResolver(t *testing.T) {
	if _, err := NewChecker(nil, time.Second); err == nil {
		t.Fatal("NewChecker(nil) succeeded")
	}
}

func TestResultLogValue(t *testing.T) {
	res := Result{
		IP: netip.MustParseAddr(sendingIP), Provider: "zen-like", Zone: "zen.dnsbl.example", Query: query4,
		Status: StatusListed, Codes: addrs("127.0.0.2", "127.0.0.4"), Reason: "why",
	}
	got := map[string]string{}
	for _, a := range res.LogValue().Group() {
		got[a.Key] = a.Value.String()
	}
	want := map[string]string{
		"ip": sendingIP, "provider": "zen-like", "zone": "zen.dnsbl.example", "query": query4,
		"status": "listed", "codes": "127.0.0.2,127.0.0.4", "reason": "why",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["detail"]; ok {
		t.Errorf("empty detail logged")
	}
	var _ slog.LogValuer = res
}

func equalAddrs(a, b []netip.Addr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
