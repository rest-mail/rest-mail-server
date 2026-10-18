package filters

import (
	"context"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

// SPF itself (RFC 7208) is tested in github.com/rest-mail/go-spf. These tests
// cover the filter around it: the identity it takes from the envelope, the
// verdict it logs, and the Authentication-Results it writes.

// spfDNS is an in-memory DNS for the SPF filter's tests. It answers TXT queries
// from txt and address queries from addr; every other name does not exist.
// err, when set, answers every query; block makes every query wait for its
// context to end, as a hung authoritative server would.
type spfDNS struct {
	txt     map[string][]string
	addr    map[string][]netip.Addr
	err     error
	block   bool
	queries atomic.Int32
}

func (d *spfDNS) answer(ctx context.Context, name string) error {
	d.queries.Add(1)
	if d.block {
		<-ctx.Done()
		return &net.DNSError{Err: ctx.Err().Error(), Name: name, IsTimeout: true}
	}
	if d.err != nil {
		return d.err
	}
	return nil
}

func (d *spfDNS) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if err := d.answer(ctx, name); err != nil {
		return nil, err
	}
	if v, ok := d.txt[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (d *spfDNS) LookupNetIP(ctx context.Context, _, host string) ([]netip.Addr, error) {
	if err := d.answer(ctx, host); err != nil {
		return nil, err
	}
	if v, ok := d.addr[host]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (d *spfDNS) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	if err := d.answer(ctx, name); err != nil {
		return nil, err
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (d *spfDNS) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	if err := d.answer(ctx, addr); err != nil {
		return nil, err
	}
	return nil, &net.DNSError{Err: "no such host", Name: addr, IsNotFound: true}
}

// useSPFDNS makes d the SPF filter's DNS for the duration of the test.
func useSPFDNS(t *testing.T, d *spfDNS) {
	t.Helper()
	orig := spfResolver
	spfResolver = d
	t.Cleanup(func() { spfResolver = orig })
}

// runSPF runs the spf_check filter, with its default configuration, on a
// message with the given envelope, and returns the verdicts it recorded.
func runSPF(t *testing.T, env pipeline.Envelope) (*pipeline.EmailJSON, *pipeline.FilterResult, []pipeline.AuthResult) {
	t.Helper()
	email := &pipeline.EmailJSON{Envelope: env}
	f, err := NewSPFCheck(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authCtx()
	res, err := f.Execute(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionContinue {
		t.Errorf("action = %s, want continue: by default SPF alone never rejects", res.Action)
	}
	return email, res, pipeline.AuthResultsFromContext(ctx).List()
}

// wantSPFAuthResults checks the one Authentication-Results entry spf_check
// wrote.
func wantSPFAuthResults(t *testing.T, email *pipeline.EmailJSON, want string) {
	t.Helper()
	if got := email.Headers.Raw["Authentication-Results"]; !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("Raw Authentication-Results = %q, want [%q]", got, want)
	}
}

// wantVerdicts checks the verdicts spf_check recorded.
func wantVerdicts(t *testing.T, got []pipeline.AuthResult, want ...pipeline.AuthResult) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("verdicts = %+v, want %+v", got, want)
	}
}

// TestSPFCheck_VerdictReachesAuthResults checks that each result go-spf gives
// is the filter's logged result, its recorded verdict, and the spf= of the
// Authentication-Results it writes, with the checked domain as smtp.mailfrom.
func TestSPFCheck_VerdictReachesAuthResults(t *testing.T) {
	cases := []struct {
		record string // "" publishes nothing
		want   string
	}{
		{"v=spf1 ip4:192.0.2.1 -all", "pass"},
		{"v=spf1 ip4:198.51.100.1 -all", "fail"},
		{"v=spf1 ~all", "softfail"},
		{"v=spf1 ?all", "neutral"},
		{"", "none"},
		{"v=spf1 ip4:192.0.2.1 bogus -all", "permerror"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			dns := &spfDNS{txt: map[string][]string{}}
			if c.record != "" {
				dns.txt["example.com"] = []string{c.record}
			}
			useSPFDNS(t, dns)

			email, res, verdicts := runSPF(t, pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "user@example.com"})
			if res.Log.Filter != "spf_check" || res.Log.Result != c.want {
				t.Errorf("log = %s %s (%s), want spf_check %s", res.Log.Filter, res.Log.Result, res.Log.Detail, c.want)
			}
			if res.Log.Detail == "" {
				t.Error("log detail is empty; it should say how the result was reached")
			}
			wantVerdicts(t, verdicts, spfVerdict(c.want, "example.com"))
			wantSPFAuthResults(t, email, "restmail; spf="+c.want+" smtp.mailfrom=example.com")
		})
	}
}

