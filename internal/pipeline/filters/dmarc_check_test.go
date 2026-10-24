package filters

import (
	"context"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// authCtx returns a context carrying verdicts, as Engine.Execute's would once
// spf_check, dkim_verify and arc_verify had recorded them.
func authCtx(verdicts ...pipeline.AuthResult) context.Context {
	a := &pipeline.AuthResults{}
	a.Add(verdicts...)
	return pipeline.WithAuthResults(context.Background(), a)
}

// spfVerdict is spf_check's verdict on the MAIL FROM identity of domain.
func spfVerdict(result, domain string) pipeline.AuthResult {
	return pipeline.AuthResult{Method: pipeline.AuthSPF, Result: result, Property: pipeline.PropMailFrom, Domain: domain}
}

// dkimVerdict is dkim_verify's verdict on a signature with d=domain.
func dkimVerdict(result, domain string) pipeline.AuthResult {
	return pipeline.AuthResult{Method: pipeline.AuthDKIM, Result: result, Property: pipeline.PropHeaderD, Domain: domain}
}

// stubDMARC makes DMARC discovery see the record publishing(domain) gives for
// _dmarc.<domain>, "" for none.
func stubDMARC(t *testing.T, publishing func(domain string) string) {
	t.Helper()
	orig := dmarcTXT
	dmarcTXT = func(name string) ([]string, error) {
		if rec := publishing(strings.TrimPrefix(name, "_dmarc.")); rec != "" {
			return []string{rec}, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { dmarcTXT = orig })
}

// belowTLD publishes rec at every name below a top-level domain, and nothing at
// a TLD itself, so each such name is its own Organizational Domain unless one
// above it publishes too (RFC 9989 §4.10.2).
func belowTLD(rec string) func(string) string {
	return func(domain string) string {
		if !strings.Contains(domain, ".") {
			return ""
		}
		return rec
	}
}

// onlyAt publishes rec at the domains given and nowhere else.
func onlyAt(rec string, domains ...string) func(string) string {
	return func(domain string) string {
		for _, d := range domains {
			if domain == d {
				return rec
			}
		}
		return ""
	}
}

// fromExampleTest is a message From example.test.
func fromExampleTest() *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Headers: pipeline.Headers{From: []pipeline.Address{{Address: "user@example.test"}}},
	}
}

func TestDMARC_DKIMAlignment(t *testing.T) {
	stubDMARC(t, belowTLD("v=DMARC1; p=reject"))

	f := &dmarcCheckFilter{}

	cases := []struct {
		name       string
		verdicts   []pipeline.AuthResult
		wantAction pipeline.Action
		wantResult string
	}{
		{
			name:       "aligned dkim passes DMARC",
			verdicts:   []pipeline.AuthResult{dkimVerdict("pass", "example.test")},
			wantAction: pipeline.ActionContinue,
			wantResult: "pass",
		},
		{
			// A DKIM pass whose signing domain isn't aligned must NOT satisfy
			// DMARC.
			name:       "unaligned dkim domain is rejected under p=reject",
			verdicts:   []pipeline.AuthResult{dkimVerdict("pass", "evil.test")},
			wantAction: pipeline.ActionReject,
			wantResult: "fail",
		},
		{
			// A dkim pass with no signing domain does not count as aligned.
			name:       "dkim pass without a domain is not aligned",
			verdicts:   []pipeline.AuthResult{dkimVerdict("pass", "")},
			wantAction: pipeline.ActionReject,
			wantResult: "fail",
		},
		{
			// Any one passing, aligned signature is enough (RFC 7489 §4.2).
			name:       "one aligned signature among several",
			verdicts:   []pipeline.AuthResult{dkimVerdict("fail", "example.test"), dkimVerdict("pass", "esp.test"), dkimVerdict("pass", "mail.example.test")},
			wantAction: pipeline.ActionContinue,
			wantResult: "pass",
		},
		{
			name:       "aligned dkim that failed",
			verdicts:   []pipeline.AuthResult{dkimVerdict("fail", "example.test")},
			wantAction: pipeline.ActionReject,
			wantResult: "fail",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := f.Execute(authCtx(tc.verdicts...), fromExampleTest())
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != tc.wantAction {
				t.Errorf("action: got %s want %s (detail: %s)", res.Action, tc.wantAction, res.Log.Detail)
			}
			if res.Log.Result != tc.wantResult {
				t.Errorf("result: got %s want %s", res.Log.Result, tc.wantResult)
			}
		})
	}
}

