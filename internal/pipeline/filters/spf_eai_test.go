package filters

import (
	"net/netip"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

func TestSPFCheck_IDNMailFrom(t *testing.T) {
	for _, tc := range []struct {
		name, sender, domain, ip, want string
	}{
		{"Unicode domain", "user@bücher.example", "xn--bcher-kva.example", "192.0.2.1", "pass"},
		{"A-label", "user@xn--bcher-kva.example", "xn--bcher-kva.example", "192.0.2.1", "pass"},
		{"decomposed domain", "用户@bu\u0308cher.example", "xn--bcher-kva.example", "192.0.2.1", "pass"},
		{"nontransitional mapping", "用户@faß.de", "xn--fa-hia.de", "192.0.2.1", "pass"},
		{"IDN policy fails", "用户@bücher.example", "xn--bcher-kva.example", "198.51.100.1", "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useSPFDNS(t, &spfDNS{txt: map[string][]string{
				tc.domain: {"v=spf1 ip4:192.0.2.1 -all"},
			}})
			env := pipeline.Envelope{ClientIP: tc.ip, MailFrom: tc.sender}
			email, res, verdicts := runSPF(t, env)
			if res.Log.Result != tc.want {
				t.Fatalf("SPF = %s (%s), want %s", res.Log.Result, res.Log.Detail, tc.want)
			}
			wantVerdicts(t, verdicts, spfVerdict(tc.want, tc.domain))
			wantSPFAuthResults(t, email, "restmail; spf="+tc.want+" smtp.mailfrom="+tc.domain)
			if !reflect.DeepEqual(email.Envelope, env) {
				t.Fatalf("SPF rewrote envelope: %+v", email.Envelope)
			}
		})
	}
}

func TestSPFCheck_IDNHELO(t *testing.T) {
	for _, sender := range []string{"", "user@example.com"} {
		t.Run("sender="+sender, func(t *testing.T) {
			useSPFDNS(t, &spfDNS{txt: map[string][]string{
				"xn--bcher-kva.example": {"v=spf1 ip4:192.0.2.1 -all"},
				"example.com":           {"v=spf1 -all"},
			}})
			env := pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: sender, Helo: "bücher.example"}
			email, _, verdicts := runSPF(t, env)
			if sender == "" {
				wantVerdicts(t, verdicts, spfVerdict("pass", "xn--bcher-kva.example"))
				wantSPFAuthResults(t, email, "restmail; spf=pass smtp.mailfrom=xn--bcher-kva.example")
			} else {
				wantVerdicts(t, verdicts,
					pipeline.AuthResult{Method: pipeline.AuthSPF, Result: "pass", Property: pipeline.PropHelo, Domain: "xn--bcher-kva.example"},
					spfVerdict("fail", "example.com"))
			}
			if !reflect.DeepEqual(email.Envelope, env) {
				t.Fatalf("SPF rewrote envelope: %+v", email.Envelope)
			}
		})
	}
}

func TestSPFCheck_IDNInvalidDomains(t *testing.T) {
	for _, domain := range []string{"bücher..example", "a\u200db.example", "\xff.example"} {
		t.Run(domain, func(t *testing.T) {
			dns := &spfDNS{}
			useSPFDNS(t, dns)
			for _, env := range []pipeline.Envelope{
				{ClientIP: "192.0.2.1", MailFrom: "user@" + domain},
				{ClientIP: "192.0.2.1", Helo: domain},
			} {
				email, res, verdicts := runSPF(t, env)
				if res.Log.Result != "none" {
					t.Fatalf("invalid domain SPF = %s, want none", res.Log.Result)
				}
				wantVerdicts(t, verdicts, spfVerdict("none", ""))
				wantSPFAuthResults(t, email, "restmail; spf=none")
			}
			if got := dns.queries.Load(); got != 0 {
				t.Fatalf("invalid IDN made %d DNS queries", got)
			}
		})
	}
}

func TestSPFCheck_IDNFailAction(t *testing.T) {
	useSPFDNS(t, &spfDNS{txt: map[string][]string{
		"xn--bcher-kva.example": {"v=spf1 -all"},
	}})
	f, err := NewSPFCheck([]byte(`{"fail_action":"reject"}`))
	if err != nil {
		t.Fatal(err)
	}
	email := &pipeline.EmailJSON{Envelope: pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "用户@bücher.example"}}
	res, err := f.Execute(authCtx(), email)
	if err != nil || res.Action != pipeline.ActionReject || res.RejectMsg != "550 5.7.23 SPF check failed for xn--bcher-kva.example" {
		t.Fatalf("SPF = %+v, %v; want IDN policy rejection", res, err)
	}
}

// The SPF macro lookup proves the remote local part retains its case and
// decomposed UTF-8 bytes, while both domain macros use the DNS spelling.
// The resulting A-label verdict must align with a Unicode From domain.
func TestSPFCheck_IDNMacrosAndDMARC(t *testing.T) {
	useSPFDNS(t, &spfDNS{txt: map[string][]string{
		"xn--bcher-kva.example": {"v=spf1 exists:%{L}.%{o}.%{d}.%{h} -all"},
	}, addr: map[string][]netip.Addr{
		"Mu%CC%88nchen.xn--bcher-kva.example.xn--bcher-kva.example.mx.xn--bcher-kva.example": {netip.MustParseAddr("192.0.2.1")},
	}})
	stubDMARC(t, func(domain string) string {
		if domain == "xn--bcher-kva.example" {
			return "v=DMARC1; p=reject; aspf=s"
		}
		return ""
	})
	ctx := authCtx()
	email := fromAddress("用户@bücher.example")
	email.Envelope = pipeline.Envelope{ClientIP: "192.0.2.1", MailFrom: "Mu\u0308nchen@bücher.example", Helo: "mx.bücher.example"}
	spfFilter, _ := NewSPFCheck(nil)
	res, err := spfFilter.Execute(ctx, email)
	if err != nil || res.Log.Result != "pass" {
		t.Fatalf("SPF = %+v, %v; want pass", res, err)
	}
	res, err = (&dmarcCheckFilter{}).Execute(ctx, email)
	if err != nil || res.Action != pipeline.ActionContinue || res.Log.Result != "pass" {
		t.Fatalf("DMARC = %+v, %v; want aligned pass", res, err)
	}
}
