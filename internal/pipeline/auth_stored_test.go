package pipeline

import (
	"reflect"
	"testing"
)

func TestParseStoredAuthResultsReadsWhatDeliveryWrites(t *testing.T) {
	want := []AuthResult{
		{Method: AuthSPF, Result: "pass", Property: PropMailFrom, Domain: "example.com"},
		{Method: AuthDKIM, Result: "pass", Property: PropHeaderD, Domain: "example.com"},
		{Method: AuthARC, Result: "none"},
		{Method: AuthDMARC, Result: "fail", Property: PropHeaderFrom, Domain: "example.org"},
	}
	raw := AuthResultsHeader(want) + "From: a@example.org\r\n\r\nbody\r\n"
	got, ok := ParseStoredAuthResults(raw)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v ok=%v, want %+v", got, ok, want)
	}
}

func TestParseStoredAuthResultsIgnoresAnythingElse(t *testing.T) {
	for name, raw := range map[string]string{
		"no raw message":     "",
		"no results":         "From: a@example.org\r\n\r\nbody\r\n",
		"another server":     "Authentication-Results: mx.example.net; dkim=pass\r\nFrom: a@example.org\r\n\r\n",
		"not the first":      "From: a@example.org\r\nAuthentication-Results: restmail; dkim=pass\r\n\r\n",
		"in a folded header": "Subject: a\r\n Authentication-Results: restmail; dkim=pass\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := ParseStoredAuthResults(raw); ok || got != nil {
				t.Fatalf("got %+v ok=%v", got, ok)
			}
		})
	}
}

func TestAuthVerdict(t *testing.T) {
	dkim := func(result string) AuthResult { return AuthResult{Method: AuthDKIM, Result: result} }
	dmarc := func(result string) AuthResult { return AuthResult{Method: AuthDMARC, Result: result} }
	for _, tc := range []struct {
		name string
		in   []AuthResult
		want string
	}{
		{"nothing checked", nil, AuthVerdictUnsigned},
		{"a signature verified", []AuthResult{dkim("pass"), dmarc("pass")}, AuthVerdictSigned},
		{"no DMARC policy is not a failure", []AuthResult{dkim("pass"), dmarc("none")}, AuthVerdictSigned},
		{"a broken signature alone is not a failure", []AuthResult{dkim("fail"), dmarc("none")}, AuthVerdictUnsigned},
		{"DMARC failed", []AuthResult{dkim("pass"), dmarc("fail")}, AuthVerdictMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AuthVerdict(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