// TestSPFCheck_AppendsAuthResults checks that the filter adds its entry after
// any Authentication-Results an earlier filter wrote, rather than replacing it.
func TestSPFCheck_AppendsAuthResults(t *testing.T) {
	useSPFDNS(t, &spfDNS{txt: map[string][]string{"example.com": {"v=spf1 ip4:192.0.2.1 -all"}}})
	email := &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "user@example.com"},
		Headers: pipeline.Headers{
			Raw: map[string][]string{"Authentication-Results": {"restmail; arc=none"}},
		},
	}
	f, _ := NewSPFCheck(nil)
	if _, err := f.Execute(context.Background(), email); err != nil {
		t.Fatal(err)
	}
	spfAR := "restmail; spf=pass smtp.mailfrom=example.com"
	if got := email.Headers.Raw["Authentication-Results"]; !reflect.DeepEqual(got, []string{"restmail; arc=none", spfAR}) {
		t.Errorf("Raw Authentication-Results = %q", got)
	}
}

// TestSPFCheck_DNSFailureIsTempError checks that a DNS failure other than "no
// such name" (here SERVFAIL) gives temperror, logged and in
// Authentication-Results.
func TestSPFCheck_DNSFailureIsTempError(t *testing.T) {
	useSPFDNS(t, &spfDNS{err: &net.DNSError{Err: "server misbehaving", Name: "example.com", IsTemporary: true}})
	email, res, _ := runSPF(t, pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "user@example.com"})
	if res.Log.Result != "temperror" {
		t.Fatalf("got %s (%s), want temperror", res.Log.Result, res.Log.Detail)
	}
	wantSPFAuthResults(t, email, "restmail; spf=temperror smtp.mailfrom=example.com")
}

// TestSPFCheck_Timeout checks that the filter bounds an evaluation with
// spfTimeout: DNS that never answers gives temperror instead of stalling the
// pipeline.
func TestSPFCheck_Timeout(t *testing.T) {
	orig := spfTimeout
	spfTimeout = 200 * time.Millisecond
	t.Cleanup(func() { spfTimeout = orig })
	useSPFDNS(t, &spfDNS{block: true})

	done := make(chan *pipeline.FilterResult, 1)
	go func() {
		f, _ := NewSPFCheck(nil)
		res, _ := f.Execute(context.Background(), &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "user@example.com"},
		})
		done <- res
	}()
	select {
	case res := <-done:
		if res.Log.Result != "temperror" {
			t.Fatalf("got %s (%s), want temperror", res.Log.Result, res.Log.Detail)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("spf_check did not return: the evaluation is not bounded by spfTimeout")
	}
}

// TestSPFCheck_NullSenderChecksHELO checks that for the null reverse-path the
// filter checks postmaster@<HELO> (RFC 7208 §2.4), records the HELO name as the
// checked MAIL FROM domain, and does not check the HELO identity twice.
func TestSPFCheck_NullSenderChecksHELO(t *testing.T) {
	useSPFDNS(t, &spfDNS{txt: map[string][]string{"mail.example.com": {"v=spf1 ip4:192.0.2.1 -all"}}})
	email, res, verdicts := runSPF(t, pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "", Helo: "mail.example.com"})
	if res.Log.Result != "pass" {
		t.Fatalf("got %s (%s), want pass", res.Log.Result, res.Log.Detail)
	}
	wantVerdicts(t, verdicts, spfVerdict("pass", "mail.example.com"))
	wantSPFAuthResults(t, email, "restmail; spf=pass smtp.mailfrom=mail.example.com")
}

