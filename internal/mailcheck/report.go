package mailcheck

import (
	"encoding/json"
	"time"
)

// Status represents the outcome of a single check.
type Status int

const (
	StatusPass Status = iota
	StatusWarn
	StatusFail
	StatusSkip
	StatusError
)

func (s Status) String() string {
	switch s {
	case StatusPass:
		return "PASS"
	case StatusWarn:
		return "WARN"
	case StatusFail:
		return "FAIL"
	case StatusSkip:
		return "SKIP"
	case StatusError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

func (s Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// CheckResult holds the outcome of a single diagnostic check.
type CheckResult struct {
	Name     string        `json:"name"`
	Category string        `json:"category"`
	Status   Status        `json:"status"`
	Summary  string        `json:"summary"`
	Detail   string        `json:"detail,omitempty"`
	Fix      string        `json:"fix,omitempty"`
	Duration time.Duration `json:"duration_ms"`
}

// Report is the full output of an Instant Mail Check run.
type Report struct {
	Domain    string        `json:"domain"`
	MXHosts   []string      `json:"mx_hosts"`
	Checks    []CheckResult `json:"checks"`
	Score     int           `json:"score"`
	MaxScore  int           `json:"max_score"`
	Timestamp time.Time     `json:"timestamp"`
}

// ScoreWeights defines how many points each check is worth.
var ScoreWeights = map[string]int{
	// DNS
	"MX Records":        5,
	"SPF Record":        10,
	"DKIM Record":       10,
	"DMARC Record":      10,
	"Reverse DNS (PTR)": 5,
	"DANE/TLSA":         5,
	"MTA-STS":           5,
	"TLS-RPT":           5,
	"DNSSEC":                 3,
	"CAA Records":            2,
	"BIMI Record":            2,
	"Forward-Confirmed rDNS": 5,
	"IPv6 Readiness":         2,
	"Client Autoconfig":      2,
	// Connection
	"SMTP Banner":           3,
	"SMTP STARTTLS":         5,
	"SMTP TLS Certificate":  10,
	"Submission Port 587":   5,
	"SMTPS Port 465":        3,
	"SMTP Extensions":       1,
	"IMAPS Port 993":        5,
	"IMAPS TLS Certificate": 5,
	"POP3S Port 995":        2,
	"Open Relay Test":       10,
	"IP Blacklists":         10,
	"Domain Blacklists":     5,
	// Security (Tier 1)
	"Banner Info Leak":    5,
	"VRFY/EXPN Commands":  5,
	"Plaintext Ports":     8,
	"TLS Minimum Version": 8,
	"Self-Signed Cert":    5,
	// Auth (Tier 3)
	"IMAP Login":              5,
	"POP3 Login":              3,
	"Authenticated SMTP Send": 5,
	"Email Round-Trip":        10,
	"Header Analysis":         5,
	"SPF Alignment":           5,
	"Spam Score Estimate":     3,
	"ARC Chain":               1,
	"IMAP Capabilities":       2,
	"IMAP IDLE Support":       2,
	"Mailbox Quota":           2,
	"Password Strength":       3,
	"Auth Mechanisms":         3,
	"Plaintext Auth":          8,
	// Security Audit (Tier 4)
	"User Enumeration (RCPT)": 8,
	"User Enumeration (VRFY)": 5,
	"Brute-Force Protection":  10,
	"SMTP Smuggling":          8,
	"Rate Limiting":           5,
}

// CalculateScore computes the score for a report based on check results.
func (r *Report) CalculateScore() {
	r.Score = 0
	r.MaxScore = 0
	for _, c := range r.Checks {
		weight, ok := ScoreWeights[c.Name]
		if !ok {
			weight = 1
		}
		r.MaxScore += weight
		switch c.Status {
		case StatusPass:
			r.Score += weight
		case StatusWarn:
			r.Score += weight / 2
		}
	}
}

// Percentage returns the score as a percentage (0-100).
func (r *Report) Percentage() int {
	if r.MaxScore == 0 {
		return 0
	}
	return (r.Score * 100) / r.MaxScore
}

// Options configures which checks to run.
type Options struct {
	Domain        string
	DKIMSelector  string
	SendTo        string
	User          string
	Pass          string
	Verbose       bool
	JSON          bool
	SecurityAudit bool
	Timeout       time.Duration
}
