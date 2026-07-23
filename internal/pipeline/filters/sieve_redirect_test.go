package filters

// OSI-13: a Sieve `redirect` must not be usable to exfiltrate mail to an
// arbitrary external domain. These tests cover the allowlist policy enforced by
// the app-side executor (redirect to the recipient's own domain is always
// allowed; external targets are denied unless explicitly permitted).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// runSievePolicy runs a redirect script under a specific redirect policy and
// returns the recorded redirect_to metadata ("" when the redirect was denied).
func runSievePolicy(t *testing.T, policy SieveRedirectPolicy, target string) string {
	t.Helper()
	script := `if header :contains "Subject" "Test" {
  redirect "` + target + `";
}`
	cfg, _ := json.Marshal(sieveConfig{Script: script})
	f, err := NewSieveWithPolicy(policy)(cfg)
	if err != nil {
		t.Fatalf("NewSieveWithPolicy: %v", err)
	}
	result, err := f.Execute(context.Background(), sieveEmail())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Message == nil {
		return ""
	}
	return result.Message.Metadata["redirect_to"]
}

// TestSieveRedirect_SameDomainAlwaysAllowed: the recipient is
// recipient@example.com, so a redirect within example.com is internal and is
// permitted even under the deny-external default.
func TestSieveRedirect_SameDomainAlwaysAllowed(t *testing.T) {
	got := runSievePolicy(t, SieveRedirectPolicy{}, "colleague@example.com")
	if got != "colleague@example.com" {
		t.Fatalf("redirect_to = %q, want same-domain redirect to be allowed", got)
	}
}

// TestSieveRedirect_ExternalDeniedByDefault: the secure default drops an
// external redirect (mail-exfiltration guard).
func TestSieveRedirect_ExternalDeniedByDefault(t *testing.T) {
	got := runSievePolicy(t, SieveRedirectPolicy{}, "attacker@evil.com")
	if got != "" {
		t.Fatalf("redirect_to = %q, want external redirect denied by default", got)
	}
}

// TestSieveRedirect_AllowlistedExternalAllowed: an explicitly allowlisted
// external domain is permitted.
func TestSieveRedirect_AllowlistedExternalAllowed(t *testing.T) {
	policy := SieveRedirectPolicy{AllowedDomains: []string{"partner.org"}}
	got := runSievePolicy(t, policy, "team@partner.org")
	if got != "team@partner.org" {
		t.Fatalf("redirect_to = %q, want allowlisted external redirect to be allowed", got)
	}
}

// TestSieveRedirect_AllowlistCaseInsensitive: allowlist matching is
// case-insensitive on the domain.
func TestSieveRedirect_AllowlistCaseInsensitive(t *testing.T) {
	policy := SieveRedirectPolicy{AllowedDomains: []string{"Partner.ORG"}}
	got := runSievePolicy(t, policy, "team@partner.org")
	if got != "team@partner.org" {
		t.Fatalf("redirect_to = %q, want case-insensitive allowlist match", got)
	}
}

// TestSieveRedirect_NonAllowlistedExternalDenied: an external domain not on the
// allowlist is still denied.
func TestSieveRedirect_NonAllowlistedExternalDenied(t *testing.T) {
	policy := SieveRedirectPolicy{AllowedDomains: []string{"partner.org"}}
	got := runSievePolicy(t, policy, "attacker@evil.com")
	if got != "" {
		t.Fatalf("redirect_to = %q, want non-allowlisted external redirect denied", got)
	}
}

// TestSieveRedirect_AllowExternalPermitsAll: AllowExternal restores the legacy
// permissive behavior.
func TestSieveRedirect_AllowExternalPermitsAll(t *testing.T) {
	policy := SieveRedirectPolicy{AllowExternal: true}
	got := runSievePolicy(t, policy, "anyone@anywhere.example")
	if got != "anyone@anywhere.example" {
		t.Fatalf("redirect_to = %q, want AllowExternal to permit any target", got)
	}
}

// TestRecipientDomains verifies the internal-domain derivation used to classify
// a redirect target.
func TestRecipientDomains(t *testing.T) {
	// Envelope wins.
	got := recipientDomains(&pipeline.EmailJSON{
		Envelope: pipeline.Envelope{RcptTo: []string{"a@corp.example", "b@corp.example"}},
		Headers:  pipeline.Headers{To: []pipeline.Address{{Address: "c@other.example"}}},
	})
	if len(got) != 1 || got[0] != "corp.example" {
		t.Fatalf("recipientDomains(envelope) = %v, want [corp.example]", got)
	}

	// Falls back to To/Cc headers when the envelope is empty.
	got = recipientDomains(&pipeline.EmailJSON{
		Headers: pipeline.Headers{
			To: []pipeline.Address{{Address: "c@other.example"}},
			Cc: []pipeline.Address{{Address: "d@cc.example"}},
		},
	})
	if len(got) != 2 || got[0] != "other.example" || got[1] != "cc.example" {
		t.Fatalf("recipientDomains(headers) = %v, want [other.example cc.example]", got)
	}
}
