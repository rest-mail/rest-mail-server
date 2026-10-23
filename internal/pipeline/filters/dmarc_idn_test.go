package filters

import (
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

func TestDMARC_IDNPolicyLookup(t *testing.T) {
	for _, tc := range []struct {
		name, from, domain string
	}{
		{"Unicode", "用户@bücher.test", "xn--bcher-kva.test"},
		{"A-label", "user@xn--bcher-kva.test", "xn--bcher-kva.test"},
		{"decomposed", "user@bu\u0308cher.test", "xn--bcher-kva.test"},
		{"case", "user@BÜCHER.TEST", "xn--bcher-kva.test"},
		{"nontransitional", "user@faß.de", "xn--fa-hia.de"},
		{"quoted local part", "\"local@part\"@bu\u0308cher.test", "xn--bcher-kva.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var queries []string
			stubDMARC(t, func(domain string) string {
				queries = append(queries, domain)
				if domain == tc.domain {
					return "v=DMARC1; p=reject; aspf=s; adkim=s"
				}
				return ""
			})
			for _, verdict := range []pipeline.AuthResult{spfVerdict("pass", tc.domain), dkimVerdict("pass", tc.domain), spfVerdict("fail", tc.domain)} {
				queries = nil
				ctx := authCtx(verdict)
				email := fromAddress(tc.from)
				res, err := (&dmarcCheckFilter{}).Execute(ctx, email)
				if err != nil {
					t.Fatal(err)
				}
				want, action := "pass", pipeline.ActionContinue
				if verdict.Result == "fail" {
					want, action = "fail", pipeline.ActionReject
				}
				if res.Log.Result != want || res.Action != action {
					t.Errorf("%+v: got %s/%s, want %s/%s", verdict, res.Log.Result, res.Action, want, action)
				}
				if !reflect.DeepEqual(queries, []string{tc.domain}) {
					t.Errorf("DNS queries = %q, want only %q", queries, tc.domain)
				}
				got := pipeline.AuthResultsFromContext(ctx).Find(pipeline.AuthDMARC, pipeline.PropHeaderFrom)
				if len(got) != 1 || got[0].Domain != tc.domain || got[0].Result != want {
					t.Errorf("DMARC verdict = %+v, want %s for %s", got, want, tc.domain)
				}
				if email.Headers.From[0].Address != tc.from {
					t.Errorf("original From changed to %q", email.Headers.From[0].Address)
				}
			}
		})
	}
}

func TestDMARC_IDNAlignment(t *testing.T) {
	stubDMARC(t, func(string) string { return "v=DMARC1; p=reject" })
	for _, tc := range []struct {
		name, domain string
		want         bool
	}{
		{"Unicode", "bücher.test", true},
		{"decomposed", "bu\u0308cher.test", true},
		{"A-label", "xn--bcher-kva.test", true},
		{"root dot", "xn--bcher-kva.test.", true},
		{"unrelated", "other.test", false},
		{"missing", "", false},
		{"joiner", "a\u200db.bücher.test", false},
		{"invalid A-label", "xn--.bücher.test", false},
		{"underscore", "bad_.bücher.test", false},
		{"invalid UTF-8", "\xff.bücher.test", false},
		{"too long", strings.Repeat("a", 64) + ".bücher.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, verdict := range []pipeline.AuthResult{spfVerdict("pass", tc.domain), dkimVerdict("pass", tc.domain)} {
				ctx := authCtx(verdict)
				res, err := (&dmarcCheckFilter{}).Execute(ctx, fromAddress("用户@xn--bcher-kva.test"))
				if err != nil {
					t.Fatal(err)
				}
				if got := res.Log.Result == "pass"; got != tc.want {
					t.Errorf("%+v: DMARC pass = %t, want %t", verdict, got, tc.want)
				}
				if got := senderAuthenticated(ctx, "bücher.test"); got != tc.want {
					t.Errorf("%+v: senderAuthenticated = %t, want %t", verdict, got, tc.want)
				}
			}
		})
	}
}

func TestDMARC_IDNInvalidFrom(t *testing.T) {
	for _, domain := range []string{"a\u200db.test", "xn--.test", "\xff.test", "bad_.test", "a..test"} {
		t.Run(domain, func(t *testing.T) {
			stubDMARC(t, func(domain string) string {
				t.Errorf("invalid domain reached DNS: %q", domain)
				return "v=DMARC1; p=reject"
			})
			ctx := authCtx(dkimVerdict("pass", domain))
			res, err := (&dmarcCheckFilter{}).Execute(ctx, fromAddress("user@"+domain))
			if err != nil || res.Log.Result != "none" || res.Action != pipeline.ActionContinue {
				t.Fatalf("invalid From result = %+v, %v", res, err)
			}
			if senderAuthenticated(ctx, domain) {
				t.Error("invalid domain authenticated")
			}
		})
	}
}

func TestDMARC_IDNOrganizationalPolicy(t *testing.T) {
	var queries []string
	stubDMARC(t, func(domain string) string {
		queries = append(queries, domain)
		if domain == "xn--bcher-kva.co.uk" {
			return "v=DMARC1; p=none; sp=reject"
		}
		return ""
	})
	for _, verdict := range []pipeline.AuthResult{dkimVerdict("pass", "bounce.bu\u0308cher.co.uk"), spfVerdict("pass", "bounce.xn--bcher-kva.co.uk"), dkimVerdict("pass", "other.co.uk")} {
		queries = nil
		res, err := (&dmarcCheckFilter{}).Execute(authCtx(verdict), fromAddress("ceo@mail.bu\u0308cher.co.uk"))
		if err != nil {
			t.Fatal(err)
		}
		want := pipeline.ActionContinue
		if verdict.Domain == "other.co.uk" {
			want = pipeline.ActionReject
		}
		if res.Action != want {
			t.Errorf("%+v: action = %s, want %s", verdict, res.Action, want)
		}
		if !reflect.DeepEqual(queries, []string{"mail.xn--bcher-kva.co.uk", "xn--bcher-kva.co.uk"}) {
			t.Errorf("policy queries = %q", queries)
		}
	}
}
