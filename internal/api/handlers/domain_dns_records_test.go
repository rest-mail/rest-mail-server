package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dns"
	"gorm.io/gorm"
)

const (
	checkMailHost = "mail.example.net"
	checkMailIP   = "203.0.113.25"
	// checkDKIMRecord is the record of the key dnsCheckKeyPEM builds.
	checkDKIMRecord = "v=DKIM1; k=ed25519; p=11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo="
)

// dnsCheckKeyPEM is the RFC 8032 §7.1 test key, whose public half checkDKIMRecord publishes.
func dnsCheckKeyPEM(t *testing.T) string {
	t.Helper()
	seed, err := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// publishedZone answers the DNS check's lookups from what a test says is published.
type publishedZone struct {
	mx    map[string][]*net.MX
	txt   map[string][]string
	srv   map[string][]*net.SRV
	ipv4  map[string][]net.IP
	ptr   map[string][]string
	cname map[string]string
	// failing names answer with a temporary failure rather than "no such host".
	failing map[string]bool
}

func noSuchHost(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (z publishedZone) answer(name string) error {
	if z.failing[name] {
		return &net.DNSError{Err: "server misbehaving 10.0.0.53:53", Name: name, IsTemporary: true}
	}
	return noSuchHost(name)
}

func (z publishedZone) install(h *DomainHandler) {
	h.lookupMX = func(_ context.Context, name string) ([]*net.MX, error) {
		if r, ok := z.mx[name]; ok {
			return r, nil
		}
		return nil, z.answer(name)
	}
	h.lookupTXT = func(_ context.Context, name string) ([]string, error) {
		if r, ok := z.txt[name]; ok {
			return r, nil
		}
		return nil, z.answer(name)
	}
	h.lookupSRV = func(_ context.Context, name string) ([]*net.SRV, error) {
		if r, ok := z.srv[name]; ok {
			return r, nil
		}
		return nil, z.answer(name)
	}
	h.lookupIPv4 = func(_ context.Context, name string) ([]net.IP, error) {
		if r, ok := z.ipv4[name]; ok {
			return r, nil
		}
		return nil, z.answer(name)
	}
	h.lookupAddr = func(_ context.Context, addr string) ([]string, error) {
		if r, ok := z.ptr[addr]; ok {
			return r, nil
		}
		return nil, z.answer(addr)
	}
	h.lookupCNAME = func(_ context.Context, name string) (string, error) {
		if r, ok := z.cname[name]; ok {
			return r, nil
		}
		return "", z.answer(name)
	}
}

// aCorrectZone is everything MailRecords lists for domain on checkMailHost, published as listed.
func aCorrectZone(domain string) publishedZone {
	srv := func(priority, weight, port uint16, target string) []*net.SRV {
		return []*net.SRV{{Target: target, Port: port, Priority: priority, Weight: weight}}
	}
	return publishedZone{
		mx: map[string][]*net.MX{domain: {{Host: checkMailHost + ".", Pref: 10}}},
		txt: map[string][]string{
			domain:                        {"google-site-verification=abc", "v=spf1 mx -all"},
			"_dmarc." + domain:            {"v=DMARC1; p=none; rua=mailto:postmaster@" + domain},
			"rm2026._domainkey." + domain: {checkDKIMRecord},
			"_smtp._tls." + domain:        {"v=TLSRPTv1; rua=https://" + checkMailHost + "/.well-known/smtp-tlsrpt"},
		},
		srv: map[string][]*net.SRV{
			"_submissions._tcp." + domain: srv(0, 1, 465, checkMailHost+"."),
			"_imaps._tcp." + domain:       srv(0, 1, 993, checkMailHost+"."),
			"_pop3s._tcp." + domain:       srv(0, 1, 995, checkMailHost+"."),
			"_submission._tcp." + domain:  srv(0, 0, 0, "."),
			"_imap._tcp." + domain:        srv(0, 0, 0, "."),
			"_pop3._tcp." + domain:        srv(0, 0, 0, "."),
		},
		ipv4: map[string][]net.IP{checkMailHost: {net.ParseIP(checkMailIP)}},
		ptr:  map[string][]string{checkMailIP: {checkMailHost + "."}},
		cname: map[string]string{
			"autoconfig." + domain:   checkMailHost + ".",
			"autodiscover." + domain: checkMailHost + ".",
		},
	}
}

type checkedRecord struct {
	Type, Name, Status, Value, Expected, Message string
}

type checkedDomain struct {
	Domain  string          `json:"domain"`
	Summary string          `json:"summary"`
	Records []checkedRecord `json:"records"`
}

func (c checkedDomain) record(t *testing.T, typ, name string) checkedRecord {
	t.Helper()
	for _, r := range c.Records {
		if r.Type == typ && r.Name == name {
			return r
		}
	}
	t.Fatalf("no %s record named %s in %+v", typ, name, c.Records)
	return checkedRecord{}
}

func checkDomainDNS(t *testing.T, h *DomainHandler, id uint) checkedDomain {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/domains/%d/dns", id), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", fmt.Sprint(id))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	domainRoutes(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data checkedDomain `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Data
}

// dnsCheckFixture makes a domain with a DKIM key, and a handler on checkMailHost.
func dnsCheckFixture(t *testing.T, change ...func(*models.Domain)) (*gorm.DB, models.Domain, *DomainHandler) {
	t.Helper()
	tx := openDNSCheckDB(t).Begin()
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{
		Name:           fmt.Sprintf("check-%d.test", time.Now().UnixNano()),
		DKIMSelector:   "rm2026",
		DKIMPrivateKey: dnsCheckKeyPEM(t),
		DMARCPolicy:    models.DMARCPolicyNone,
	}
	for _, c := range change {
		c(&domain)
	}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	h := NewDomainHandler(tx, nil)
	h.SetMailServer(checkMailHost, "", dns.ClientPorts{Submission: 465, IMAP: 993, POP3: 995})
	return tx, domain, h
}

// Every record is listed with the value to publish, the same list as `restmail-api dns`.
func TestDNSCheck_ListsEveryRecordToPublish(t *testing.T) {
	_, domain, h := dnsCheckFixture(t)
	aCorrectZone(domain.Name).install(h)

	got := checkDomainDNS(t, h, domain.ID)

	want := dns.MailRecords(domain.Name, checkMailHost, "", "rm2026", checkDKIMRecord, "none", dns.ClientPorts{Submission: 465, IMAP: 993, POP3: 995})
	if len(want) != 13 {
		t.Fatalf("MailRecords lists %d, want 13", len(want))
	}
	for _, r := range want {
		found := false
		for _, c := range got.Records {
			if c.Name == r.Name && c.Expected != "" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s %s is not listed with a value to publish in %+v", r.Type, r.Name, got.Records)
		}
	}
	if r := got.record(t, "MX", domain.Name); r.Expected != "10 "+checkMailHost || r.Value != "10 "+checkMailHost || r.Status != "ok" {
		t.Errorf("MX = %+v, want it to expect and find the mail host at priority 10", r)
	}
	for _, c := range []struct{ typ, name, expected string }{
		{"SPF", domain.Name, "v=spf1 mx -all"},
		{"DMARC", "_dmarc." + domain.Name, "v=DMARC1; p=none; rua=mailto:postmaster@" + domain.Name},
		{"DKIM", "rm2026._domainkey." + domain.Name, checkDKIMRecord},
		{"TLS-RPT", "_smtp._tls." + domain.Name, "v=TLSRPTv1; rua=https://" + checkMailHost + "/.well-known/smtp-tlsrpt"},
		{"SRV", "_submissions._tcp." + domain.Name, "0 1 465 " + checkMailHost},
		{"SRV", "_imaps._tcp." + domain.Name, "0 1 993 " + checkMailHost},
		{"SRV", "_pop3s._tcp." + domain.Name, "0 1 995 " + checkMailHost},
		{"SRV", "_submission._tcp." + domain.Name, "0 0 0 ."},
		{"SRV", "_imap._tcp." + domain.Name, "0 0 0 ."},
		{"SRV", "_pop3._tcp." + domain.Name, "0 0 0 ."},
	} {
		if r := got.record(t, c.typ, c.name); r.Expected != c.expected || r.Status != "ok" {
			t.Errorf("%s %s = %+v, want it verified against %q", c.typ, c.name, r, c.expected)
		}
	}
	if r := got.record(t, "PTR", checkMailIP); r.Expected != checkMailHost || r.Status != "ok" {
		t.Errorf("PTR = %+v, want it verified against the mail host", r)
	}
	// The server does not know its own address, so there is no value to compare, and the
	// check says that rather than letting Verified suggest otherwise.
	a := got.record(t, "A", checkMailHost)
	if a.Expected != "" || a.Value != checkMailIP || a.Status != "ok" || !strings.Contains(a.Message, "does not know") {
		t.Errorf("A = %+v, want the found address and a message that it could not be compared", a)
	}
	// MTA-STS is optional until the domain has an active policy, as the guide says.
	sts := got.record(t, "MTA-STS", "_mta-sts."+domain.Name)
	if sts.Status != "missing" || sts.Expected != "" {
		t.Errorf("MTA-STS with no policy = %+v, want missing with nothing to publish", sts)
	}
	wantSummary := fmt.Sprintf("%d/%d records verified", len(got.Records)-1, len(got.Records))
	if got.Summary != wantSummary {
		t.Errorf("summary = %q, want %q", got.Summary, wantSummary)
	}
}

// The check compares what it finds with the list, and says when they differ (#1703).
func TestDNSCheck_SaysWhenAPublishedValueDiffers(t *testing.T) {
	cases := []struct {
		name          string
		change        func(*models.Domain)
		publish       func(domain string, z *publishedZone)
		typ, recName  string // "%s" is the domain
		wantStatus    string
		wantValue     string
		wantExpected  string
		wantInMessage string
	}{
		{
			name: "MX at another server",
			publish: func(d string, z *publishedZone) {
				z.mx[d] = []*net.MX{{Host: "sink.remote.test.", Pref: 10}}
			},
			typ: "MX", recName: "%s", wantStatus: "different",
			wantValue: "10 sink.remote.test", wantExpected: "10 " + checkMailHost, wantInMessage: "sink.remote.test",
		},
		{
			name: "MX here and at the old provider",
			publish: func(d string, z *publishedZone) {
				z.mx[d] = append(z.mx[d], &net.MX{Host: "old.provider.test.", Pref: 20})
			},
			typ: "MX", recName: "%s", wantStatus: "different",
			wantValue: "10 " + checkMailHost + ", 20 old.provider.test", wantExpected: "10 " + checkMailHost, wantInMessage: "More than one",
		},
		{
			name: "SPF that does not name the MX",
			publish: func(d string, z *publishedZone) {
				z.txt[d] = []string{"v=spf1 include:old.provider.test -all"}
			},
			typ: "SPF", recName: "%s", wantStatus: "different",
			wantValue: "v=spf1 include:old.provider.test -all", wantExpected: "v=spf1 mx -all", wantInMessage: "differs",
		},
		{
			name: "two SPF records",
			publish: func(d string, z *publishedZone) {
				z.txt[d] = []string{"v=spf1 mx -all", "v=spf1 a -all"}
			},
			typ: "SPF", recName: "%s", wantStatus: "different",
			wantValue: "v=spf1 mx -all, v=spf1 a -all", wantExpected: "v=spf1 mx -all", wantInMessage: "RFC 7208",
		},
		{
			name:   "DMARC at an older policy than the domain's",
			change: func(d *models.Domain) { d.DMARCPolicy = models.DMARCPolicyQuarantine },
			publish: func(d string, z *publishedZone) {
				z.txt["_dmarc."+d] = []string{"v=DMARC1; p=none; rua=mailto:postmaster@" + d}
			},
			typ: "DMARC", recName: "_dmarc.%s", wantStatus: "different",
			wantValue:     "v=DMARC1; p=none; rua=mailto:postmaster@%s",
			wantExpected:  "v=DMARC1; p=quarantine; rua=mailto:postmaster@%s",
			wantInMessage: "differs",
		},
		{
			name: "DKIM with another key",
			publish: func(d string, z *publishedZone) {
				z.txt["rm2026._domainkey."+d] = []string{"v=DKIM1; k=rsa; p=MIIBold"}
			},
			typ: "DKIM", recName: "rm2026._domainkey.%s", wantStatus: "different",
			wantValue: "v=DKIM1; k=rsa; p=MIIBold", wantExpected: checkDKIMRecord, wantInMessage: "differs",
		},
		{
			name: "TLS-RPT sending reports elsewhere",
			publish: func(d string, z *publishedZone) {
				z.txt["_smtp._tls."+d] = []string{"v=TLSRPTv1; rua=mailto:tls@" + d}
			},
			typ: "TLS-RPT", recName: "_smtp._tls.%s", wantStatus: "different",
			wantValue:     "v=TLSRPTv1; rua=mailto:tls@%s",
			wantExpected:  "v=TLSRPTv1; rua=https://" + checkMailHost + "/.well-known/smtp-tlsrpt",
			wantInMessage: "differs",
		},
		{
			name: "SRV on another port",
			publish: func(d string, z *publishedZone) {
				z.srv["_imaps._tcp."+d] = []*net.SRV{{Target: checkMailHost + ".", Port: 143, Weight: 1}}
			},
			typ: "SRV", recName: "_imaps._tcp.%s", wantStatus: "different",
			wantValue: "0 1 143 " + checkMailHost, wantExpected: "0 1 993 " + checkMailHost, wantInMessage: "differs",
		},
		{
			name: "SRV missing",
			publish: func(d string, z *publishedZone) {
				delete(z.srv, "_submissions._tcp."+d)
			},
			typ: "SRV", recName: "_submissions._tcp.%s", wantStatus: "missing",
			wantExpected: "0 1 465 " + checkMailHost, wantInMessage: "No SRV record",
		},
		{
			name: "reverse record naming another host",
			publish: func(_ string, z *publishedZone) {
				z.ptr[checkMailIP] = []string{"host-25.provider.test."}
			},
			typ: "PTR", recName: checkMailIP, wantStatus: "different",
			wantValue: "host-25.provider.test", wantExpected: checkMailHost, wantInMessage: "differs",
		},
		{
			name: "MX lookup fails for now",
			publish: func(d string, z *publishedZone) {
				delete(z.mx, d)
				z.failing = map[string]bool{d: true}
			},
			typ: "MX", recName: "%s", wantStatus: "error",
			wantExpected: "10 " + checkMailHost, wantInMessage: "could not be checked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var change []func(*models.Domain)
			if tc.change != nil {
				change = append(change, tc.change)
			}
			_, domain, h := dnsCheckFixture(t, change...)
			zone := aCorrectZone(domain.Name)
			tc.publish(domain.Name, &zone)
			zone.install(h)

			name := tc.recName
			if strings.Contains(name, "%s") {
				name = fmt.Sprintf(name, domain.Name)
			}
			fill := func(s string) string {
				if strings.Contains(s, "%s") {
					return fmt.Sprintf(s, domain.Name)
				}
				return s
			}
			r := checkDomainDNS(t, h, domain.ID).record(t, tc.typ, name)
			if r.Status != tc.wantStatus || r.Value != fill(tc.wantValue) || r.Expected != fill(tc.wantExpected) {
				t.Errorf("got %+v, want status %s, value %q, expected %q", r, tc.wantStatus, fill(tc.wantValue), fill(tc.wantExpected))
			}
			if !strings.Contains(r.Message, tc.wantInMessage) {
				t.Errorf("message %q does not say %q", r.Message, tc.wantInMessage)
			}
			if strings.Contains(r.Message, "10.0.0.53") {
				t.Errorf("message %q shows the resolver's address", r.Message)
			}
		})
	}
}

// Formatting that does not change a record's meaning is not a difference.
func TestDNSCheck_IgnoresFormattingThatChangesNothing(t *testing.T) {
	_, domain, h := dnsCheckFixture(t)
	zone := aCorrectZone(domain.Name)
	zone.mx[domain.Name] = []*net.MX{{Host: strings.ToUpper(checkMailHost) + ".", Pref: 5}}
	zone.txt["_dmarc."+domain.Name] = []string{"v=DMARC1;p=none;  rua=mailto:postmaster@" + domain.Name + ";"}
	zone.txt["rm2026._domainkey."+domain.Name] = []string{"v=DKIM1; k=ed25519; p=11qYAYKxCrfVS/7Ty WQHOg7hcvPapiMlrwIaaPcHURo="}
	zone.install(h)

	got := checkDomainDNS(t, h, domain.ID)
	for _, c := range []struct{ typ, name string }{
		{"MX", domain.Name},
		{"DMARC", "_dmarc." + domain.Name},
		{"DKIM", "rm2026._domainkey." + domain.Name},
	} {
		if r := got.record(t, c.typ, c.name); r.Status != "ok" {
			t.Errorf("%s = %+v, want ok", c.typ, r)
		}
	}
}

// An active policy that cannot be served has no record to publish, and the check says so, even
// though the mail app discovery names mean the list always holds a CNAME.
func TestDNSCheck_UnservablePolicyIsReported(t *testing.T) {
	tx, domain, h := dnsCheckFixture(t)
	policy := models.MTASTSPolicy{DomainID: domain.ID, Mode: "testing", MXHosts: "not a host!", MaxAge: 604800, Active: true}
	if err := tx.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := policy.RecordID(); err == nil {
		t.Fatal("the fixture's policy can be served; this test needs one that cannot")
	}
	aCorrectZone(domain.Name).install(h)
	r := checkDomainDNS(t, h, domain.ID).record(t, "MTA-STS", "_mta-sts."+domain.Name)
	if r.Status != statusError || !strings.Contains(r.Message, "cannot be served") {
		t.Errorf("unservable policy = %+v", r)
	}
}

// A missing discovery name is reported under its own name, not as the MTA-STS policy host.
func TestDNSCheck_MissingCNAMENamesItsHost(t *testing.T) {
	_, domain, h := dnsCheckFixture(t)
	zone := aCorrectZone(domain.Name)
	delete(zone.cname, "autoconfig."+domain.Name)
	zone.install(h)
	name := "autoconfig." + domain.Name
	r := checkDomainDNS(t, h, domain.ID).record(t, "CNAME", name)
	if r.Status != statusMissing || !strings.Contains(r.Message, name) || strings.Contains(r.Message, "MTA-STS") {
		t.Errorf("missing discovery name = %+v", r)
	}
}

// A policy-less domain's MTA-STS is optional, but a record with no policy behind it is not
// right, and an active policy has a record to publish, and a host for its policy file.
func TestDNSCheck_MTASTSFollowsTheDomainsPolicy(t *testing.T) {
	t.Run("record with no active policy", func(t *testing.T) {
		_, domain, h := dnsCheckFixture(t)
		zone := aCorrectZone(domain.Name)
		zone.txt["_mta-sts."+domain.Name] = []string{"v=STSv1; id=old"}
		zone.install(h)
		r := checkDomainDNS(t, h, domain.ID).record(t, "MTA-STS", "_mta-sts."+domain.Name)
		if r.Status != "different" || r.Value != "v=STSv1; id=old" || !strings.Contains(r.Message, "no active MTA-STS policy") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("active policy", func(t *testing.T) {
		tx, domain, h := dnsCheckFixture(t)
		policy := models.MTASTSPolicy{DomainID: domain.ID, Mode: "testing", MXHosts: checkMailHost, MaxAge: 604800, Active: true}
		if err := tx.Create(&policy).Error; err != nil {
			t.Fatal(err)
		}
		id, err := policy.RecordID()
		if err != nil {
			t.Fatal(err)
		}
		zone := aCorrectZone(domain.Name)
		zone.install(h)
		got := checkDomainDNS(t, h, domain.ID)
		sts := got.record(t, "MTA-STS", "_mta-sts."+domain.Name)
		if sts.Status != "missing" || sts.Expected != "v=STSv1; id="+id || !strings.Contains(sts.Message, "HTTPS") {
			t.Errorf("unpublished = %+v", sts)
		}
		host := got.record(t, "CNAME", "mta-sts."+domain.Name)
		if host.Status != "missing" || host.Expected != checkMailHost {
			t.Errorf("host = %+v", host)
		}

		zone.txt["_mta-sts."+domain.Name] = []string{"v=STSv1; id=" + id}
		zone.cname = map[string]string{"mta-sts." + domain.Name: checkMailHost + "."}
		zone.install(h)
		got = checkDomainDNS(t, h, domain.ID)
		if r := got.record(t, "MTA-STS", "_mta-sts."+domain.Name); r.Status != "ok" {
			t.Errorf("published = %+v", r)
		}
		if r := got.record(t, "CNAME", "mta-sts."+domain.Name); r.Status != "ok" {
			t.Errorf("published host = %+v", r)
		}
	})
}

// A rotation's staged key has a record to publish, under its own selector.
func TestDNSCheck_ListsARotationsKeys(t *testing.T) {
	tx, domain, h := dnsCheckFixture(t)
	key := models.DKIMRotationKey{DomainID: domain.ID, Slot: models.DKIMRotationStaged, Selector: "rm2027", PrivateKey: dnsCheckKeyPEM(t), TTLSeconds: 3600}
	if err := tx.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	aCorrectZone(domain.Name).install(h)
	r := checkDomainDNS(t, h, domain.ID).record(t, "DKIM", "rm2027._domainkey."+domain.Name)
	if r.Status != "missing" || r.Expected != checkDKIMRecord {
		t.Errorf("staged key = %+v", r)
	}
}

// With no key there is nothing to publish, and an unreadable one is not called missing.
func TestDNSCheck_DKIMWithoutAUsableKey(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		_, domain, h := dnsCheckFixture(t, func(d *models.Domain) { d.DKIMPrivateKey = "-- key --" })
		aCorrectZone(domain.Name).install(h)
		r := checkDomainDNS(t, h, domain.ID).record(t, "DKIM", "rm2026._domainkey."+domain.Name)
		if r.Status != "error" || r.Expected != "" || !strings.Contains(r.Message, "could not read") {
			t.Errorf("got %+v", r)
		}
	})
}

// A server with no fully-qualified GATEWAY_HOSTNAME has no value for the records that name it.
// It must not call what it finds verified. The records that do not name it still compare.
func TestDNSCheck_NoMailHostConfigured(t *testing.T) {
	_, domain, h := dnsCheckFixture(t)
	h.SetMailServer("localhost", "", dns.ClientPorts{Submission: 465, IMAP: 993, POP3: 995})
	aCorrectZone(domain.Name).install(h)
	got := checkDomainDNS(t, h, domain.ID)

	mx := got.record(t, "MX", domain.Name)
	if mx.Status != "error" || mx.Expected != "" || mx.Value != "10 "+checkMailHost || !strings.Contains(mx.Message, "GATEWAY_HOSTNAME") {
		t.Errorf("MX = %+v", mx)
	}
	for _, n := range []string{"_smtp._tls.", "_imaps._tcp."} {
		for _, r := range got.Records {
			if r.Name == n+domain.Name && (r.Status != "error" || r.Expected != "") {
				t.Errorf("%s = %+v", n, r)
			}
		}
	}
	if r := got.record(t, "SPF", domain.Name); r.Status != "ok" {
		t.Errorf("SPF = %+v, want it still compared", r)
	}
	if r := got.record(t, "SRV", "_imap._tcp."+domain.Name); r.Status != "ok" {
		t.Errorf("a service not offered names no host: %+v", r)
	}
	for _, r := range got.Records {
		if r.Type == "A" || r.Type == "PTR" {
			t.Errorf("no mail host to look up, but got %+v", r)
		}
	}
}

func TestAbsoluteLookupAsksForTheExactName(t *testing.T) {
	for given, want := range map[string]string{
		"_mta-sts.example.com":  "_mta-sts.example.com.",
		"_mta-sts.example.com.": "_mta-sts.example.com.",
		"mail.example.net":      "mail.example.net.",
		"203.0.113.25":          "203.0.113.25",
		"2001:db8::25":          "2001:db8::25",
	} {
		var asked string
		lookup := absoluteLookup(func(_ context.Context, name string) (int, error) {
			asked = name
			return 7, nil
		})
		if got, err := lookup(context.Background(), given); got != 7 || err != nil {
			t.Fatalf("lookup(%q) = %d, %v", given, got, err)
		}
		if asked != want {
			t.Errorf("lookup(%q) asked for %q, want %q", given, asked, want)
		}
	}
}