// TestSPFCheck_HELO checks that with a MAIL FROM the HELO identity is checked
// as well (RFC 7208 §2.3) and recorded as its own verdict, before the MAIL
// FROM one, while the logged result stays the MAIL FROM's. An address-literal
// HELO is not checked.
func TestSPFCheck_HELO(t *testing.T) {
	useSPFDNS(t, &spfDNS{txt: map[string][]string{
		"mail.example.com": {"v=spf1 ip4:192.0.2.1 -all"},
		"example.com":      {"v=spf1 -all"},
	}})
	email, res, verdicts := runSPF(t, pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "user@example.com", Helo: "mail.example.com"})
	if res.Log.Result != "fail" || !strings.Contains(res.Log.Detail, "HELO mail.example.com: pass") {
		t.Errorf("log = %s (%s), want fail with the HELO result in the detail", res.Log.Result, res.Log.Detail)
	}
	wantVerdicts(t, verdicts,
		pipeline.AuthResult{Method: pipeline.AuthSPF, Result: "pass", Property: pipeline.PropHelo, Domain: "mail.example.com"},
		spfVerdict("fail", "example.com"))
	wantSPFAuthResults(t, email, "restmail; spf=pass smtp.helo=mail.example.com; spf=fail smtp.mailfrom=example.com")

	_, _, verdicts = runSPF(t, pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "user@example.com", Helo: "[192.0.2.1]"})
	wantVerdicts(t, verdicts, spfVerdict("fail", "example.com"))
}

// TestSPFCheck_FailAction checks "Action on SPF Fail": only a fail of the MAIL
// FROM identity acts, as configured; tag (the default) continues, and so does
// every other result, a HELO fail included. An unknown action is a config error.
func TestSPFCheck_FailAction(t *testing.T) {
	useSPFDNS(t, &spfDNS{txt: map[string][]string{
		"fail.example":     {"v=spf1 -all"},
		"softfail.example": {"v=spf1 ~all"},
		"pass.example":     {"v=spf1 +all"},
		"mx.fail.example":  {"v=spf1 -all"},
	}})
	cases := []struct {
		config, mailFrom, helo string
		want                   pipeline.Action
	}{
		{`{"fail_action":"reject"}`, "a@fail.example", "", pipeline.ActionReject},
		{`{"fail_action":"quarantine"}`, "a@fail.example", "", pipeline.ActionQuarantine},
		{`{"fail_action":"tag"}`, "a@fail.example", "", pipeline.ActionContinue},
		{`{}`, "a@fail.example", "", pipeline.ActionContinue},
		{``, "a@fail.example", "", pipeline.ActionContinue},
		{`{"fail_action":"reject"}`, "a@softfail.example", "", pipeline.ActionContinue},
		{`{"fail_action":"reject"}`, "a@pass.example", "mx.fail.example", pipeline.ActionContinue},
	}
	for _, c := range cases {
		f, err := NewSPFCheck([]byte(c.config))
		if err != nil {
			t.Fatalf("%s: %v", c.config, err)
		}
		email := &pipeline.EmailJSON{Envelope: pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: c.mailFrom, Helo: c.helo}}
		ctx := authCtx()
		res, err := f.Execute(ctx, email)
		if err != nil {
			t.Fatal(err)
		}
		if res.Action != c.want {
			t.Errorf("%s, %s HELO %q: action = %s, want %s", c.config, c.mailFrom, c.helo, res.Action, c.want)
		}
		if c.want == pipeline.ActionReject && res.RejectMsg != "550 5.7.23 SPF check failed for fail.example" {
			t.Errorf("reject message = %q", res.RejectMsg)
		}
		if len(pipeline.AuthResultsFromContext(ctx).List()) == 0 || len(email.Headers.Raw["Authentication-Results"]) != 1 {
			t.Errorf("%s: the verdict was not recorded before acting on it", c.config)
		}
	}
	for _, bad := range []string{`{"fail_action":"bounce"}`, `{"fail_action":`} {
		if _, err := NewSPFCheck([]byte(bad)); err == nil {
			t.Errorf("NewSPFCheck(%s) accepted a bad config", bad)
		}
	}
}

// TestSPFCheck_NoIdentity checks that with neither a MAIL FROM nor a HELO the
// result is none, with no DNS query, no verdict and no Authentication-Results:
// the header From is not an SPF identity (RFC 7208 §2.2), so it is not checked
// instead.
func TestSPFCheck_NoIdentity(t *testing.T) {
	dns := &spfDNS{txt: map[string][]string{"example.com": {"v=spf1 ip4:192.0.2.1 -all"}}}
	useSPFDNS(t, dns)
	email := &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{ClientIP: "192.0.2.1"},
		Headers:  pipeline.Headers{From: []pipeline.Address{{Address: "user@example.com"}}},
	}
	f, _ := NewSPFCheck(nil)
	ctx := authCtx()
	res, err := f.Execute(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if res.Log.Result != "none" {
		t.Errorf("got %s (%s), want none", res.Log.Result, res.Log.Detail)
	}
	if n := dns.queries.Load(); n != 0 {
		t.Errorf("made %d DNS queries, want none: the header From was checked", n)
	}
	wantVerdicts(t, pipeline.AuthResultsFromContext(ctx).List())
	if ar := email.Headers.Raw["Authentication-Results"]; len(ar) != 0 {
		t.Errorf("wrote Authentication-Results %q with nothing checked", ar)
	}
}

