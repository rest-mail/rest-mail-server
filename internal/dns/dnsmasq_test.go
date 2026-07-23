package dns

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A record value containing a newline would inject a second dnsmasq directive
// (e.g. listen-address=0.0.0.0). It must be refused, and nothing written.
func TestDnsmasqRejectsInjection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domains.conf")
	p := NewDnsmasqProvider(path)

	err := p.EnsureRecords(context.Background(), "evil.test", []DNSRecord{
		{Type: "A", Name: "evil.test", Value: "1.2.3.4\nlisten-address=0.0.0.0"},
	})
	if err == nil {
		t.Fatal("expected injection via record value to be rejected")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("config file must not be written when a record is rejected")
	}

	// Injection via the record name must also be refused.
	if err := p.EnsureRecords(context.Background(), "ok.test", []DNSRecord{
		{Type: "A", Name: "ok.test\nlisten-address=0.0.0.0", Value: "1.2.3.4"},
	}); err == nil {
		t.Fatal("expected injection via record name to be rejected")
	}

	// Injection via the domain argument must also be refused.
	if err := p.EnsureRecords(context.Background(), "d\nserver=/x/1.2.3.4", nil); err == nil {
		t.Fatal("expected injection via domain to be rejected")
	}
}

func TestDnsmasqWritesValidRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domains.conf")
	p := NewDnsmasqProvider(path)

	if err := p.EnsureRecords(context.Background(), "mail1.test", []DNSRecord{
		{Type: "A", Name: "mail1.test", Value: "10.0.0.1"},
		{Type: "TXT", Name: "mail1.test", Value: "v=spf1 ip4:10.0.0.1 -all"},
	}); err != nil {
		t.Fatalf("valid records were rejected: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "address=/mail1.test/10.0.0.1") {
		t.Errorf("expected A record in config, got:\n%s", data)
	}
	if strings.Contains(string(data), "listen-address") {
		t.Errorf("unexpected injected directive present")
	}
}
