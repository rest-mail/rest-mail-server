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

// RequiredRecords generates the core DNS records needed for a mail domain
// (A, MX, SPF, DMARC). For the full set including autoconfig, MTA-STS, and
// TLS-RPT records, use FullRequiredRecords.
func RequiredRecords(domain, ip string) []DNSRecord {
	return []DNSRecord{
		{Type: "A", Name: domain, Value: ip, TTL: 3600},
		{Type: "MX", Name: domain, Value: domain, TTL: 3600, Priority: 10},
		{Type: "TXT", Name: domain, Value: "v=spf1 ip4:" + ip + " -all", TTL: 3600},
		{Type: "TXT", Name: "_dmarc." + domain, Value: "v=DMARC1; p=reject; rua=mailto:postmaster@" + domain, TTL: 3600},
	}
}

// FullRequiredRecords generates all DNS records needed for a fully-configured
// mail domain: core mail records plus autoconfig/autodiscover, SRV service
// discovery, MTA-STS (RFC 8461), and TLS-RPT (RFC 8460).
//
// mailIP is the IP of the mail server (MX host).
// apiIP is the IP of the API/web server (for autoconfig and MTA-STS policy
// serving). If apiIP is empty it defaults to mailIP.
func FullRequiredRecords(domain, mailIP, apiIP string) []DNSRecord {
	if apiIP == "" {
		apiIP = mailIP
	}

	records := RequiredRecords(domain, mailIP)

	// Autoconfig / autodiscover (email client auto-discovery)
	records = append(records,
		DNSRecord{Type: "A", Name: "autoconfig." + domain, Value: apiIP, TTL: 3600},
		DNSRecord{Type: "A", Name: "autodiscover." + domain, Value: apiIP, TTL: 3600},
	)

	// SRV records for email client auto-discovery (RFC 6186)
	records = append(records,
		DNSRecord{Type: "SRV", Name: "_submission._tcp." + domain, Value: domain, TTL: 3600, Priority: 10},
		DNSRecord{Type: "SRV", Name: "_imap._tcp." + domain, Value: domain, TTL: 3600, Priority: 10},
		DNSRecord{Type: "SRV", Name: "_imaps._tcp." + domain, Value: domain, TTL: 3600, Priority: 10},
		DNSRecord{Type: "SRV", Name: "_pop3._tcp." + domain, Value: domain, TTL: 3600, Priority: 10},
		DNSRecord{Type: "SRV", Name: "_pop3s._tcp." + domain, Value: domain, TTL: 3600, Priority: 10},
	)

	// MTA-STS (RFC 8461) - policy announcement TXT + policy-serving CNAME
	records = append(records,
		DNSRecord{Type: "TXT", Name: "_mta-sts." + domain, Value: "v=STSv1; id=1", TTL: 3600},
		DNSRecord{Type: "A", Name: "mta-sts." + domain, Value: apiIP, TTL: 3600},
	)

	// TLS-RPT (RFC 8460) - TLS failure reporting
	records = append(records,
		DNSRecord{Type: "TXT", Name: "_smtp._tls." + domain, Value: "v=TLSRPTv1; rua=mailto:tls-reports@" + domain, TTL: 3600},
	)

	return records
}
