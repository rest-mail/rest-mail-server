package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dns"
)

// An internationalized domain's records are published and checked under its
// A-labels: zones and dig only know that form, and an MX value in Unicode
// never matches what a resolver answers.
func TestRecordsForDomain_IDNUsesALabels(t *testing.T) {
	for _, stored := range []string{"bücher.example", "BÜCHER.example", "bücher.example", "xn--bcher-kva.example"} {
		t.Run(stored, func(t *testing.T) {
			entry := recordsForDomain(models.Domain{Name: stored}, "mail.xn--bcher-kva.example", "203.0.113.5", "", dns.ClientPorts{})
			if entry.Domain != stored {
				t.Errorf("domain = %q, want the stored name %q", entry.Domain, stored)
			}
			names := map[string]string{}
			for _, r := range entry.Records {
				for _, s := range []string{r.Name, r.Value} {
					if strings.ContainsFunc(s, func(c rune) bool { return c > 0x7f }) {
						t.Errorf("%s record %q = %q is not ASCII", r.Type, r.Name, r.Value)
					}
				}
				names[r.Type+" "+r.Name] = r.Value
			}
			if got := names["MX xn--bcher-kva.example"]; got != "mail.xn--bcher-kva.example" {
				t.Errorf("MX = %q, records %v", got, names)
			}
			if got := names["TXT _dmarc.xn--bcher-kva.example"]; got != "v=DMARC1; p=none; rua=mailto:postmaster@xn--bcher-kva.example" {
				t.Errorf("DMARC = %q, records %v", got, names)
			}
		})
	}
}

// The DMARC record asks for the domain's own policy, so dns:check reports a published record
// whose policy differs (#729).
func TestRecordsForDomain_DMARCPolicy(t *testing.T) {
	for policy, want := range map[string]string{
		models.DMARCPolicyNone:       "v=DMARC1; p=none; rua=mailto:postmaster@example.org",
		models.DMARCPolicyQuarantine: "v=DMARC1; p=quarantine; rua=mailto:postmaster@example.org",
		models.DMARCPolicyReject:     "v=DMARC1; p=reject; rua=mailto:postmaster@example.org",
	} {
		entry := recordsForDomain(models.Domain{Name: "example.org", DMARCPolicy: policy}, "mail.example.org", "", "", dns.ClientPorts{})
		var got string
		for _, r := range entry.Records {
			if r.Type == "TXT" && r.Name == "_dmarc.example.org" {
				got = r.Value
			}
		}
		if got != want {
			t.Errorf("policy %s: DMARC = %q, want %q", policy, got, want)
		}
	}
}

func TestRecordsForDomain_InvalidNamePublishesNothing(t *testing.T) {
	entry := recordsForDomain(models.Domain{Name: "bad_name.example", DKIMSelector: "mail", DKIMPrivateKey: "unused"}, "mail.example", "203.0.113.5", "", dns.ClientPorts{})
	if len(entry.Records) != 0 || entry.Records == nil {
		t.Errorf("records = %#v, want an empty list", entry.Records)
	}
	if !strings.Contains(entry.Problem, "not a valid domain name") {
		t.Errorf("problem = %q", entry.Problem)
	}
}

func TestDNSRecords_RejectsAnInvalidMailHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := dnsRecords([]string{"--mail-host", "mail_host.example"}, &stdout, &stderr); status != exitUsage {
		t.Fatalf("status %d, want %d", status, exitUsage)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), `"bad_flag"`) {
		t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}
