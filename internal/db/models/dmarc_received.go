package models

import "time"

type DMARCReportReceived struct {
	ID              uint                  `gorm:"primaryKey" json:"id"`
	DomainID        uint                  `gorm:"index;not null" json:"domain_id"`
	Reporter        string                `gorm:"size:255;not null;uniqueIndex:idx_dmarc_received_identity" json:"reporter" maxLength:"255"`
	ReportingOrg    string                `gorm:"size:255;not null" json:"reporting_org" maxLength:"255"`
	ReportID        string                `gorm:"size:255;not null;uniqueIndex:idx_dmarc_received_identity" json:"report_id" maxLength:"255"`
	StartDate       time.Time             `gorm:"not null" json:"start_date"`
	EndDate         time.Time             `gorm:"not null" json:"end_date"`
	PolicyPublished DMARCReceivedPolicy   `gorm:"serializer:json;type:jsonb;not null" json:"policy_published"`
	Records         []DMARCReceivedRecord `gorm:"serializer:json;type:jsonb;not null" json:"records" minItems:"1" maxItems:"100"`
	ReceivedAt      time.Time             `gorm:"index;not null" json:"received_at"`
}

func (DMARCReportReceived) TableName() string { return "dmarc_reports_received" }

type DMARCReceivedPolicy struct {
	Domain          string `xml:"domain" json:"domain" maxLength:"255"`
	ADKIM           string `xml:"adkim" json:"adkim,omitempty" maxLength:"1"`
	ASPF            string `xml:"aspf" json:"aspf,omitempty" maxLength:"1"`
	P               string `xml:"p" json:"p" enums:"none,quarantine,reject"`
	SP              string `xml:"sp" json:"sp,omitempty" maxLength:"10"`
	NP              string `xml:"np" json:"np,omitempty" maxLength:"10"`
	Pct             *uint  `xml:"pct" json:"pct,omitempty" maximum:"100"`
	FO              string `xml:"fo" json:"fo,omitempty" maxLength:"32"`
	Testing         string `xml:"testing" json:"testing,omitempty" maxLength:"1"`
	DiscoveryMethod string `xml:"discovery_method" json:"discovery_method,omitempty" maxLength:"32"`
}

type DMARCReceivedRecord struct {
	Row         DMARCReceivedRow         `xml:"row" json:"row"`
	Identifiers DMARCReceivedIdentifiers `xml:"identifiers" json:"identifiers"`
	AuthResults DMARCReceivedAuthResults `xml:"auth_results" json:"auth_results"`
}

type DMARCReceivedRow struct {
	SourceIP        string                  `xml:"source_ip" json:"source_ip" maxLength:"45"`
	Count           int64                   `xml:"count" json:"count" minimum:"1" maximum:"9007199254740991"`
	PolicyEvaluated DMARCReceivedEvaluation `xml:"policy_evaluated" json:"policy_evaluated"`
}

type DMARCReceivedEvaluation struct {
	Disposition string                `xml:"disposition" json:"disposition" enums:"none,pass,quarantine,reject"`
	DKIM        string                `xml:"dkim" json:"dkim" enums:"pass,fail"`
	SPF         string                `xml:"spf" json:"spf" enums:"pass,fail"`
	Reasons     []DMARCReceivedReason `xml:"reason" json:"reasons,omitempty" maxItems:"10"`
}

type DMARCReceivedReason struct {
	Type    string `xml:"type" json:"type" maxLength:"1024"`
	Comment string `xml:"comment" json:"comment,omitempty" maxLength:"1024"`
}

type DMARCReceivedIdentifiers struct {
	HeaderFrom   string `xml:"header_from" json:"header_from" maxLength:"255"`
	EnvelopeFrom string `xml:"envelope_from" json:"envelope_from,omitempty" maxLength:"1024"`
	EnvelopeTo   string `xml:"envelope_to" json:"envelope_to,omitempty" maxLength:"1024"`
}

type DMARCReceivedAuthResults struct {
	DKIM []DMARCReceivedDKIM `xml:"dkim" json:"dkim,omitempty" maxItems:"10"`
	SPF  []DMARCReceivedSPF  `xml:"spf" json:"spf,omitempty" maxItems:"10"`
}

type DMARCReceivedDKIM struct {
	Domain      string `xml:"domain" json:"domain" maxLength:"255"`
	Selector    string `xml:"selector" json:"selector,omitempty" maxLength:"255"`
	Result      string `xml:"result" json:"result" maxLength:"32"`
	HumanResult string `xml:"human_result" json:"human_result,omitempty" maxLength:"1024"`
}

type DMARCReceivedSPF struct {
	Domain      string `xml:"domain" json:"domain" maxLength:"255"`
	Scope       string `xml:"scope" json:"scope,omitempty" maxLength:"32"`
	Result      string `xml:"result" json:"result" maxLength:"32"`
	HumanResult string `xml:"human_result" json:"human_result,omitempty" maxLength:"1024"`
}
