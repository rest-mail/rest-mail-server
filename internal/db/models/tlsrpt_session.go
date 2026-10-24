package models

import "time"

// TLSRPTSession is the TLS outcome of one outbound SMTP session, counted into
// the daily TLS-RPT report REST Mail sends the recipient's Policy Domain (RFC
// 8460 §4). A day's rows are deleted once its reports are sent.
type TLSRPTSession struct {
	ID uint `gorm:"primaryKey"`
	// Day is the UTC day of the session, YYYY-MM-DD; a report covers one
	// (§4.1).
	Day string `gorm:"size:10;not null;index:idx_tlsrpt_sessions_day_domain"`
	// PolicyDomain is the recipient domain, in A-label form (§4.4).
	PolicyDomain string `gorm:"size:255;not null;index:idx_tlsrpt_sessions_day_domain"`
	// PolicyType is "sts" for a session under an MTA-STS policy and
	// "no-policy-found" otherwise.
	PolicyType string `gorm:"size:20;not null"`
	// PolicyString and MXHost are the applied MTA-STS policy's lines and MX
	// patterns, as JSON arrays, and empty without a policy (§4.4, §4.5).
	PolicyString string `gorm:"type:text"`
	MXHost       string `gorm:"type:text"`
	ReceivingMX  string `gorm:"size:255"`
	ReceivingIP  string `gorm:"size:64"`
	SendingIP    string `gorm:"size:64"`
	// ResultType is empty for a session that negotiated policy-compliant TLS,
	// and otherwise its result type (§4.3); FailureReason says more.
	ResultType    string `gorm:"size:40"`
	FailureReason string `gorm:"size:255"`
	CreatedAt     time.Time
}
