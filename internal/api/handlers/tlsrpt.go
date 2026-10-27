package handlers

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/antimatter-studios/go-oapifly"
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

// forRequest gives the handler's SQL work the HTTP request's budget and
// cancellation without changing the shared handler.
func (h *TLSReportHandler) forRequest(r *http.Request) *TLSReportHandler {
	if h.db == nil {
		return h
	}
	return &TLSReportHandler{db: h.db.WithContext(r.Context())}
}

// TLSRPTReport is an inbound TLS-RPT aggregate report (RFC 8460 §4.4). Every level is open:
// a reporter may add fields of its own, and the report is stored as it was sent. Only what
// the handler needs is checked; a policy whose domain this server does not host, or that
// names none, is skipped rather than refused, so one entry cannot cost a report.
type TLSRPTReport struct {
	_                struct{}       `oapi:"open"`
	OrganizationName string         `json:"organization-name" minLength:"1" message:"Required field" missing:"Required field"`
	DateRange        TLSRPTRange    `json:"date-range"`
	ContactInfo      string         `json:"contact-info,omitempty"`
	ReportID         string         `json:"report-id,omitempty"`
	Policies         []TLSRPTPolicy `json:"policies" minItems:"1" maxItems:"100" message:"Must hold 1 to 100 policy entries" missing:"At least one policy entry is required"`
}

// TLSRPTRange is a report's date range, RFC 3339 times.
type TLSRPTRange struct {
	_             struct{} `oapi:"open"`
	StartDatetime string   `json:"start-datetime" format:"date-time" message:"Invalid RFC3339 datetime" missing:"Invalid RFC3339 datetime"`
	EndDatetime   string   `json:"end-datetime" format:"date-time" message:"Invalid RFC3339 datetime" missing:"Invalid RFC3339 datetime"`
}

// TLSRPTPolicy is one policy a report gives its sessions' results under.
type TLSRPTPolicy struct {
	_              struct{}                 `oapi:"open"`
	Policy         TLSRPTPolicyDesc         `json:"policy,omitempty"`
	Summary        TLSRPTSummary            `json:"summary,omitempty"`
	FailureDetails []map[string]interface{} `json:"failure-details,omitempty"`
}

// TLSRPTPolicyDesc is the policy a report's sessions were held to.
type TLSRPTPolicyDesc struct {
	_            struct{} `oapi:"open"`
	PolicyType   string   `json:"policy-type,omitempty"`
	PolicyString []string `json:"policy-string,omitempty"`
	PolicyDomain string   `json:"policy-domain,omitempty"`
	MXHost       []string `json:"mx-host,omitempty"`
}

// TLSRPTSummary counts a policy's sessions.
type TLSRPTSummary struct {
	_                           struct{} `oapi:"open"`
	TotalSuccessfulSessionCount int64    `json:"total-successful-session-count,omitempty"`
	TotalFailureSessionCount    int64    `json:"total-failure-session-count,omitempty"`
}

// TLS-RPT ingestion is unauthenticated — external MTAs POST reports per RFC 8460 —
// so it is bounded to stop it being used as a storage write-amplification vector:
// the request body is hard-capped, compressed or not, the number of policy entries
// per report is capped (TLSRPTReport.Policies), and the route is rate-limited per
// client IP (wired in routes.go). Only reports whose policy-domain matches a domain
// this server hosts are stored; the rest are dropped. These caps are security
// invariants, not operator knobs.
//
// tlsRPTMaxBodyBytes caps the accepted request body, and the report a compressed
// one inflates to. A legitimate aggregate report is small; anything larger is
// rejected outright (413) rather than silently truncated, so a caller cannot stream
// an unbounded body.
const tlsRPTMaxBodyBytes int64 = 512 << 10 // 512 KiB

// receiveTLSReportInput is a report as RFC 8460 §5.4 sends it: JSON, under its own media
// type or plain application/json, or gzip-compressed.
type receiveTLSReportInput struct {
	Report *TLSRPTReport `body:"application/tlsrpt+json,application/json"`
	Gzip   *[]byte       `body:"application/tlsrpt+gzip" description:"The report, gzip-compressed (RFC 1952)."`
}

// tlsReportStored is how many of a report's policy entries were stored.
type tlsReportStored struct {
	Stored int `json:"stored" description:"Number of policy entries persisted"`
}

// A structurally valid report may name no hosted domain. That refusal has no
// validation fields; binding failures carry the same typed field details as before.
type tlsReportUnprocessable struct {
	Error tlsReportUnprocessableError `json:"error"`
}

type tlsReportUnprocessableError struct {
	Code    string                    `json:"code" enums:"validation_failed,no_matching_domains"`
	Message string                    `json:"message"`
	Details respond.ValidationDetails `json:"details,omitempty"`
}

// RegisterReceive registers report ingestion (RFC 8460 §5.4) behind the per-IP throttle.
func (h *TLSReportHandler) RegisterReceive(rs endpoint.Routes, throttle endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/.well-known/smtp-tlsrpt",
		OperationID: "receiveTlsReport", Summary: "TLS-RPT report ingestion (RFC 8460)", Tags: []string{"System"},
		Description: "Receives TLS-RPT reports from external MTAs, as JSON or gzip-compressed. No auth by design. " +
			"A body sent with Content-Encoding: gzip, or gzip-compressed under a JSON media type, is inflated first.",
		Status: http.StatusCreated, Security: oapifly.Public,
		Responses: []oapifly.EndpointResponse{{Status: http.StatusUnprocessableEntity,
			Description: "Invalid report fields, or no policy domain hosted by this server",
			Type:        oapifly.TypeOf[tlsReportUnprocessable]()}},
		Middleware: []endpoint.Middleware{throttle, {Wrap: inflateReport}},
	}, h.receiveReport)
}

