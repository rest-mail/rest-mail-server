package handlers

import (
	"encoding/json"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// SenderRule is the API representation, independent of the database model.
type SenderRule struct {
	ID        uint      `json:"id"`
	DomainID  uint      `json:"domain_id"`
	Pattern   string    `json:"pattern"`                       // "spam@evil.com" or "@evil.com"
	ListType  string    `json:"list_type" enums:"allow,block"` // "allow" or "block"
	Reason    string    `json:"reason,omitempty"`
	CreatedBy *uint     `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func senderRuleOf(row models.DomainSenderRule) SenderRule {
	return SenderRule{
		ID:        row.ID,
		DomainID:  row.DomainID,
		Pattern:   row.Pattern,
		ListType:  row.ListType,
		Reason:    row.Reason,
		CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt,
	}
}

// Suppression is the API representation, independent of the database model.
type Suppression struct {
	ID              uint      `json:"id"`
	DomainID        uint      `json:"domain_id"`
	Address         string    `json:"address"`
	Reason          string    `json:"reason" enums:"hard_bounce"`
	FailureCategory string    `json:"failure_category" enums:"bad_mailbox,bad_domain"`
	LastError       string    `json:"last_error"`
	MessageID       *uint     `json:"message_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

func suppressionOf(row models.Suppression) Suppression {
	return Suppression{
		ID:              row.ID,
		DomainID:        row.DomainID,
		Address:         row.Address,
		Reason:          string(row.Reason),
		FailureCategory: string(row.FailureCategory),
		LastError:       row.LastError,
		MessageID:       row.MessageID,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		ExpiresAt:       row.ExpiresAt,
	}
}

// MTASTSPolicy is the API representation, independent of the database model.
type MTASTSPolicy struct {
	ID        uint      `json:"id"`
	DomainID  uint      `json:"domain_id"`
	Mode      string    `json:"mode" enums:"none,testing,enforce"` // none, testing, enforce
	MXHosts   string    `json:"mx_hosts"`                          // comma-separated
	MaxAge    int       `json:"max_age"`                           // seconds
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Domain    Domain    `json:"domain,omitempty"`
}

func mtaSTSPolicyOf(row models.MTASTSPolicy) MTASTSPolicy {
	return MTASTSPolicy{
		ID:        row.ID,
		DomainID:  row.DomainID,
		Mode:      row.Mode,
		MXHosts:   row.MXHosts,
		MaxAge:    row.MaxAge,
		Active:    row.Active,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		Domain:    domainOf(row.Domain),
	}
}

// TLSReport is the API representation, independent of the database model.
type TLSReport struct {
	ID              uint            `json:"id"`
	DomainID        uint            `json:"domain_id"`
	ReportingOrg    string          `json:"reporting_org"`
	StartDate       time.Time       `json:"start_date"`
	EndDate         time.Time       `json:"end_date"`
	PolicyType      string          `json:"policy_type" enums:"sts,tlsa,no-policy"` // "sts", "tlsa", "no-policy"
	PolicyDomain    string          `json:"policy_domain"`
	TotalSuccessful int64           `json:"total_successful"`
	TotalFailure    int64           `json:"total_failure"`
	FailureDetails  json.RawMessage `json:"failure_details,omitempty"`
	RawReport       string          `json:"raw_report,omitempty"`
	ReceivedAt      time.Time       `json:"received_at"`
	CreatedAt       time.Time       `json:"created_at"`
	Domain          Domain          `json:"domain,omitempty"`
}

func tlsReportOf(row models.TLSReport) TLSReport {
	return TLSReport{
		ID:              row.ID,
		DomainID:        row.DomainID,
		ReportingOrg:    row.ReportingOrg,
		StartDate:       row.StartDate,
		EndDate:         row.EndDate,
		PolicyType:      row.PolicyType,
		PolicyDomain:    row.PolicyDomain,
		TotalSuccessful: row.TotalSuccessful,
		TotalFailure:    row.TotalFailure,
		FailureDetails:  row.FailureDetails,
		RawReport:       row.RawReport,
		ReceivedAt:      row.ReceivedAt,
		CreatedAt:       row.CreatedAt,
		Domain:          domainOf(row.Domain),
	}
}
