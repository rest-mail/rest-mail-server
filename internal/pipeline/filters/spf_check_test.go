package filters

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

// stubResolver is an in-memory, context-aware DNS resolver for SPF tests. A
// missing key yields an NXDOMAIN (net.DNSError.IsNotFound). When block is true
// every lookup blocks until the context is cancelled, simulating a hung or
// hostile authoritative server.
type stubResolver struct {
	txt   map[string][]string
	ips   map[string][]net.IPAddr
	mxs   map[string][]*net.MX
	ptrs  map[string][]string
	block bool
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (r *stubResolver) wait(ctx context.Context) error {
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return ctx.Err()
}

func (r *stubResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if err := r.wait(ctx); err != nil {
		return nil, err
	}
	if v, ok := r.txt[name]; ok {
		return v, nil
	}
	return nil, notFound(name)
}

func (r *stubResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if err := r.wait(ctx); err != nil {
		return nil, err
	}
	if v, ok := r.ips[host]; ok {
		return v, nil
	}
	return nil, notFound(host)
}

func (r *stubResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	if err := r.wait(ctx); err != nil {
		return nil, err
	}
	if v, ok := r.mxs[name]; ok {
		return v, nil
	}
	return nil, notFound(name)
}

func (r *stubResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	if err := r.wait(ctx); err != nil {
		return nil, err
	}
	if v, ok := r.ptrs[addr]; ok {
		return v, nil
	}
	return nil, notFound(addr)
}

func ipAddrs(ss ...string) []net.IPAddr {
	out := make([]net.IPAddr, 0, len(ss))
	for _, s := range ss {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out
}

// useResolver installs r as the SPF resolver for the duration of the test.
func useResolver(t *testing.T, r spfResolver) {
	t.Helper()
	orig := spfDefaultResolver
	spfDefaultResolver = r
	t.Cleanup(func() { spfDefaultResolver = orig })
}

func TestSPF_IP4Pass(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{"example.com": {"v=spf1 ip4:1.2.3.4 -all"}},
	})
	res, detail := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "pass" {
		t.Fatalf("got %s (%s), want pass", res, detail)
	}
}

func TestSPF_HardFail(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{"example.com": {"v=spf1 ip4:9.9.9.9 -all"}},
	})
	res, _ := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "fail" {
		t.Fatalf("got %s, want fail", res)
	}
}

func TestSPF_None(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{"example.com": {"not an spf record"}},
	})
	res, _ := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "none" {
		t.Fatalf("got %s, want none", res)
	}
}

// TestSPF_Include is RED on the hand-rolled evaluator: include: is skipped so
// the IP that only matches inside the included record never matches and the
// record falls through to ~all (softfail) instead of pass.
func TestSPF_Include(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{
			"example.com":      {"v=spf1 include:_spf.example.net ~all"},
			"_spf.example.net": {"v=spf1 ip4:1.2.3.4 -all"},
		},
	})
	res, detail := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "pass" {
		t.Fatalf("include: got %s (%s), want pass", res, detail)
	}
}

// TestSPF_IncludeSoftfailDoesNotMatch: an include whose recursive result is not
// pass must NOT match; evaluation continues to the trailing mechanism.
func TestSPF_IncludeNoMatchContinues(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{
			"example.com":      {"v=spf1 include:_spf.example.net -all"},
			"_spf.example.net": {"v=spf1 ip4:9.9.9.9 ~all"},
		},
	})
	// 1.2.3.4 not covered by the include -> include does not match -> -all -> fail.
	res, detail := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "fail" {
		t.Fatalf("include-no-match: got %s (%s), want fail", res, detail)
	}
}

// TestSPF_Redirect is RED on the hand-rolled evaluator: redirect= is ignored.
func TestSPF_Redirect(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{
			"example.com":      {"v=spf1 redirect=_spf.example.org"},
			"_spf.example.org": {"v=spf1 ip4:1.2.3.4 -all"},
		},
	})
	res, detail := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "pass" {
		t.Fatalf("redirect: got %s (%s), want pass", res, detail)
	}
}

// TestSPF_RedirectIgnoredWithAll: redirect must be ignored when an all
// mechanism is present (RFC 7208 §6.1).
func TestSPF_RedirectIgnoredWithAll(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{
			"example.com":      {"v=spf1 ~all redirect=_spf.example.org"},
			"_spf.example.org": {"v=spf1 ip4:1.2.3.4 -all"},
		},
	})
	res, _ := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "softfail" {
		t.Fatalf("redirect-with-all: got %s, want softfail", res)
	}
}

