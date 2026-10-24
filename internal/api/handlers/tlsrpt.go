package handlers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
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

	// failure-details is stored as raw JSON, so its shape is checked here: RFC
	// 8460 §4.4 makes it a list of failure objects.
	for i, pol := range report.Policies {
		if len(pol.FailureDetails) > 0 && !isJSONObjectList(pol.FailureDetails) {
			return start, end, &tlsRPTValidationErr{fields: map[string]string{
				fmt.Sprintf("policies[%d].failure-details", i): "Must be a list of failure objects",
			}}
		}
	}

	return start, end, nil
}

// isJSONObjectList reports whether raw is a JSON array whose every item is an
// object.
func isJSONObjectList(raw json.RawMessage) bool {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return false
	}
	for _, item := range items {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err != nil || obj == nil {
			return false
		}
	}
	return true
}

// compressedReport reports whether a report came gzip-compressed: its media type is
// application/tlsrpt+gzip (RFC 8460 §5.4), its encoding is gzip, or its bytes start as a gzip
// stream does (RFC 1952 §2.3.1), for a reporter that labels it otherwise.
func compressedReport(r *http.Request, body []byte) bool {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil &&
		strings.EqualFold(mediaType, "application/tlsrpt+gzip") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		return true
	}
	return len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b
}

// gunzipReport inflates a compressed report, held to the same cap as one sent plain, so a
// small compressed body cannot expand without bound. It answers the status to refuse with:
// 413 past the cap, 400 for a stream that is not gzip.
func gunzipReport(body []byte) ([]byte, int, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	defer func() { _ = reader.Close() }()
	inflated, err := io.ReadAll(io.LimitReader(reader, tlsRPTMaxBodyBytes+1))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if int64(len(inflated)) > tlsRPTMaxBodyBytes {
		return nil, http.StatusRequestEntityTooLarge, errors.New("decompressed report too large")
	}
	return inflated, 0, nil
}

// ReceiveReport accepts a JSON TLS-RPT report from an external MTA, plain or
// gzip-compressed, parses it, and stores each policy entry as a separate
// TLSReport row.
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

	// RFC 8460 §5.2 asks reporters to gzip their reports, and §5.4 names
	// application/tlsrpt+gzip for one sent compressed.
	if compressedReport(r, body) {
		inflated, status, err := gunzipReport(body)
		if err != nil {
			code, message := "bad_request", "Invalid gzip-compressed TLS-RPT report"
			if status == http.StatusRequestEntityTooLarge {
				code, message = "payload_too_large", "TLS-RPT report exceeds the maximum accepted size"
			}
			respond.Error(w, status, code, message)
			return
		}
		body = inflated
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

type listTLSReportsInput struct {
	DomainID     *uint32 `query:"domain_id" minimum:"0"`
	Limit        *int    `query:"limit" maximum:"200" message:"exceeds the maximum page size"`
	Offset       *int    `query:"offset"`
	PolicyType   *string `query:"policy_type" enums:"sts,tlsa,no-policy" message:"must be one of sts, tlsa, no-policy"`
	ReportingOrg string  `query:"reporting_org"`
}

// listReports returns TLS-RPT reports with optional filters and pagination.
func (h *TLSReportHandler) listReports(_ *http.Request, in listTLSReportsInput) (endpoint.List[TLSReport], error) {
	limit, offset := pagination(in.Limit, in.Offset, 50, 200)
	query := h.db.Model(&models.TLSReport{})
	if in.DomainID != nil {
		query = query.Where("domain_id = ?", *in.DomainID)
	}
	if in.PolicyType != nil {
		query = query.Where("policy_type = ?", *in.PolicyType)
	}
	if in.ReportingOrg != "" {
		query = query.Where("reporting_org ILIKE ? ESCAPE '\\'", "%"+escapeLike(in.ReportingOrg)+"%")
	}
	var total int64
	query.Count(&total)
	var reports []models.TLSReport
	if err := query.Order("received_at DESC").Limit(limit).Offset(offset).Find(&reports).Error; err != nil {
		return endpoint.List[TLSReport]{}, err
	}
	data := make([]TLSReport, len(reports))
	for i, row := range reports {
		data[i] = tlsReportOf(row)
	}
	return endpoint.List[TLSReport]{Data: data, Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, nil
}