func TestDMARC_SPFAlignment(t *testing.T) {
	stubDMARC(t, belowTLD("v=DMARC1; p=reject"))

	f := &dmarcCheckFilter{}

	cases := []struct {
		name     string
		verdicts []pipeline.AuthResult
		want     pipeline.Action
	}{
		// SPF-aligned alone satisfies DMARC even with no DKIM.
		{"aligned pass", []pipeline.AuthResult{spfVerdict("pass", "example.test")}, pipeline.ActionContinue},
		{"aligned subdomain pass", []pipeline.AuthResult{spfVerdict("pass", "bounce.example.test")}, pipeline.ActionContinue},
		{"unaligned pass", []pipeline.AuthResult{spfVerdict("pass", "attacker.test")}, pipeline.ActionReject},
		{"aligned softfail", []pipeline.AuthResult{spfVerdict("softfail", "example.test")}, pipeline.ActionReject},
		{"pass with no domain", []pipeline.AuthResult{spfVerdict("pass", "")}, pipeline.ActionReject},
		// The HELO identity is not DMARC's SPF identifier (RFC 7489 §3.1.2).
		{"aligned HELO pass only", []pipeline.AuthResult{
			{Method: pipeline.AuthSPF, Result: "pass", Property: pipeline.PropHelo, Domain: "example.test"},
			spfVerdict("fail", "attacker.test"),
		}, pipeline.ActionReject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := f.Execute(authCtx(tc.verdicts...), fromExampleTest())
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != tc.want {
				t.Errorf("action = %s, want %s (result %s, detail %q)", res.Action, tc.want, res.Log.Result, res.Log.Detail)
			}
		})
	}
}

// TestDMARC_RecordsVerdict checks that dmarc_check records its own verdict on
// the From domain, for the filters after it and the stored
// Authentication-Results: pass, fail, or none without a policy.
func TestDMARC_RecordsVerdict(t *testing.T) {
	f := &dmarcCheckFilter{}

	cases := []struct {
		name     string
		record   string
		verdicts []pipeline.AuthResult
		want     string
	}{
		{"pass", "v=DMARC1; p=reject", []pipeline.AuthResult{spfVerdict("pass", "example.test")}, "pass"},
		{"fail", "v=DMARC1; p=none", []pipeline.AuthResult{spfVerdict("fail", "example.test")}, "fail"},
		{"no record", "", nil, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubDMARC(t, func(string) string { return tc.record })
			ctx := authCtx(tc.verdicts...)
			if _, err := f.Execute(ctx, fromExampleTest()); err != nil {
				t.Fatal(err)
			}
			got := pipeline.AuthResultsFromContext(ctx).Find(pipeline.AuthDMARC, pipeline.PropHeaderFrom)
			want := []pipeline.AuthResult{{Method: pipeline.AuthDMARC, Result: tc.want, Property: pipeline.PropHeaderFrom, Domain: "example.test"}}
			if len(got) != 1 || got[0] != want[0] {
				t.Errorf("recorded %+v, want %+v", got, want)
			}
		})
	}
}

// TestDMARC_PolicyIsTheOnlyAuthority proves a DMARC fail does what the From
// domain's published policy asks and nothing else. dmarc_check has no setting
// of its own (#396): the admin UI's old "Action on DMARC Fail" was never read,
// and a pipeline that still stores fail_action can neither weaken p=reject or
// p=quarantine nor turn p=none into an enforcement.
func TestDMARC_PolicyIsTheOnlyAuthority(t *testing.T) {
	cases := []struct {
		policy, config string
		want           pipeline.Action
	}{
		{"reject", `{"fail_action":"tag"}`, pipeline.ActionReject},
		{"reject", `{}`, pipeline.ActionReject},
		{"quarantine", `{"fail_action":"tag"}`, pipeline.ActionQuarantine},
		{"quarantine", `{"fail_action":"reject"}`, pipeline.ActionQuarantine},
		{"none", `{"fail_action":"reject"}`, pipeline.ActionContinue},
		{"none", `{"fail_action":"quarantine"}`, pipeline.ActionContinue},
	}
	for _, tc := range cases {
		t.Run(tc.policy+" "+tc.config, func(t *testing.T) {
			stubDMARC(t, belowTLD("v=DMARC1; p="+tc.policy))
			f, err := NewDMARCCheck([]byte(tc.config))
			if err != nil {
				t.Fatalf("NewDMARCCheck(%s): %v", tc.config, err)
			}
			ctx := authCtx(spfVerdict("fail", "example.test"), dkimVerdict("fail", "example.test"))
			res, err := f.Execute(ctx, fromExampleTest())
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != tc.want || res.Log.Result != "fail" {
				t.Fatalf("action %q result %q, want %q and fail", res.Action, res.Log.Result, tc.want)
			}
		})
	}
}
