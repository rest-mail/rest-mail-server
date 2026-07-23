package queue

import (
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// TestCapabilityApplies_MXBinding verifies OSI-20: a cached RESTMAIL capability
// is bound to the exact (domain, mx_host) it was learned from and is only reused
// when that binding still holds and the entry is unexpired. A capability learned
// from one MX host must never be applied when the domain's primary MX has changed
// — that is the cross-host poisoning the binding prevents.
func TestCapabilityApplies_MXBinding(t *testing.T) {
	now := time.Now()
	future := now.Add(30 * time.Minute)
	past := now.Add(-1 * time.Minute)

	base := models.RESTMAILCapability{
		Domain:      "example.test",
		MXHost:      "mx1.example.test",
		Supported:   true,
		EndpointURL: "https://mx1.example.test/restmail",
		ExpiresAt:   future,
	}

	cases := []struct {
		name   string
		cap    models.RESTMAILCapability
		domain string
		mxHost string
		want   bool
	}{
		{
			name:   "same domain, same host, unexpired -> reuse",
			cap:    base,
			domain: "example.test",
			mxHost: "mx1.example.test",
			want:   true,
		},
		{
			name:   "same domain, DIFFERENT host -> no reuse (MX rotated / rogue host)",
			cap:    base,
			domain: "example.test",
			mxHost: "mx2.example.test",
			want:   false,
		},
		{
			name:   "DIFFERENT domain -> no reuse (multi-tenant isolation)",
			cap:    base,
			domain: "other.test",
			mxHost: "mx1.example.test",
			want:   false,
		},
		{
			name:   "expired entry -> no reuse even if binding matches",
			cap:    func() models.RESTMAILCapability { c := base; c.ExpiresAt = past; return c }(),
			domain: "example.test",
			mxHost: "mx1.example.test",
			want:   false,
		},
		{
			name:   "legacy row with empty MXHost -> no reuse, force reprobe",
			cap:    func() models.RESTMAILCapability { c := base; c.MXHost = ""; return c }(),
			domain: "example.test",
			mxHost: "mx1.example.test",
			want:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := capabilityApplies(c.cap, c.domain, c.mxHost, now)
			if got != c.want {
				t.Errorf("capabilityApplies(domain=%q, mxHost=%q) = %v, want %v",
					c.domain, c.mxHost, got, c.want)
			}
		})
	}
}
