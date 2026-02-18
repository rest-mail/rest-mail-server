package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// TLSReportHandler handles TLS-RPT (RFC 8460) report ingestion and listing.
type TLSReportHandler struct {
	db *gorm.DB
}

func NewTLSReportHandler(db *gorm.DB) *TLSReportHandler {
	return &TLSReportHandler{db: db}
}

// tlsRPTReport represents the top-level JSON structure of a TLS-RPT report
// as defined in RFC 8460 Section 4.
type tlsRPTReport struct {
	OrganizationName string         `json:"organization-name"`
	DateRange        tlsRPTRange    `json:"date-range"`
	ContactInfo      string         `json:"contact-info"`
	ReportID         string         `json:"report-id"`
	Policies         []tlsRPTPolicy `json:"policies"`
}

type tlsRPTRange struct {
	StartDatetime string `json:"start-datetime"`
	EndDatetime   string `json:"end-datetime"`
}

type tlsRPTPolicy struct {
	Policy  tlsRPTPolicyDesc `json:"policy"`
	Summary tlsRPTSummary    `json:"summary"`
	// FailureDetails is kept as raw JSON for flexible storage
	FailureDetails json.RawMessage `json:"failure-details,omitempty"`
}

type tlsRPTPolicyDesc struct {
	PolicyType   string   `json:"policy-type"`
	PolicyString []string `json:"policy-string"`
	PolicyDomain string   `json:"policy-domain"`
	MXHost       []string `json:"mx-host"`
}

type tlsRPTSummary struct {
	TotalSuccessfulSessionCount int64 `json:"total-successful-session-count"`
	TotalFailureSessionCount    int64 `json:"total-failure-session-count"`
}

// ReceiveReport accepts a JSON TLS-RPT report from an external MTA,
// parses it, and stores each policy entry as a separate TLSReport row.
// POST /.well-known/smtp-tlsrpt
func (h *TLSReportHandler) ReceiveReport(w http.ResponseWriter, r *http.Request) {
	// Limit body to 1MB to prevent abuse
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Failed to read request body")
		return
	}

	var report tlsRPTReport
	if err := json.Unmarshal(body, &report); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid TLS-RPT JSON")
		return
	}

	if report.OrganizationName == "" {
		respond.ValidationError(w, map[string]string{
			"organization-name": "Required field",
		})
		return
	}

	// Parse date range
	startDate, err := time.Parse(time.RFC3339, report.DateRange.StartDatetime)
	if err != nil {
		respond.ValidationError(w, map[string]string{
			"date-range.start-datetime": "Invalid RFC3339 datetime",
		})
		return
	}
	endDate, err := time.Parse(time.RFC3339, report.DateRange.EndDatetime)
	if err != nil {
		respond.ValidationError(w, map[string]string{
			"date-range.end-datetime": "Invalid RFC3339 datetime",
		})
		return
	}

	if len(report.Policies) == 0 {
		respond.ValidationError(w, map[string]string{
			"policies": "At least one policy entry is required",
		})
		return
	}

	now := time.Now()
	rawReport := string(body)
	var created []models.TLSReport

	for _, pol := range report.Policies {
		policyDomain := pol.Policy.PolicyDomain
		if policyDomain == "" {
			continue
		}

		// Look up the domain in our database; skip unknown domains
		var domain models.Domain
		domainName := strings.TrimSuffix(policyDomain, ".")
		if err := h.db.Where("name = ?", domainName).First(&domain).Error; err != nil {
			continue
		}

		// Validate policy type
		policyType := pol.Policy.PolicyType
		switch policyType {
		case "sts", "tlsa", "no-policy-found":
			// "no-policy-found" is the RFC term; normalize to "no-policy" for storage
			if policyType == "no-policy-found" {
				policyType = "no-policy"
			}
		default:
			policyType = "no-policy"
		}

		// Marshal failure details back to JSON for storage
		var failureDetails json.RawMessage
		if len(pol.FailureDetails) > 0 {
			failureDetails = pol.FailureDetails
		}

		tlsReport := models.TLSReport{
			DomainID:        domain.ID,
			ReportingOrg:    report.OrganizationName,
			StartDate:       startDate,
			EndDate:         endDate,
			PolicyType:      policyType,
			PolicyDomain:    domainName,
			TotalSuccessful: pol.Summary.TotalSuccessfulSessionCount,
			TotalFailure:    pol.Summary.TotalFailureSessionCount,
			FailureDetails:  failureDetails,
			RawReport:       rawReport,
			ReceivedAt:      now,
		}

		if err := h.db.Create(&tlsReport).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to store TLS report")
			return
		}
		created = append(created, tlsReport)
	}

	if len(created) == 0 {
		respond.Error(w, http.StatusUnprocessableEntity, "no_matching_domains",
			"None of the policy domains in the report match a known domain")
		return
	}

	respond.Data(w, http.StatusCreated, map[string]interface{}{
		"stored": len(created),
	})
}

// ListReports returns TLS-RPT reports with optional domain_id filtering and pagination.
// GET /api/v1/admin/tls-reports?domain_id=X&limit=50&offset=0
func (h *TLSReportHandler) ListReports(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	query := h.db.Model(&models.TLSReport{})

	if domainID := r.URL.Query().Get("domain_id"); domainID != "" {
		if id, err := strconv.ParseUint(domainID, 10, 32); err == nil {
			query = query.Where("domain_id = ?", id)
		} else {
			respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain_id")
			return
		}
	}

	if policyType := r.URL.Query().Get("policy_type"); policyType != "" {
		query = query.Where("policy_type = ?", policyType)
	}

	if reportingOrg := r.URL.Query().Get("reporting_org"); reportingOrg != "" {
		query = query.Where("reporting_org ILIKE ?", "%"+reportingOrg+"%")
	}

	var total int64
	query.Count(&total)

	var reports []models.TLSReport
	if err := query.Order("received_at DESC").Limit(limit).Offset(offset).Find(&reports).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list TLS reports")
		return
	}

	respond.List(w, reports, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}
