package handlers

import "testing"

// TestRestmailDeliverAuth_Authorize covers the OSI-3 accept/reject decision
// matrix for the RESTMAIL delivery gate.
func TestRestmailDeliverAuth_Authorize(t *testing.T) {
	const localFrom = "example.com"

	cases := []struct {
		name         string
		cfg          RestmailDeliverAuthConfig
		clientIP     string
		fromDomain   string
		fromLocal    bool
		dkimAligned  bool
		wantAccepted bool
	}{
		{
			name:         "disabled accepts spoofed local sender",
			cfg:          RestmailDeliverAuthConfig{Enabled: false},
			clientIP:     "203.0.113.9",
			fromDomain:   localFrom,
			fromLocal:    true,
			dkimAligned:  false,
			wantAccepted: true,
		},
		{
			name:         "trusted peer bypasses DKIM even for local sender",
			cfg:          RestmailDeliverAuthConfig{Enabled: true, TrustedCIDRs: []string{"203.0.113.0/24"}},
			clientIP:     "203.0.113.9",
			fromDomain:   localFrom,
			fromLocal:    true,
			dkimAligned:  false,
			wantAccepted: true,
		},
		{
			name:         "aligned DKIM accepts local sender",
			cfg:          RestmailDeliverAuthConfig{Enabled: true},
			clientIP:     "203.0.113.9",
			fromDomain:   localFrom,
			fromLocal:    true,
			dkimAligned:  true,
			wantAccepted: true,
		},
		{
			name:         "unauthenticated local sender rejected (BEC)",
			cfg:          RestmailDeliverAuthConfig{Enabled: true},
			clientIP:     "203.0.113.9",
			fromDomain:   localFrom,
			fromLocal:    true,
			dkimAligned:  false,
			wantAccepted: false,
		},
		{
			name:         "external unsigned sender accepted in non-strict",
			cfg:          RestmailDeliverAuthConfig{Enabled: true},
			clientIP:     "203.0.113.9",
			fromDomain:   "sender.net",
			fromLocal:    false,
			dkimAligned:  false,
			wantAccepted: true,
		},
		{
			name:         "strict rejects external unsigned untrusted sender",
			cfg:          RestmailDeliverAuthConfig{Enabled: true, Strict: true},
			clientIP:     "203.0.113.9",
			fromDomain:   "sender.net",
			fromLocal:    false,
			dkimAligned:  false,
			wantAccepted: false,
		},
		{
			name:         "strict accepts aligned external sender",
			cfg:          RestmailDeliverAuthConfig{Enabled: true, Strict: true},
			clientIP:     "203.0.113.9",
			fromDomain:   "sender.net",
			fromLocal:    false,
			dkimAligned:  true,
			wantAccepted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newRestmailDeliverAuth(tc.cfg)
			got, reason := a.authorize(tc.clientIP, tc.fromDomain, tc.fromLocal, tc.dkimAligned)
			if got != tc.wantAccepted {
				t.Fatalf("authorize = %v (%q), want %v", got, reason, tc.wantAccepted)
			}
		})
	}
}

// TestRestmailDeliverAuth_Trusted verifies CIDR / bare-IP parsing and matching,
// including that an invalid entry is dropped (never widens trust).
func TestRestmailDeliverAuth_Trusted(t *testing.T) {
	a := newRestmailDeliverAuth(RestmailDeliverAuthConfig{
		Enabled: true,
		TrustedCIDRs: []string{
			"10.0.0.0/8",
			"192.168.1.5", // bare IPv4 -> /32
			"2001:db8::/32",
			"",           // empty ignored
			"not-a-cidr", // invalid dropped
		},
	})

	cases := map[string]bool{
		"10.4.5.6":    true,
		"192.168.1.5": true,
		"192.168.1.6": false,
		"2001:db8::1": true,
		"203.0.113.1": false,
		"not-an-ip":   false,
	}
	for ip, want := range cases {
		if got := a.trusted(ip); got != want {
			t.Errorf("trusted(%q) = %v, want %v", ip, got, want)
		}
	}
}

// TestDomainsAligned covers relaxed DMARC-style alignment.
func TestDomainsAligned(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"example.com", "example.com", true},
		{"Example.COM", "example.com", true},
		{"example.com.", "example.com", true}, // trailing root dot
		{"mail.example.com", "example.com", true},
		{"example.com", "mail.example.com", true},
		{"example.com", "evil.com", false},
		{"example.com", "notexample.com", false}, // suffix but not subdomain boundary
		{"", "example.com", false},
		{"example.com", "", false},
	}
	for _, tc := range cases {
		if got := domainsAligned(tc.a, tc.b); got != tc.want {
			t.Errorf("domainsAligned(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