// inflateReport caps a report's body and, for one that came gzip-compressed other than
// under application/tlsrpt+gzip, inflates it (the inflated report held to the same cap), so a
// small compressed body cannot expand without bound. RFC 8460 §5.2 asks reporters to gzip
// their reports: one may say so with Content-Encoding: gzip, or label the compressed bytes as
// JSON. A stream that is not gzip is the 400.
func inflateReport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, tlsRPTMaxBodyBytes)
		buffered := bufio.NewReader(r.Body)
		head, _ := buffered.Peek(2)
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		encoded := strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip")
		labelled := strings.EqualFold(mediaType, "application/tlsrpt+gzip")
		sniffed := !labelled && len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b
		body := io.ReadCloser(struct {
			io.Reader
			io.Closer
		}{buffered, r.Body})
		if encoded || sniffed {
			inflated, err := gzip.NewReader(buffered)
			if err != nil {
				respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid gzip-compressed TLS-RPT report")
				return
			}
			body = http.MaxBytesReader(w, inflated, tlsRPTMaxBodyBytes)
			r.Header.Del("Content-Encoding")
			if labelled || mediaType == "" {
				r.Header.Set("Content-Type", "application/tlsrpt+json")
			}
		}
		r.Body = body
		next.ServeHTTP(w, r)
	})
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

// receiveReport stores each policy entry of a report for a domain this server hosts as a
// TLSReport row, with the report as it was sent.
func (h *TLSReportHandler) receiveReport(r *http.Request, in receiveTLSReportInput) (tlsReportStored, error) {
	var raw []byte
	report := in.Report
	if in.Gzip != nil {
		inflated, status, err := gunzipReport(*in.Gzip)
		if err != nil {
			// The refusals a body the framework reads gives: past the cap it is the
			// same 413, inflated or not.
			code, message := "bad_request", "Invalid gzip-compressed TLS-RPT report"
			if status == http.StatusRequestEntityTooLarge {
				code, message = "request_too_large", "Request body too large"
			}
			return tlsReportStored{}, endpoint.Refuse(status, code, message)
		}
		report = &TLSRPTReport{}
		if err := endpoint.BindBody(report, inflated); err != nil {
			return tlsReportStored{}, err
		}
		raw = inflated
	} else {
		// The bound body, read again as it was sent, so a reporter's own fields are kept.
		read, err := io.ReadAll(r.Body)
		if err != nil {
			return tlsReportStored{}, endpoint.Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
		}
		raw = read
	}

	var stored tlsReportStored
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		stored, err = NewTLSReportHandler(tx).storeReport(report, raw)
		return err
	})
	if err != nil {
		return tlsReportStored{}, err
	}
	return stored, nil
}

// storeReport is shared by HTTP and authenticated MIME report ingestion.
func (h *TLSReportHandler) storeReport(report *TLSRPTReport, raw []byte) (tlsReportStored, error) {
	startDate, _ := time.Parse(time.RFC3339, report.DateRange.StartDatetime)
	endDate, _ := time.Parse(time.RFC3339, report.DateRange.EndDatetime)
	now := time.Now()
	rawReport := string(raw)
	stored := 0

	for _, pol := range report.Policies {
		domainName := strings.TrimSuffix(pol.Policy.PolicyDomain, ".")
		if domainName == "" {
			continue
		}
		// Look up the domain in our database; skip unknown domains
		var domain models.Domain
		if err := h.db.Where("name = ?", domainName).First(&domain).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return tlsReportStored{}, err
		}

		// "no-policy-found" is the RFC term; it and anything unknown are stored as
		// "no-policy".
		policyType := pol.Policy.PolicyType
		switch policyType {
		case "sts", "tlsa":
		default:
			policyType = "no-policy"
		}

		var failureDetails json.RawMessage
		if len(pol.FailureDetails) > 0 {
			encoded, err := json.Marshal(pol.FailureDetails)
			if err != nil {
				return tlsReportStored{}, err
			}
			failureDetails = encoded
		}

		row := models.TLSReport{
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
		if err := h.db.Create(&row).Error; err != nil {
			return tlsReportStored{}, err
		}
		stored++
	}

	if stored == 0 {
		return tlsReportStored{}, endpoint.Refuse(http.StatusUnprocessableEntity, "no_matching_domains",
			"None of the policy domains in the report match a known domain")
	}
	return tlsReportStored{Stored: stored}, nil
}

type listTLSReportsInput struct {
	DomainID     *uint32 `query:"domain_id" minimum:"0"`
	Limit        *int    `query:"limit" maximum:"200" message:"exceeds the maximum page size"`
	Offset       *int    `query:"offset"`
	PolicyType   *string `query:"policy_type" enums:"sts,tlsa,no-policy" message:"must be one of sts, tlsa, no-policy"`
	ReportingOrg string  `query:"reporting_org"`
}

// listReports returns TLS-RPT reports with optional filters and pagination.
func (h *TLSReportHandler) listReports(r *http.Request, in listTLSReportsInput) (endpoint.List[TLSReport], error) {
	h = h.forRequest(r)
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
