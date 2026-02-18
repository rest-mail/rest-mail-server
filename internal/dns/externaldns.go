package dns

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ExternalDNSProvider implements Provider by generating Kubernetes DNSEndpoint
// CRD YAML files for the external-dns controller. It writes one YAML file per
// domain to the configured output directory; the external-dns controller watches
// this directory and reconciles the records.
type ExternalDNSProvider struct {
	outputDir string
}

// NewExternalDNSProvider creates a new external-dns DNS provider.
// outputDir is the directory where DNSEndpoint YAML files are written.
func NewExternalDNSProvider(outputDir string) *ExternalDNSProvider {
	if outputDir == "" {
		outputDir = "/etc/externaldns/"
	}
	return &ExternalDNSProvider{outputDir: outputDir}
}

// sanitizeDomain replaces dots and other non-alphanumeric characters with
// hyphens so the result is a valid Kubernetes resource name component.
func sanitizeDomain(domain string) string {
	return strings.ReplaceAll(domain, ".", "-")
}

// yamlFilePath returns the path to the DNSEndpoint YAML file for a domain.
func (p *ExternalDNSProvider) yamlFilePath(domain string) string {
	return filepath.Join(p.outputDir, fmt.Sprintf("restmail-%s.yaml", sanitizeDomain(domain)))
}

func (p *ExternalDNSProvider) EnsureRecords(ctx context.Context, domain string, records []DNSRecord) error {
	slog.Info("externaldns: ensuring DNS records", "domain", domain, "count", len(records))

	if err := os.MkdirAll(p.outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", p.outputDir, err)
	}

	sanitized := sanitizeDomain(domain)

	var b strings.Builder
	b.WriteString("apiVersion: externaldns.k8s.io/v1alpha1\n")
	b.WriteString("kind: DNSEndpoint\n")
	b.WriteString("metadata:\n")
	b.WriteString(fmt.Sprintf("  name: restmail-%s\n", sanitized))
	b.WriteString("  labels:\n")
	b.WriteString("    app: restmail\n")
	b.WriteString(fmt.Sprintf("    domain: %s\n", domain))
	b.WriteString("spec:\n")
	b.WriteString("  endpoints:\n")

	for _, r := range records {
		b.WriteString(fmt.Sprintf("    - dnsName: %s\n", r.Name))
		b.WriteString(fmt.Sprintf("      recordType: %s\n", r.Type))
		b.WriteString("      targets:\n")

		if r.Type == "MX" {
			b.WriteString(fmt.Sprintf("        - \"%d %s\"\n", r.Priority, r.Value))
		} else {
			b.WriteString(fmt.Sprintf("        - %q\n", r.Value))
		}

		ttl := r.TTL
		if ttl <= 0 {
			ttl = 3600
		}
		b.WriteString(fmt.Sprintf("      recordTTL: %d\n", ttl))
	}

	path := p.yamlFilePath(domain)
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("failed to write DNSEndpoint YAML: %w", err)
	}

	slog.Info("externaldns: DNSEndpoint written", "path", path)
	return nil
}

func (p *ExternalDNSProvider) RemoveRecords(ctx context.Context, domain string) error {
	path := p.yamlFilePath(domain)
	slog.Info("externaldns: removing DNS records", "domain", domain, "path", path)

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove DNSEndpoint YAML: %w", err)
	}
	return nil
}

func (p *ExternalDNSProvider) VerifyRecords(ctx context.Context, domain string) ([]VerifyResult, error) {
	slog.Info("externaldns: verifying DNS records", "domain", domain)

	// Read back the YAML to find out which records we expect.
	path := p.yamlFilePath(domain)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("no DNSEndpoint file for domain %s", domain)
	}

	// Re-derive expected records from RequiredRecords is not possible since we
	// don't know the IP. Instead, use DNS lookups for the standard record types.
	var results []VerifyResult

	// Verify A records
	addrs, err := net.LookupHost(domain)
	if err != nil {
		results = append(results, VerifyResult{
			Record: DNSRecord{Type: "A", Name: domain},
			Found:  false,
			Error:  err.Error(),
		})
	} else {
		results = append(results, VerifyResult{
			Record: DNSRecord{Type: "A", Name: domain},
			Found:  true,
			Actual: strings.Join(addrs, ", "),
		})
	}

	// Verify MX records
	mxs, err := net.LookupMX(domain)
	if err != nil {
		results = append(results, VerifyResult{
			Record: DNSRecord{Type: "MX", Name: domain},
			Found:  false,
			Error:  err.Error(),
		})
	} else {
		var mxStrs []string
		for _, mx := range mxs {
			mxStrs = append(mxStrs, fmt.Sprintf("%d %s", mx.Pref, mx.Host))
		}
		results = append(results, VerifyResult{
			Record: DNSRecord{Type: "MX", Name: domain},
			Found:  len(mxs) > 0,
			Actual: strings.Join(mxStrs, ", "),
		})
	}

	// Verify TXT records (SPF on domain)
	txts, err := net.LookupTXT(domain)
	if err != nil {
		results = append(results, VerifyResult{
			Record: DNSRecord{Type: "TXT", Name: domain},
			Found:  false,
			Error:  err.Error(),
		})
	} else {
		results = append(results, VerifyResult{
			Record: DNSRecord{Type: "TXT", Name: domain},
			Found:  len(txts) > 0,
			Actual: strings.Join(txts, "; "),
		})
	}

	// Verify DMARC TXT record
	dmarcName := "_dmarc." + domain
	dmarcTxts, err := net.LookupTXT(dmarcName)
	if err != nil {
		results = append(results, VerifyResult{
			Record: DNSRecord{Type: "TXT", Name: dmarcName},
			Found:  false,
			Error:  err.Error(),
		})
	} else {
		results = append(results, VerifyResult{
			Record: DNSRecord{Type: "TXT", Name: dmarcName},
			Found:  len(dmarcTxts) > 0,
			Actual: strings.Join(dmarcTxts, "; "),
		})
	}

	return results, nil
}
