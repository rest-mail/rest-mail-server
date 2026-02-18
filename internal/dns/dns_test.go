package dns

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Factory tests
// ---------------------------------------------------------------------------

func TestNewProvider_Manual(t *testing.T) {
	p, err := NewProvider("manual")
	if err != nil {
		t.Fatalf("NewProvider(manual) returned error: %v", err)
	}
	if _, ok := p.(*ManualProvider); !ok {
		t.Fatalf("expected *ManualProvider, got %T", p)
	}
}

func TestNewProvider_Dnsmasq(t *testing.T) {
	p, err := NewProvider("dnsmasq")
	if err != nil {
		t.Fatalf("NewProvider(dnsmasq) returned error: %v", err)
	}
	if _, ok := p.(*DnsmasqProvider); !ok {
		t.Fatalf("expected *DnsmasqProvider, got %T", p)
	}
}

func TestNewProvider_ExternalDNS(t *testing.T) {
	p, err := NewProvider("externaldns")
	if err != nil {
		t.Fatalf("NewProvider(externaldns) returned error: %v", err)
	}
	if _, ok := p.(*ExternalDNSProvider); !ok {
		t.Fatalf("expected *ExternalDNSProvider, got %T", p)
	}
}

func TestNewProvider_Unknown(t *testing.T) {
	_, err := NewProvider("unknown")
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "unknown DNS provider") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNewProvider_DnsmasqWithOpts(t *testing.T) {
	p, err := NewProvider("dnsmasq", "/tmp/test.conf")
	if err != nil {
		t.Fatalf("NewProvider(dnsmasq, path) returned error: %v", err)
	}
	dp, ok := p.(*DnsmasqProvider)
	if !ok {
		t.Fatalf("expected *DnsmasqProvider, got %T", p)
	}
	if dp.configPath != "/tmp/test.conf" {
		t.Fatalf("expected configPath /tmp/test.conf, got %s", dp.configPath)
	}
}

func TestNewProvider_ExternalDNSWithOpts(t *testing.T) {
	p, err := NewProvider("externaldns", "/tmp/edns")
	if err != nil {
		t.Fatalf("NewProvider(externaldns, dir) returned error: %v", err)
	}
	ep, ok := p.(*ExternalDNSProvider)
	if !ok {
		t.Fatalf("expected *ExternalDNSProvider, got %T", p)
	}
	if ep.outputDir != "/tmp/edns" {
		t.Fatalf("expected outputDir /tmp/edns, got %s", ep.outputDir)
	}
}

// ---------------------------------------------------------------------------
// ManualProvider tests
// ---------------------------------------------------------------------------

func TestManualProvider_EnsureRecords(t *testing.T) {
	p := NewManualProvider()
	ctx := context.Background()
	records := RequiredRecords("example.test", "10.0.0.1")

	err := p.EnsureRecords(ctx, "example.test", records)
	if err != nil {
		t.Fatalf("ManualProvider.EnsureRecords returned error: %v", err)
	}
}

func TestManualProvider_RemoveRecords(t *testing.T) {
	p := NewManualProvider()
	ctx := context.Background()

	err := p.RemoveRecords(ctx, "example.test")
	if err != nil {
		t.Fatalf("ManualProvider.RemoveRecords returned error: %v", err)
	}
}

