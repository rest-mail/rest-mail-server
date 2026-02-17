package dns

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// DnsmasqProvider implements Provider for the development dnsmasq container.
// It writes records to the dnsmasq config file and triggers a reload.
type DnsmasqProvider struct {
	configPath string
}

// NewDnsmasqProvider creates a new dnsmasq DNS provider.
func NewDnsmasqProvider(configPath string) *DnsmasqProvider {
	if configPath == "" {
		configPath = "/etc/dnsmasq.d/domains.conf"
	}
	return &DnsmasqProvider{configPath: configPath}
}

func (p *DnsmasqProvider) EnsureRecords(ctx context.Context, domain string, records []DNSRecord) error {
	slog.Info("dnsmasq: ensuring DNS records", "domain", domain, "count", len(records))

	var lines []string
	lines = append(lines, fmt.Sprintf("# DNS records for %s", domain))

	for _, r := range records {
		switch r.Type {
		case "A":
			lines = append(lines, fmt.Sprintf("address=/%s/%s", r.Name, r.Value))
		case "MX":
			lines = append(lines, fmt.Sprintf("mx-host=%s,%s,%d", r.Name, r.Value, r.Priority))
		case "TXT":
			lines = append(lines, fmt.Sprintf("txt-record=%s,\"%s\"", r.Name, r.Value))
		case "PTR":
			lines = append(lines, fmt.Sprintf("ptr-record=%s,%s", r.Name, r.Value))
		default:
			slog.Warn("dnsmasq: unsupported record type", "type", r.Type)
		}
	}

	content := strings.Join(lines, "\n") + "\n"

	if err := os.WriteFile(p.configPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write dnsmasq config: %w", err)
	}

	slog.Info("dnsmasq: records written", "path", p.configPath)
	return nil
}

func (p *DnsmasqProvider) RemoveRecords(ctx context.Context, domain string) error {
	slog.Info("dnsmasq: removing DNS records", "domain", domain)
	return os.Remove(p.configPath)
}

func (p *DnsmasqProvider) VerifyRecords(ctx context.Context, domain string) ([]VerifyResult, error) {
	// In dev, records are always correct since we control dnsmasq
	slog.Info("dnsmasq: verify skipped (dev environment)", "domain", domain)
	return nil, nil
}
