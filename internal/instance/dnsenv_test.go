package instance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReversePTR(t *testing.T) {
	if got := reversePTR("10.99.0.13"); got != "13.0.99.10.in-addr.arpa" {
		t.Errorf("reversePTR = %q", got)
	}
	if got := reversePTR("bad"); got != "" {
		t.Errorf("reversePTR(bad) = %q, want empty", got)
	}
}

func TestMtaStsIDStable(t *testing.T) {
	a := mtaStsID("mail3.test")
	b := mtaStsID("mail3.test")
	c := mtaStsID("mail4.test")
	if a != b {
		t.Error("mtaStsID not deterministic")
	}
	if a == c {
		t.Error("mtaStsID collided across domains")
	}
	if len(a) != 12 {
		t.Errorf("mtaStsID len = %d, want 12", len(a))
	}
}

func TestDNSEnvFromMail3Manifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "manifest.yml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := DNSEnv(m)
	if err != nil {
		t.Fatalf("DNSEnv: %v", err)
	}
	for _, want := range []string{
		"MAIL_NAME=mail3\n",
		"MAIL_HOSTNAME=mail3.test\n",
		"SMTP_IP=10.99.0.13\n",
		"IMAP_IP=10.99.0.15\n",
		"POP3_IP=10.99.0.16\n",
		"API_IP=10.99.0.20\n",
		"MAIL_PTR=13.0.99.10.in-addr.arpa\n",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("dns.env missing %q\ngot:\n%s", want, out)
		}
	}
}

func TestDNSEnvRequiresSMTP(t *testing.T) {
	m := &Manifest{Domain: "x.test", Components: []Component{{Name: "api", IP: "10.99.0.1"}}}
	if _, err := DNSEnv(m); err == nil {
		t.Fatal("expected error when smtp-gateway component is missing")
	}
}

// TestDNSEnvPerServedDomain proves multi-domain DNS: each additional served
// domain renders its own MAIL_NAME/MAIL_HOSTNAME/MTA_STS_ID over the SAME
// shared instance gateway IPs.
func TestDNSEnvPerServedDomain(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "manifest_multidomain.yml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := DNSEnvForDomain(m, "shop.test")
	if err != nil {
		t.Fatalf("DNSEnvForDomain: %v", err)
	}
	for _, want := range []string{
		"MAIL_NAME=shop\n",
		"MAIL_HOSTNAME=shop.test\n",
		// shared instance gateways — identical to the primary's fragment.
		"SMTP_IP=10.99.0.13\n",
		"IMAP_IP=10.99.0.15\n",
		"POP3_IP=10.99.0.16\n",
		"API_IP=10.99.0.20\n",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("shop.test dns.env missing %q\ngot:\n%s", want, out)
		}
	}
	// The MTA-STS id is per-hostname, so it must differ from the primary's.
	primary, err := DNSEnv(m)
	if err != nil {
		t.Fatalf("DNSEnv primary: %v", err)
	}
	if bytes.Equal(out, primary) {
		t.Error("shop.test dns.env is identical to the primary — per-domain hostname not applied")
	}
}

func TestDNSEnvForUnknownDomainErrors(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "manifest_multidomain.yml"))
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := DNSEnvForDomain(m, "not-served.test"); err == nil {
		t.Fatal("expected error for a domain the instance does not serve")
	}
}
