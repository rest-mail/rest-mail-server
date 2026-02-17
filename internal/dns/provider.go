package dns

import "context"

// DNSRecord represents a DNS record to be created/updated.
type DNSRecord struct {
	Type     string `json:"type"`     // A, MX, TXT, PTR, CNAME
	Name     string `json:"name"`     // e.g. "mail1.test", "_dmarc.mail1.test"
	Value    string `json:"value"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"` // for MX records
}

// VerifyResult represents the result of verifying a DNS record.
type VerifyResult struct {
	Record   DNSRecord `json:"record"`
	Found    bool      `json:"found"`
	Actual   string    `json:"actual,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// Provider is the interface for pluggable DNS providers.
type Provider interface {
	// EnsureRecords creates or updates DNS records for a domain.
	EnsureRecords(ctx context.Context, domain string, records []DNSRecord) error

	// RemoveRecords removes all DNS records for a domain.
	RemoveRecords(ctx context.Context, domain string) error

	// VerifyRecords checks that DNS records are propagated correctly.
	VerifyRecords(ctx context.Context, domain string) ([]VerifyResult, error)
}

// RequiredRecords generates the standard DNS records needed for a mail domain.
func RequiredRecords(domain, ip string) []DNSRecord {
	return []DNSRecord{
		{Type: "A", Name: domain, Value: ip, TTL: 3600},
		{Type: "MX", Name: domain, Value: domain, TTL: 3600, Priority: 10},
		{Type: "TXT", Name: domain, Value: "v=spf1 ip4:" + ip + " -all", TTL: 3600},
		{Type: "TXT", Name: "_dmarc." + domain, Value: "v=DMARC1; p=reject; rua=mailto:postmaster@" + domain, TTL: 3600},
	}
}
