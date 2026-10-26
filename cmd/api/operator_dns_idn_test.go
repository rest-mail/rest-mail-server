package main

import (
	"bytes"
	"strings"
	"testing"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dns"
	"github.com/restmail/restmail/internal/outbound"
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

// A domain with an active MTA-STS policy gets its _mta-sts record, the id derived from the
// policy, so dns:check reports a published id the policy has moved on from (#727). One whose
// policy cannot be served gets none, and the problem says why.
func TestAddMTASTSRecord(t *testing.T) {
	policy := models.MTASTSPolicy{Mode: "enforce", MXHosts: "mail.xn--bcher-kva.example", MaxAge: 86400}
	id, err := policy.RecordID()
	if err != nil {
		t.Fatal(err)
	}
	entry := domainRecords{Domain: "bücher.example"}
	addMTASTSRecord(&entry, models.Domain{Name: "bücher.example"}, policy)
	want := dns.DNSRecord{Type: "TXT", Name: "_mta-sts.xn--bcher-kva.example", Value: "v=STSv1; id=" + id, TTL: 3600}
	if len(entry.Records) != 1 || entry.Records[0] != want || entry.Problem != "" {
		t.Fatalf("records %+v, problem %q; want %+v", entry.Records, entry.Problem, want)
	}

	broken := domainRecords{Domain: "example.org", Problem: "no DKIM key: mail from this domain goes out unsigned"}
	addMTASTSRecord(&broken, models.Domain{Name: "example.org"}, models.MTASTSPolicy{Mode: "enforce", MXHosts: "bad_.example", MaxAge: 1})
	if len(broken.Records) != 0 || !strings.Contains(broken.Problem, "no DKIM key") || !strings.Contains(broken.Problem, "MTA-STS policy cannot be served") {
		t.Fatalf("records %+v, problem %q", broken.Records, broken.Problem)
	}
}

// A rotation's keys join the records to publish: the staged key before signing moves to it,
// and the previous key until it retires, under the A-label name and the DKIM TTL. A key this
// server cannot read is left out (#728).
func TestRotationRecords_StagedAndPreviousKeys(t *testing.T) {
	private, public, err := dkim.GenerateKey(outbound.MinDKIMRSABits)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := models.EncryptDKIMPrivateKey(private, "rotation-records-master-key-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	want, err := dkim.RecordValue(public)
	if err != nil {
		t.Fatal(err)
	}
	records := rotationRecords(models.Domain{Name: "bücher.example"}, []models.DKIMRotationKey{
		{Slot: models.DKIMRotationPrevious, Selector: "default", PrivateKey: stored},
		{Slot: models.DKIMRotationStaged, Selector: "restmail-1", PrivateKey: stored},
		{Slot: models.DKIMRotationStaged, Selector: "restmail-9", PrivateKey: "dkim:v1:unreadable"},
	}, "rotation-records-master-key-0123456789")
	if len(records) != 2 {
		t.Fatalf("records = %+v, want the two readable keys", records)
	}
	for i, selector := range []string{"default", "restmail-1"} {
		r := records[i]
		if r.Type != "TXT" || r.Name != selector+"._domainkey.xn--bcher-kva.example" || r.Value != want || r.TTL != 3600 {
			t.Errorf("record %d = %+v", i, r)
		}
	}
	if got := rotationRecords(models.Domain{Name: "bad_name.example"}, []models.DKIMRotationKey{{Selector: "x", PrivateKey: stored}}, ""); got != nil {
		t.Errorf("a name DNS cannot hold published %+v", got)
	}
}
