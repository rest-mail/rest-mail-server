package dns

import "context"

// DNSRecord represents a DNS record to be created/updated.
type DNSRecord struct {
	Type     string `json:"type"`     // A, MX, TXT, PTR, CNAME, SRV
	Name     string `json:"name"`     // e.g. "mail1.test", "_dmarc.mail1.test"
	Value    string `json:"value"`    // target hostname for MX/SRV; IP for A; text for TXT
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"` // MX preference; SRV priority
	Weight   int    `json:"weight"`   // SRV weight (0 = not applicable)
	Port     int    `json:"port"`     // SRV port (0 = not applicable)
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
