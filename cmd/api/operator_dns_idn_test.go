package main

import (
	"bytes"
	"strings"
	"testing"

	dkim "github.com/rest-mail/go-dkim"
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
			if got := names["TXT _dmarc.xn--bcher-kva.example"]; got != "v=DMARC1; p=reject; rua=mailto:postmaster@xn--bcher-kva.example" {
				t.Errorf("DMARC = %q, records %v", got, names)
			}
		})
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