// TestSPF_LookupLimit is RED on the hand-rolled evaluator: with no lookup limit
// the chain of includes is simply skipped and never permerrors.
func TestSPF_LookupLimit(t *testing.T) {
	txt := map[string][]string{}
	// Top record fans out to 11 includes, each of which is a valid but
	// non-matching record. The 11th lookup exceeds RFC 7208 §4.6.4's limit of 10.
	rec := "v=spf1"
	for i := 0; i < 11; i++ {
		name := "i" + string(rune('a'+i)) + ".example.net"
		rec += " include:" + name
		txt[name] = []string{"v=spf1 -all"}
	}
	rec += " ~all"
	txt["example.com"] = []string{rec}

	useResolver(t, &stubResolver{txt: txt})
	res, detail := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "permerror" {
		t.Fatalf("lookup-limit: got %s (%s), want permerror", res, detail)
	}
}

// TestSPF_WithinLookupLimit: a record right at the limit must not permerror.
func TestSPF_WithinLookupLimit(t *testing.T) {
	txt := map[string][]string{}
	rec := "v=spf1"
	for i := 0; i < 9; i++ {
		name := "i" + string(rune('a'+i)) + ".example.net"
		rec += " include:" + name
		txt[name] = []string{"v=spf1 -all"}
	}
	rec += " ip4:1.2.3.4 -all"
	txt["example.com"] = []string{rec}

	useResolver(t, &stubResolver{txt: txt})
	res, detail := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "pass" {
		t.Fatalf("within-limit: got %s (%s), want pass", res, detail)
	}
}

// TestSPF_Stall is RED on the hand-rolled evaluator: DNS calls discard the
// context, so a hung resolver stalls evaluation indefinitely. The fixed
// evaluator must bound resolution and return temperror.
func TestSPF_Stall(t *testing.T) {
	orig := spfLookupTimeout
	spfLookupTimeout = 200 * time.Millisecond
	t.Cleanup(func() { spfLookupTimeout = orig })

	useResolver(t, &stubResolver{block: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // unblocks a stalled resolver goroutine on Phase-A failure

	type outcome struct {
		res    string
		detail string
	}
	done := make(chan outcome, 1)
	go func() {
		res, detail := checkSPF(ctx, "1.2.3.4", "example.com", "user@example.com", "")
		done <- outcome{res, detail}
	}()

	select {
	case o := <-done:
		if o.res != "temperror" {
			t.Fatalf("stall: got %s (%s), want temperror", o.res, o.detail)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stall: checkSPF did not return; evaluation is not bounded by a DNS timeout")
	}
}

// TestSPF_Exists is RED on the hand-rolled evaluator: exists: and macros are
// skipped. A record like exists:%{i}.whitelist.example must expand %{i} to the
// client IP and match when the synthesized name resolves.
func TestSPF_Exists(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{
			"example.com": {"v=spf1 exists:%{ir}.whitelist.example -all"},
		},
		ips: map[string][]net.IPAddr{
			// %{ir} reverses the dotted IPv4: 1.2.3.4 -> 4.3.2.1
			"4.3.2.1.whitelist.example": ipAddrs("127.0.0.1"),
		},
	})
	res, detail := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "pass" {
		t.Fatalf("exists: got %s (%s), want pass", res, detail)
	}
}

// TestSPF_NullSenderHELO is RED on the hand-rolled evaluator: a null sender
// (<>) gets no HELO-based check. Per RFC 7208 §2.4 the check uses the HELO
// identity (postmaster@<helo>, domain=<helo>).
func TestSPF_NullSenderHELO(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{
			"mail.example.com": {"v=spf1 ip4:1.2.3.4 -all"},
		},
	})
	f := &spfCheckFilter{}
	email := &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{
			ClientIP: "1.2.3.4",
			MailFrom: "",
			Helo:     "mail.example.com",
		},
	}
	res, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	if res.Log.Result != "pass" {
		t.Fatalf("null-sender HELO: got %s (%s), want pass", res.Log.Result, res.Log.Detail)
	}
}

// TestSPF_MXMatch exercises mx: with a resolver-returned host (trailing dot).
func TestSPF_MXMatch(t *testing.T) {
	useResolver(t, &stubResolver{
		txt: map[string][]string{"example.com": {"v=spf1 mx -all"}},
		mxs: map[string][]*net.MX{
			"example.com": {{Host: "mail.example.com.", Pref: 10}},
		},
		ips: map[string][]net.IPAddr{
			"mail.example.com.": ipAddrs("1.2.3.4"),
			"mail.example.com":  ipAddrs("1.2.3.4"),
		},
	})
	res, detail := checkSPF(context.Background(), "1.2.3.4", "example.com", "user@example.com", "")
	if res != "pass" {
		t.Fatalf("mx: got %s (%s), want pass", res, detail)
	}
}
