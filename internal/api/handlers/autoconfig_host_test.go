package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dns"
)

// Without emailaddress, the domain is the host the request was made to
// (draft-ietf-mailmaint-autoconfig §4.1, #721).
func TestAutoconfig_RequestDomain(t *testing.T) {
	for _, tc := range []struct {
		path, host, email string
		want              string
		ok                bool
	}{
		{"/mail/config-v1.1.xml", "autoconfig.example.org", "bob@Example.ORG", "example.org", true},
		{"/mail/config-v1.1.xml", "autoconfig.Example.org", "", "example.org", true},
		{"/mail/config-v1.1.xml", "autoconfig.example.org:443", "", "example.org", true},
		{"/mail/config-v1.1.xml", "example.org", "", "", false},
		{"/.well-known/autoconfig/mail/config-v1.1.xml", "example.org", "", "example.org", true},
		{"/.well-known/autoconfig/mail/config-v1.1.xml", "bücher.example", "", "xn--bcher-kva.example", true},
		{"/mail/config-v1.1.xml", "autoconfig.example.org", "nobody", "", false},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Host = tc.host
		got, ok := requestDomain(r, tc.email)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s on %s with %q: %q %v, want %q %v", tc.path, tc.host, tc.email, got, ok, tc.want, tc.ok)
		}
	}
}

// Autoconfig, Autodiscover and the MX record all name the mail host, never the
// email domain, so the four can never disagree (#722).
func TestAutoconfig_NamesTheMailHost(t *testing.T) {
	const domain, host = "example.org", "mail.example.net"
	moz := mozillaClientConfig(domain, host)
	ad := autodiscoverResponse(host, "bob@"+domain)
	if moz.Provider.Domain != domain {
		t.Errorf("the document is for %q, want %s", moz.Provider.Domain, domain)
	}
	names := []string{moz.Provider.Incoming.Hostname, moz.Provider.Outgoing.Hostname}
	for _, p := range ad.Response.Account.Protocol {
		names = append(names, p.Server)
	}
	for _, r := range dns.MailRecords(domain, host, "", "", "", "", dns.ClientPorts{Submission: 465, IMAP: 993}) {
		if r.Type == "MX" {
			names = append(names, r.Value)
		}
	}
	if len(names) != 5 {
		t.Fatalf("%d server names, want 5: %v", len(names), names)
	}
	for _, name := range names {
		if name != host {
			t.Errorf("a server is named %q, want the mail host %s: %v", name, host, names)
		}
	}
}

// Without a mail host there is nothing to advertise: 503, before any lookup.
func TestAutoconfig_WithoutAMailHost(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/.well-known/autoconfig/mail/config-v1.1.xml", nil)
	r.Host = "example.org"
	rr := httptest.NewRecorder()
	autoconfigRoutes(NewAutoconfigHandler(nil, "localhost")).ServeHTTP(rr, r)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", rr.Code, rr.Body)
	}
}

// Thunderbird's lookups without emailaddress get the configuration (#721), and
// it names the mail host (#722).
func TestMozillaAutoconfig_WithoutEmailAddress(t *testing.T) {
	gdb := openAuthzTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	domainName := fmt.Sprintf("auto-%d.test", time.Now().UnixNano())
	if err := tx.Create(&models.Domain{Name: domainName, Active: true}).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	h := NewAutoconfigHandler(tx, "mail.restmail.test")
	for _, tc := range []struct{ path, host string }{
		{"/.well-known/autoconfig/mail/config-v1.1.xml", domainName},
		{"/mail/config-v1.1.xml", "autoconfig." + domainName},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Host = tc.host
		rr := httptest.NewRecorder()
		autoconfigRoutes(h).ServeHTTP(rr, r)
		body := rr.Body.String()
		if rr.Code != http.StatusOK {
			t.Fatalf("%s on %s: %d %s", tc.path, tc.host, rr.Code, body)
		}
		for _, want := range []string{"<domain>" + domainName + "</domain>", "<hostname>mail.restmail.test</hostname>", "<port>993</port>", "<port>465</port>", "<socketType>SSL</socketType>"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s on %s: no %s in\n%s", tc.path, tc.host, want, body)
			}
		}
	}
}
