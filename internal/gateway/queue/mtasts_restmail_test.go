package queue

import (
	"testing"

	mtasts "github.com/rest-mail/go-mtasts"
)

// TestRESTMAILUpgradePermitted_EnforceGate is the red-green guard for #168: the
// plaintext, EHLO-advertised RESTMAIL upgrade must be gated by the recipient's
// MTA-STS policy. Before the fix the upgrade was ALWAYS taken — the STS checks
// guarded only the SMTP fallback — so an on-path attacker could inject a rogue
// "RESTMAIL <url>" capability over cleartext port 25 and intercept all mail to an
// enforce-mode domain, exactly what MTA-STS exists to prevent.
//
// restmailUpgradePermitted is the single decision point both the cached-capability
// path and the fresh EHLO probe consult before delivering over the RESTMAIL HTTPS
// hop. Under an enforce-mode policy it permits the upgrade only when the endpoint
// is served over HTTPS and its host is named by the (MTA-STS-authenticated) policy
// MX set; every other endpoint is refused and delivery falls back to the
// policy-guarded SMTP path.
func TestRESTMAILUpgradePermitted_EnforceGate(t *testing.T) {
	enforce := &mtasts.Policy{Mode: mtasts.ModeEnforce, MX: []string{"mx1.example.com", "*.mx.example.com"}, MaxAge: 86400}
	testingPol := &mtasts.Policy{Mode: mtasts.ModeTesting, MX: []string{"mx1.example.com"}, MaxAge: 86400}

	cases := []struct {
		name      string
		url       string
		policy    *mtasts.Policy
		enforcing bool
		want      bool
	}{
		// --- Under enforce, the bug: a rogue endpoint must be refused. ---
		{
			name:      "enforce: attacker endpoint not named by policy -> refuse",
			url:       "https://attacker.evil/restmail",
			policy:    enforce,
			enforcing: true,
			want:      false,
		},
		{
			name:      "enforce: cleartext http endpoint (even if host named) -> refuse",
			url:       "http://mx1.example.com/restmail",
			policy:    enforce,
			enforcing: true,
			want:      false,
		},
		{
			name:      "enforce: scheme-less endpoint whose host is not named -> refuse",
			url:       "attacker.evil/restmail",
			policy:    enforce,
			enforcing: true,
			want:      false,
		},
		{
			name:      "enforce: endpoint host is an IP (never named by an MX policy) -> refuse",
			url:       "https://203.0.113.7/restmail",
			policy:    enforce,
			enforcing: true,
			want:      false,
		},
		{
			name:      "enforcing but nil policy (defensive) -> refuse",
			url:       "https://mx1.example.com/restmail",
			policy:    nil,
			enforcing: true,
			want:      false,
		},
		// --- Under enforce, a legitimately policy-named HTTPS endpoint is allowed. ---
		{
			name:      "enforce: endpoint named by policy over https -> permit",
			url:       "https://mx1.example.com/restmail",
			policy:    enforce,
			enforcing: true,
			want:      true,
		},
		{
			name:      "enforce: wildcard-named endpoint over https -> permit",
			url:       "https://a.mx.example.com/restmail",
			policy:    enforce,
			enforcing: true,
			want:      true,
		},
		{
			name:      "enforce: named endpoint with explicit port -> permit",
			url:       "https://mx1.example.com:8443/restmail",
			policy:    enforce,
			enforcing: true,
			want:      true,
		},
		{
			name:      "enforce: scheme-less endpoint defaults to https and host is named -> permit",
			url:       "mx1.example.com/restmail",
			policy:    enforce,
			enforcing: true,
			want:      true,
		},
		// --- Not enforcing: opportunistic behavior is unchanged (upgrade allowed). ---
		{
			name:      "testing mode (not enforcing): rogue endpoint still permitted (STS testing never blocks)",
			url:       "https://attacker.evil/restmail",
			policy:    testingPol,
			enforcing: false,
			want:      true,
		},
		{
			name:      "no policy: upgrade permitted",
			url:       "http://whatever.example/restmail",
			policy:    nil,
			enforcing: false,
			want:      true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := restmailUpgradePermitted(c.url, c.policy, c.enforcing); got != c.want {
				t.Errorf("restmailUpgradePermitted(%q, enforcing=%v) = %v, want %v",
					c.url, c.enforcing, got, c.want)
			}
		})
	}
}
