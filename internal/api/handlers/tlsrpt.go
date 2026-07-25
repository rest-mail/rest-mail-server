package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
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

// TLS-RPT ingestion is unauthenticated — external MTAs POST reports per RFC 8460 —
// so it is bounded to stop it being used as a storage write-amplification vector:
// the request body is hard-capped, the number of policy entries per report is
// capped, and the route is rate-limited per client IP (wired in routes.go). Only
// reports whose policy-domain matches a domain this server hosts are stored; the
// rest are dropped. These caps are security invariants, not operator knobs.
const (
	// tlsRPTMaxBodyBytes caps the accepted request body. A legitimate aggregate
	// report is small; anything larger is rejected outright (413) rather than
	// silently truncated, so a caller cannot stream an unbounded body.
	tlsRPTMaxBodyBytes int64 = 512 << 10 // 512 KiB

	// tlsRPTMaxPolicies caps how many policy entries a single report may contain.
	// Each stored policy becomes a database row, so without this cap one accepted
	// body fans out into unbounded rows (write amplification). RFC 8460 reports
	// carry a handful of policies; 100 is far above any legitimate report.
	tlsRPTMaxPolicies = 100
)

// tlsRPTValidationErr is a structured validation failure the handler maps to an
// HTTP response. When fields is non-nil it is surfaced as a 422 field-validation
// error; otherwise status/code/message are used directly.
type tlsRPTValidationErr struct {
	status  int
	code    string
	message string
	fields  map[string]string
}

// validateTLSRPTReport enforces the RFC 8460 structure and the policy-count cap on
// an already-parsed report, returning the parsed date range on success. It performs
// NO database access and NO storage, so a malformed or over-large report is rejected
// before it can fan out into stored rows.
func validateTLSRPTReport(report *tlsRPTReport) (start, end time.Time, verr *tlsRPTValidationErr) {
	if report.OrganizationName == "" {
		return start, end, &tlsRPTValidationErr{fields: map[string]string{
			"organization-name": "Required field",
		}}
	}

	start, err := time.Parse(time.RFC3339, report.DateRange.StartDatetime)
	if err != nil {
		return start, end, &tlsRPTValidationErr{fields: map[string]string{
			"date-range.start-datetime": "Invalid RFC3339 datetime",
		}}
	}
	end, err = time.Parse(time.RFC3339, report.DateRange.EndDatetime)
	if err != nil {
		return start, end, &tlsRPTValidationErr{fields: map[string]string{
			"date-range.end-datetime": "Invalid RFC3339 datetime",
		}}
	}

	if len(report.Policies) == 0 {
		return start, end, &tlsRPTValidationErr{fields: map[string]string{
			"policies": "At least one policy entry is required",
		}}
	}
	if len(report.Policies) > tlsRPTMaxPolicies {
		return start, end, &tlsRPTValidationErr{
			status:  http.StatusRequestEntityTooLarge,
			code:    "too_many_policies",
			message: fmt.Sprintf("Report exceeds the maximum of %d policy entries", tlsRPTMaxPolicies),
		}
	}

	return start, end, nil
}

// ReceiveReport accepts a JSON TLS-RPT report from an external MTA,
// parses it, and stores each policy entry as a separate TLSReport row.
// POST /.well-known/smtp-tlsrpt
func (h *TLSReportHandler) ReceiveReport(w http.ResponseWriter, r *http.Request) {
	// Hard-cap the body: an oversized report is rejected (413), never silently
	// truncated, so an unauthenticated caller cannot stream an unbounded body.
	r.Body = http.MaxBytesReader(w, r.Body, tlsRPTMaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			respond.Error(w, http.StatusRequestEntityTooLarge, "payload_too_large",
				"TLS-RPT report exceeds the maximum accepted size")
			return
		}
		respond.Error(w, http.StatusBadRequest, "bad_request", "Failed to read request body")
		return
	}

	var report tlsRPTReport
	if err := json.Unmarshal(body, &report); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid TLS-RPT JSON")
		return
	}

	// Validate structure and enforce the policy-count cap BEFORE any storage, so a
	// malformed or over-large report cannot fan out into database rows.
	startDate, endDate, verr := validateTLSRPTReport(&report)
	if verr != nil {
		if verr.fields != nil {
			respond.ValidationError(w, verr.fields)
		} else {
			respond.Error(w, verr.status, verr.code, verr.message)
		}
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
		query = query.Where("reporting_org ILIKE ? ESCAPE '\\'", "%"+escapeLike(reportingOrg)+"%")
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
