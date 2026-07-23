package models

import "time"

// DMARCAggregateRecord captures one inbound message's DMARC evaluation, for
// later aggregation into RFC 7489 aggregate (rua) reports. One row is written
// per evaluated message whose From domain publishes a DMARC record; the
// reporter groups them by (domain, source IP, disposition, auth results) over a
// reporting period.
type DMARCAggregateRecord struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Domain      string    `gorm:"size:255;index:idx_dmarc_agg_domain_reported" json:"domain"` // header-From domain (the reported-on domain)
	SourceIP    string    `gorm:"size:64" json:"source_ip"`
	HeaderFrom  string    `gorm:"size:255" json:"header_from"`
	Disposition string    `gorm:"size:16" json:"disposition"` // none|quarantine|reject (policy applied)
	Policy      string    `gorm:"size:16" json:"policy"`      // published p=
	DKIMResult  string    `gorm:"size:16" json:"dkim_result"` // pass|fail|none
	DKIMAligned bool      `json:"dkim_aligned"`
	SPFResult   string    `gorm:"size:16" json:"spf_result"` // pass|fail|none
	SPFAligned  bool      `json:"spf_aligned"`
	Reported    bool      `gorm:"index:idx_dmarc_agg_domain_reported;default:false" json:"reported"`
	CreatedAt   time.Time `gorm:"index" json:"created_at"`
}