// TestSPFCheck_ClientIP checks how the filter takes the client address: none
// without one, and permerror for one that does not parse.
func TestSPFCheck_ClientIP(t *testing.T) {
	useSPFDNS(t, &spfDNS{txt: map[string][]string{"example.com": {"v=spf1 +all"}}})

	email, res, verdicts := runSPF(t, pipeline.Envelope{MailFrom: "user@example.com"})
	if res.Log.Result != "none" {
		t.Errorf("no client IP: got %s (%s), want none", res.Log.Result, res.Log.Detail)
	}
	wantVerdicts(t, verdicts)
	if ar := email.Headers.Raw["Authentication-Results"]; len(ar) != 0 {
		t.Errorf("no client IP: wrote Authentication-Results %q with nothing checked", ar)
	}

	email, res, _ = runSPF(t, pipeline.Envelope{ClientIP: "not-an-address", MailFrom: "user@example.com"})
	if res.Log.Result != "permerror" {
		t.Errorf("bad client IP: got %s (%s), want permerror", res.Log.Result, res.Log.Detail)
	}
	wantSPFAuthResults(t, email, "restmail; spf=permerror smtp.mailfrom=example.com")
}

// TestSPFCheck_AuthResultsCannotBeForged checks that nothing the sender or the
// SPF record chooses can make the verdict, or the Authentication-Results
// written from it, name a different result or domain: a "spf=pass" local-part
// on a failing message, a record whose text names another domain, and a MAIL
// FROM domain with a ";" in it that passes on the attacker's own DNS.
func TestSPFCheck_AuthResultsCannotBeForged(t *testing.T) {
	useSPFDNS(t, &spfDNS{txt: map[string][]string{
		"victim.example":                    {"v=spf1 -all"},
		"attacker.example":                  {"v=spf1 +exists:smtp.mailfrom=x@victim.example,.attacker.example"},
		"victim.example;x.attacker.example": {"v=spf1 +all"},
	}, addr: map[string][]netip.Addr{
		"smtp.mailfrom=x@victim.example,.attacker.example": {netip.MustParseAddr("127.0.0.2")},
	}})

	email, _, verdicts := runSPF(t, pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "spf=pass@victim.example"})
	wantVerdicts(t, verdicts, spfVerdict("fail", "victim.example"))
	wantSPFAuthResults(t, email, "restmail; spf=fail smtp.mailfrom=victim.example")
	if senderAuthenticated(authCtx(verdicts...), "victim.example") {
		t.Errorf("a spf=pass local-part authenticated victim.example: %+v", verdicts)
	}

	email, res, verdicts := runSPF(t, pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "a@attacker.example"})
	if res.Log.Result != "pass" {
		t.Fatalf("setup: got %s (%s), want pass on the attacker's record", res.Log.Result, res.Log.Detail)
	}
	wantVerdicts(t, verdicts, spfVerdict("pass", "attacker.example"))
	wantSPFAuthResults(t, email, "restmail; spf=pass smtp.mailfrom=attacker.example")
	if senderAuthenticated(authCtx(verdicts...), "victim.example") {
		t.Errorf("the record's text authenticated victim.example: %+v", verdicts)
	}

	email, res, verdicts = runSPF(t, pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "a@victim.example;x.attacker.example"})
	if res.Log.Result != "pass" {
		t.Fatalf("setup: got %s (%s), want pass on the attacker's DNS", res.Log.Result, res.Log.Detail)
	}
	wantVerdicts(t, verdicts, spfVerdict("pass", ""))
	wantSPFAuthResults(t, email, "restmail; spf=pass")
	if senderAuthenticated(authCtx(verdicts...), "victim.example") {
		t.Errorf("a ';' in the MAIL FROM domain authenticated victim.example: %+v", verdicts)
	}
}
