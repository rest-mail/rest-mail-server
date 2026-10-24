package models

import "time"

// DMARCAggregateRecord captures one inbound message's DMARC evaluation, for
// later aggregation into RFC 9990 aggregate (rua) reports. One row is written
// per evaluated message whose From domain publishes a DMARC record; the
// reporter groups them by (domain, source IP, disposition, auth results) over a
// reporting period.
type DMARCAggregateRecord struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Domain      string `gorm:"size:255;index:idx_dmarc_agg_domain_reported" json:"domain"` // header-From domain (the reported-on domain)
	SourceIP    string `gorm:"size:64" json:"source_ip"`
	HeaderFrom  string `gorm:"size:255" json:"header_from"`
	Disposition string `gorm:"size:16" json:"disposition"` // none|quarantine|reject (policy applied)
	Policy      string `gorm:"size:16" json:"policy"`      // the policy the record requests for the domain
	// EnvelopeTo is the domain of the message's first recipient
	// (identifiers/envelope_to, RFC 9990 §3.1.1.10).
	EnvelopeTo string `gorm:"size:255" json:"envelope_to"`
	// DKIMDomain and DKIMSelector are the d= and s= of the signature the
	// report names: the passing aligned one if there is one, else a passing
	// one, else the first. Empty when the message carried no signature.
	DKIMDomain   string `gorm:"size:255" json:"dkim_domain"`
	DKIMSelector string `gorm:"size:255" json:"dkim_selector"`
	DKIMResult   string `gorm:"size:16" json:"dkim_result"` // pass|fail|none... (RFC 8601 §2.7.1)
	DKIMAligned  bool   `json:"dkim_aligned"`
	// SPFDomain is the MAIL FROM domain SPF checked, empty when SPF did not
	// check one.
	SPFDomain  string `gorm:"size:255" json:"spf_domain"`
	SPFResult  string `gorm:"size:16" json:"spf_result"` // pass|fail|softfail|none... (RFC 8601 §2.7.2)
	SPFAligned bool   `json:"spf_aligned"`
	// OverrideReason and OverrideComment say why Disposition differs from
	// Policy, when it does (RFC 9990 §3.1.6): a trusted ARC sealer's pass is a
	// trusted_forwarder, a mailing list's mail a mailing_list, and a policy
	// lowered by the record's test mode a policy_test_mode.
	OverrideReason  string    `gorm:"size:32" json:"override_reason,omitempty"`
	OverrideComment string    `gorm:"size:255" json:"override_comment,omitempty"`
	Reported        bool      `gorm:"index:idx_dmarc_agg_domain_reported;default:false" json:"reported"`
	CreatedAt       time.Time `gorm:"index" json:"created_at"`
}
