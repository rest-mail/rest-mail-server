package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Names with an empty label cannot be resolved, so they are never a domain
// (#1762). Each was accepted before, some normalized to another bad name.
var emptyLabelNames = []string{
	".a", "a..b", "..", "a..", "a。", "。", ".a.b", "a.b..", "a..b.", "a．．b", "a.。b", ". .a",
}

func TestValidateDomainName_RefusesEmptyLabels(t *testing.T) {
	for _, name := range emptyLabelNames {
		if got, err := validateDomainName(name); err == nil {
			t.Errorf("%q: accepted as %q, want refused for an empty label", name, got)
		}
	}
}

// The one trailing dot, the root, is still removed (#1762).
func TestValidateDomainName_StripsOneTrailingDot(t *testing.T) {
	for name, want := range map[string]string{
		"restmail.test.":   "restmail.test",
		"RESTMAIL.Test.":   "restmail.test",
		" restmail.test. ": "restmail.test",
		"münchen.test.":    "xn--mnchen-3ya.test",
		"restmail。test":    "restmail.test",
		"a.b":              "a.b",
	} {
		got, err := validateDomainName(name)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", name, got, err, want)
		}
	}
	// A label longer than DNS allows is no hostname either.
	if _, err := validateDomainName(strings.Repeat("a", 64) + ".test"); err == nil {
		t.Error("a 64-octet label was accepted")
	}
	if _, err := validateDomainName(strings.Repeat("a", 63) + ".test"); err != nil {
		t.Errorf("a 63-octet label was refused: %v", err)
	}
}

// Discovery refuses them as it refuses any non-domain: 422 validation_failed,
// before any lookup.
func TestRESTMAILDiscoveryRefusesEmptyLabels(t *testing.T) {
	h := discoverRoute(NewRestmailDiscoveryHandler(nil, "mx.example.test"))
	for _, name := range emptyLabelNames {
		t.Run(name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h(rr, httptest.NewRequest(http.MethodGet, "/.well-known/restmail?domain="+url.QueryEscape(name), nil))
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"validation_failed"`) {
				t.Fatalf("status = %d, want 422 validation_failed: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// A configured gateway hostname with an empty label is no hostname: discovery
// and autoconfig have nothing to advertise.
func TestGatewayHostnameRefusesEmptyLabels(t *testing.T) {
	for _, host := range []string{"mx..example.test", ".mx.example.test", "mx.example.test.."} {
		if h := NewRestmailDiscoveryHandler(nil, host); h.endpoint != "" {
			t.Errorf("discovery advertises %q for hostname %q", h.endpoint, host)
		}
		if h := NewAutoconfigHandler(nil, host); h.mailHost != "" {
			t.Errorf("autoconfig advertises %q for hostname %q", h.mailHost, host)
		}
	}
	if h := NewRestmailDiscoveryHandler(nil, "mx.example.test."); h.endpoint != "https://mx.example.test/restmail" {
		t.Errorf("discovery advertises %q for mx.example.test., want https://mx.example.test/restmail", h.endpoint)
	}
}

// Autoconfig never looks up a name with an empty label, from the address or
// from the host.
func TestAutoconfig_RequestDomainRefusesEmptyLabels(t *testing.T) {
	for _, name := range emptyLabelNames {
		r := httptest.NewRequest(http.MethodGet, "/.well-known/autoconfig/mail/config-v1.1.xml", nil)
		if got, ok := requestDomain(r, "bob@"+name); ok {
			t.Errorf("address bob@%s: looks up %q", name, got)
		}
		r.Host = name
		if got, ok := requestDomain(r, ""); ok {
			t.Errorf("host %q: looks up %q", name, got)
		}
	}
}