func TestManualProvider_VerifyRecords(t *testing.T) {
	p := NewManualProvider()
	ctx := context.Background()

	results, err := p.VerifyRecords(ctx, "example.test")
	if err != nil {
		t.Fatalf("ManualProvider.VerifyRecords returned error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// DnsmasqProvider tests
// ---------------------------------------------------------------------------

func TestDnsmasqProvider_EnsureRecords_WritesConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "domains.conf")
	p := NewDnsmasqProvider(configPath)
	ctx := context.Background()

	records := []DNSRecord{
		{Type: "A", Name: "mail.test", Value: "10.0.0.1", TTL: 3600},
		{Type: "MX", Name: "mail.test", Value: "mail.test", TTL: 3600, Priority: 10},
		{Type: "TXT", Name: "mail.test", Value: "v=spf1 ip4:10.0.0.1 -all", TTL: 3600},
		{Type: "PTR", Name: "1.0.0.10.in-addr.arpa", Value: "mail.test", TTL: 3600},
	}

	err := p.EnsureRecords(ctx, "mail.test", records)
	if err != nil {
		t.Fatalf("DnsmasqProvider.EnsureRecords returned error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	content := string(data)

	// Check comment header
	if !strings.Contains(content, "# DNS records for mail.test") {
		t.Error("config missing comment header")
	}

	// Check A record
	if !strings.Contains(content, "address=/mail.test/10.0.0.1") {
		t.Error("config missing A record: address=/mail.test/10.0.0.1")
	}

	// Check MX record
	if !strings.Contains(content, "mx-host=mail.test,mail.test,10") {
		t.Error("config missing MX record: mx-host=mail.test,mail.test,10")
	}

	// Check TXT record
	if !strings.Contains(content, `txt-record=mail.test,"v=spf1 ip4:10.0.0.1 -all"`) {
		t.Error("config missing TXT record")
	}

	// Check PTR record
	if !strings.Contains(content, "ptr-record=1.0.0.10.in-addr.arpa,mail.test") {
		t.Error("config missing PTR record")
	}

	// Verify file ends with newline
	if !strings.HasSuffix(content, "\n") {
		t.Error("config file should end with newline")
	}
}

func TestDnsmasqProvider_RemoveRecords(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "domains.conf")

	// Create the file first
	if err := os.WriteFile(configPath, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	p := NewDnsmasqProvider(configPath)
	ctx := context.Background()

	err := p.RemoveRecords(ctx, "mail.test")
	if err != nil {
		t.Fatalf("DnsmasqProvider.RemoveRecords returned error: %v", err)
	}

	// Verify file is gone
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatal("expected config file to be removed")
	}
}

func TestDnsmasqProvider_DefaultConfigPath(t *testing.T) {
	p := NewDnsmasqProvider("")
	if p.configPath != "/etc/dnsmasq.d/domains.conf" {
		t.Fatalf("expected default config path /etc/dnsmasq.d/domains.conf, got %s", p.configPath)
	}
}

func TestDnsmasqProvider_EnsureRecords_UnsupportedType(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "domains.conf")
	p := NewDnsmasqProvider(configPath)
	ctx := context.Background()

	records := []DNSRecord{
		{Type: "AAAA", Name: "mail.test", Value: "::1", TTL: 3600},
	}

	err := p.EnsureRecords(ctx, "mail.test", records)
	if err != nil {
		t.Fatalf("DnsmasqProvider.EnsureRecords returned error for unsupported type: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	content := string(data)

	// Should only contain the comment header, not the AAAA record
	if strings.Contains(content, "AAAA") {
		t.Error("config should not contain unsupported AAAA record")
	}
	if !strings.Contains(content, "# DNS records for mail.test") {
		t.Error("config should still contain the header comment")
	}
}

func TestDnsmasqProvider_VerifyRecords(t *testing.T) {
	p := NewDnsmasqProvider("")
	ctx := context.Background()

	results, err := p.VerifyRecords(ctx, "mail.test")
	if err != nil {
		t.Fatalf("DnsmasqProvider.VerifyRecords returned error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// ExternalDNSProvider tests
// ---------------------------------------------------------------------------

func TestExternalDNSProvider_EnsureRecords_WritesYAML(t *testing.T) {
	tmpDir := t.TempDir()
	p := NewExternalDNSProvider(tmpDir)
	ctx := context.Background()

	records := []DNSRecord{
		{Type: "A", Name: "mail.test", Value: "10.0.0.1", TTL: 3600},
		{Type: "MX", Name: "mail.test", Value: "mail.test", TTL: 3600, Priority: 10},
		{Type: "TXT", Name: "mail.test", Value: "v=spf1 ip4:10.0.0.1 -all", TTL: 3600},
	}

	err := p.EnsureRecords(ctx, "mail.test", records)
	if err != nil {
		t.Fatalf("ExternalDNSProvider.EnsureRecords returned error: %v", err)
	}

	yamlPath := filepath.Join(tmpDir, "restmail-mail-test.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("failed to read YAML file: %v", err)
	}
	content := string(data)

	// Verify apiVersion
	if !strings.Contains(content, "apiVersion: externaldns.k8s.io/v1alpha1") {
		t.Error("YAML missing apiVersion")
	}

	// Verify kind
	if !strings.Contains(content, "kind: DNSEndpoint") {
		t.Error("YAML missing kind")
	}

	// Verify metadata name
	if !strings.Contains(content, "  name: restmail-mail-test") {
		t.Error("YAML missing metadata name")
	}

	// Verify labels
	if !strings.Contains(content, "    app: restmail") {
		t.Error("YAML missing app label")
	}
	if !strings.Contains(content, "    domain: mail.test") {
		t.Error("YAML missing domain label")
	}

	// Verify spec section
	if !strings.Contains(content, "spec:") {
		t.Error("YAML missing spec section")
	}
	if !strings.Contains(content, "  endpoints:") {
		t.Error("YAML missing endpoints section")
	}

	// Verify A record target
	if !strings.Contains(content, `- "10.0.0.1"`) {
		t.Error("YAML missing A record target")
	}

	// Verify MX record formatted as "priority value"
	if !strings.Contains(content, `- "10 mail.test"`) {
		t.Error("YAML missing MX record in 'priority value' format")
	}

	// Verify recordType fields
	if !strings.Contains(content, "      recordType: A") {
		t.Error("YAML missing recordType A")
	}
	if !strings.Contains(content, "      recordType: MX") {
		t.Error("YAML missing recordType MX")
	}
	if !strings.Contains(content, "      recordType: TXT") {
		t.Error("YAML missing recordType TXT")
	}

	// Verify TTL
	if !strings.Contains(content, "      recordTTL: 3600") {
		t.Error("YAML missing recordTTL")
	}
}

func TestExternalDNSProvider_EnsureRecords_DefaultTTL(t *testing.T) {
	tmpDir := t.TempDir()
	p := NewExternalDNSProvider(tmpDir)
	ctx := context.Background()

	records := []DNSRecord{
		{Type: "A", Name: "mail.test", Value: "10.0.0.1", TTL: 0},
	}

	err := p.EnsureRecords(ctx, "mail.test", records)
	if err != nil {
		t.Fatalf("ExternalDNSProvider.EnsureRecords returned error: %v", err)
	}

	yamlPath := filepath.Join(tmpDir, "restmail-mail-test.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("failed to read YAML file: %v", err)
	}
	content := string(data)

	// When TTL is 0, it should default to 3600
	if !strings.Contains(content, "      recordTTL: 3600") {
		t.Error("YAML should default TTL to 3600 when TTL <= 0")
	}
}

func TestExternalDNSProvider_RemoveRecords(t *testing.T) {
	tmpDir := t.TempDir()
	p := NewExternalDNSProvider(tmpDir)
	ctx := context.Background()

	// Create a YAML file first
	yamlPath := filepath.Join(tmpDir, "restmail-mail-test.yaml")
	if err := os.WriteFile(yamlPath, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	err := p.RemoveRecords(ctx, "mail.test")
	if err != nil {
		t.Fatalf("ExternalDNSProvider.RemoveRecords returned error: %v", err)
	}

	// Verify file is gone
	if _, err := os.Stat(yamlPath); !os.IsNotExist(err) {
		t.Fatal("expected YAML file to be removed")
	}
}

func TestExternalDNSProvider_RemoveRecords_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	p := NewExternalDNSProvider(tmpDir)
	ctx := context.Background()

	// RemoveRecords on a domain with no file should not error
	err := p.RemoveRecords(ctx, "nonexistent.test")
	if err != nil {
		t.Fatalf("ExternalDNSProvider.RemoveRecords on non-existent file returned error: %v", err)
	}
}

func TestExternalDNSProvider_DefaultOutputDir(t *testing.T) {
	p := NewExternalDNSProvider("")
	if p.outputDir != "/etc/externaldns/" {
		t.Fatalf("expected default outputDir /etc/externaldns/, got %s", p.outputDir)
	}
}

func TestExternalDNSProvider_SanitizeDomain(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"mail.test", "mail-test"},
		{"sub.domain.example.com", "sub-domain-example-com"},
		{"nodots", "nodots"},
	}
	for _, tt := range tests {
		got := sanitizeDomain(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeDomain(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// RequiredRecords helper tests
// ---------------------------------------------------------------------------

func TestRequiredRecords(t *testing.T) {
	records := RequiredRecords("mail.test", "10.0.0.1")

	if len(records) != 4 {
		t.Fatalf("expected 4 records, got %d", len(records))
	}

	// A record
	a := records[0]
	if a.Type != "A" || a.Name != "mail.test" || a.Value != "10.0.0.1" {
		t.Errorf("unexpected A record: %+v", a)
	}

	// MX record
	mx := records[1]
	if mx.Type != "MX" || mx.Name != "mail.test" || mx.Value != "mail.test" || mx.Priority != 10 {
		t.Errorf("unexpected MX record: %+v", mx)
	}

	// SPF TXT record
	spf := records[2]
	if spf.Type != "TXT" || spf.Name != "mail.test" || spf.Value != "v=spf1 ip4:10.0.0.1 -all" {
		t.Errorf("unexpected SPF record: %+v", spf)
	}

	// DMARC TXT record
	dmarc := records[3]
	if dmarc.Type != "TXT" || dmarc.Name != "_dmarc.mail.test" {
		t.Errorf("unexpected DMARC record: %+v", dmarc)
	}
	if !strings.Contains(dmarc.Value, "v=DMARC1") {
		t.Errorf("DMARC record value should contain v=DMARC1, got %s", dmarc.Value)
	}
}

// ---------------------------------------------------------------------------
// FullRequiredRecords helper tests
// ---------------------------------------------------------------------------

func TestFullRequiredRecords_WithAPIIP(t *testing.T) {
	records := FullRequiredRecords("mail.test", "10.0.0.1", "10.0.0.5")

	byName := map[string]DNSRecord{}
	for _, r := range records {
		byName[r.Type+":"+r.Name] = r
	}

	// Core records should be present
	if _, ok := byName["A:mail.test"]; !ok {
		t.Error("missing A record for mail.test")
	}
	if _, ok := byName["MX:mail.test"]; !ok {
		t.Error("missing MX record")
	}
	if _, ok := byName["TXT:mail.test"]; !ok {
		t.Error("missing SPF record")
	}
	if _, ok := byName["TXT:_dmarc.mail.test"]; !ok {
		t.Error("missing DMARC record")
	}

	// Autoconfig records must point to apiIP
	ac := byName["A:autoconfig.mail.test"]
	if ac.Value != "10.0.0.5" {
		t.Errorf("autoconfig A should point to apiIP 10.0.0.5, got %s", ac.Value)
	}
	ad := byName["A:autodiscover.mail.test"]
	if ad.Value != "10.0.0.5" {
		t.Errorf("autodiscover A should point to apiIP 10.0.0.5, got %s", ad.Value)
	}

	// SRV records
	for _, srvName := range []string{
		"SRV:_submission._tcp.mail.test",
		"SRV:_imap._tcp.mail.test",
		"SRV:_imaps._tcp.mail.test",
		"SRV:_pop3._tcp.mail.test",
		"SRV:_pop3s._tcp.mail.test",
	} {
		if _, ok := byName[srvName]; !ok {
			t.Errorf("missing SRV record: %s", srvName)
		}
	}

	// MTA-STS records
	mtas := byName["TXT:_mta-sts.mail.test"]
	if !strings.Contains(mtas.Value, "v=STSv1") {
		t.Errorf("_mta-sts TXT should contain v=STSv1, got %s", mtas.Value)
	}
	mtaA := byName["A:mta-sts.mail.test"]
	if mtaA.Value != "10.0.0.5" {
		t.Errorf("mta-sts A should point to apiIP, got %s", mtaA.Value)
	}

	// TLS-RPT record
	tlsrpt := byName["TXT:_smtp._tls.mail.test"]
	if !strings.Contains(tlsrpt.Value, "v=TLSRPTv1") {
		t.Errorf("_smtp._tls TXT should contain v=TLSRPTv1, got %s", tlsrpt.Value)
	}
}

func TestFullRequiredRecords_DefaultsAPIIPToMailIP(t *testing.T) {
	records := FullRequiredRecords("mail.test", "10.0.0.1", "")

	byName := map[string]DNSRecord{}
	for _, r := range records {
		byName[r.Type+":"+r.Name] = r
	}

	// When apiIP is empty, autoconfig should point to mailIP
	ac := byName["A:autoconfig.mail.test"]
	if ac.Value != "10.0.0.1" {
		t.Errorf("autoconfig A should default to mailIP 10.0.0.1, got %s", ac.Value)
	}

	mtas := byName["A:mta-sts.mail.test"]
	if mtas.Value != "10.0.0.1" {
		t.Errorf("mta-sts A should default to mailIP, got %s", mtas.Value)
	}
}

// ---------------------------------------------------------------------------
// Integration-style: roundtrip EnsureRecords -> RemoveRecords for dnsmasq
// ---------------------------------------------------------------------------

func TestDnsmasqProvider_Roundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "roundtrip.conf")
	p := NewDnsmasqProvider(configPath)
	ctx := context.Background()

	records := RequiredRecords("roundtrip.test", "192.168.1.1")

	// Ensure
	if err := p.EnsureRecords(ctx, "roundtrip.test", records); err != nil {
		t.Fatalf("EnsureRecords failed: %v", err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file should exist after EnsureRecords: %v", err)
	}

	// Remove
	if err := p.RemoveRecords(ctx, "roundtrip.test"); err != nil {
		t.Fatalf("RemoveRecords failed: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatal("config file should not exist after RemoveRecords")
	}
}

// ---------------------------------------------------------------------------
// Integration-style: roundtrip EnsureRecords -> RemoveRecords for externaldns
// ---------------------------------------------------------------------------

func TestExternalDNSProvider_Roundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	p := NewExternalDNSProvider(tmpDir)
	ctx := context.Background()

	records := RequiredRecords("roundtrip.test", "192.168.1.1")

	// Ensure
	if err := p.EnsureRecords(ctx, "roundtrip.test", records); err != nil {
		t.Fatalf("EnsureRecords failed: %v", err)
	}

	yamlPath := filepath.Join(tmpDir, "restmail-roundtrip-test.yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		t.Fatalf("YAML file should exist after EnsureRecords: %v", err)
	}

	// Remove
	if err := p.RemoveRecords(ctx, "roundtrip.test"); err != nil {
		t.Fatalf("RemoveRecords failed: %v", err)
	}
	if _, err := os.Stat(yamlPath); !os.IsNotExist(err) {
		t.Fatal("YAML file should not exist after RemoveRecords")
	}
}
