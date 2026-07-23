package config

import (
	"reflect"
	"testing"
)

// TestRestmailDeliverAuth_Defaults verifies the OSI-3 accessor's secure
// defaults and env overrides.
func TestRestmailDeliverAuth_Defaults(t *testing.T) {
	c := &Config{}

	got := c.RestmailDeliverAuth()
	if !got.Enabled {
		t.Errorf("default Enabled = false, want true (gate on by default)")
	}
	if got.Strict {
		t.Errorf("default Strict = true, want false")
	}
	if got.TrustedCIDRs != nil {
		t.Errorf("default TrustedCIDRs = %v, want nil", got.TrustedCIDRs)
	}

	t.Setenv("RESTMAIL_DELIVER_AUTH_ENABLED", "false")
	t.Setenv("RESTMAIL_DELIVER_STRICT", "true")
	t.Setenv("RESTMAIL_DELIVER_TRUSTED_CIDRS", "10.0.0.0/8, 192.168.1.1")
	got = c.RestmailDeliverAuth()
	if got.Enabled {
		t.Errorf("Enabled override not applied")
	}
	if !got.Strict {
		t.Errorf("Strict override not applied")
	}
	if !reflect.DeepEqual(got.TrustedCIDRs, []string{"10.0.0.0/8", "192.168.1.1"}) {
		t.Errorf("TrustedCIDRs = %v, want [10.0.0.0/8 192.168.1.1]", got.TrustedCIDRs)
	}
}

// TestSieveRedirect_Defaults verifies the OSI-13 accessor's deny-external
// default and overrides.
func TestSieveRedirect_Defaults(t *testing.T) {
	c := &Config{}

	got := c.SieveRedirect()
	if got.AllowExternal {
		t.Errorf("default AllowExternal = true, want false (deny external by default)")
	}
	if got.AllowedDomains != nil {
		t.Errorf("default AllowedDomains = %v, want nil", got.AllowedDomains)
	}

	t.Setenv("SIEVE_REDIRECT_ALLOW_EXTERNAL", "true")
	t.Setenv("SIEVE_REDIRECT_ALLOWED_DOMAINS", "partner.org, backup.example")
	got = c.SieveRedirect()
	if !got.AllowExternal {
		t.Errorf("AllowExternal override not applied")
	}
	if !reflect.DeepEqual(got.AllowedDomains, []string{"partner.org", "backup.example"}) {
		t.Errorf("AllowedDomains = %v, want [partner.org backup.example]", got.AllowedDomains)
	}
}

// TestPipelineFilterErrorAction verifies the OSI-18 knob: fail-closed default,
// valid overrides, and secure fallback on a malformed value.
func TestPipelineFilterErrorAction(t *testing.T) {
	c := &Config{}

	if got := c.PipelineFilterErrorAction(); got != "defer" {
		t.Errorf("default = %q, want defer (fail-closed)", got)
	}

	for _, v := range []string{"continue", "reject", "defer"} {
		t.Setenv("PIPELINE_FILTER_ERROR_ACTION", v)
		if got := c.PipelineFilterErrorAction(); got != v {
			t.Errorf("override %q = %q, want %q", v, got, v)
		}
	}

	// Case-insensitive.
	t.Setenv("PIPELINE_FILTER_ERROR_ACTION", "CONTINUE")
	if got := c.PipelineFilterErrorAction(); got != "continue" {
		t.Errorf("case-insensitive override = %q, want continue", got)
	}

	// Malformed -> secure default.
	t.Setenv("PIPELINE_FILTER_ERROR_ACTION", "bogus")
	if got := c.PipelineFilterErrorAction(); got != "defer" {
		t.Errorf("malformed value = %q, want defer (secure fallback)", got)
	}
}
