package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

type DMARCReport models.DMARCReportReceived

type DMARCReportHandler struct{ db *gorm.DB }

func NewDMARCReportHandler(db *gorm.DB) *DMARCReportHandler { return &DMARCReportHandler{db: db} }

func (h *DMARCReportHandler) Register(rs endpoint.Routes, onList func(string) endpoint.Middleware) {
	checks := []endpoint.Middleware{onList(middleware.CapDomainsRead)}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/dmarc-reports", OperationID: "listDmarcReports", Summary: "List received DMARC aggregate reports", Description: "Domain-scoped administrators read only their own domains' reports and pagination totals. Installation-wide access requires domains:read at *. Domain and reporter filters intersect that scope. Reports are received by mail, not by this API.", Tags: []string{"Admin: Domain mail policy"}, Middleware: checks}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/dmarc-reports/{id}", OperationID: "getDmarcReport", Summary: "Read a received DMARC aggregate report", Description: "Requires domains:read for the report's domain. A missing report and a report outside the caller's domain scope both return 404.", Tags: []string{"Admin: Domain mail policy"}, Errors: []int{http.StatusNotFound}, Middleware: checks}, h.get)
}

type listDMARCReportsInput struct {
	DomainID *uint32 `query:"domain_id" minimum:"0"`
	Reporter string  `query:"reporter" maxLength:"255" description:"Case-insensitive substring of the authenticated reporter domain; SQL wildcard characters are literal."`
	Limit    *int    `query:"limit" maximum:"200" message:"exceeds the maximum page size"`
	Offset   *int    `query:"offset"`
}

func (h *DMARCReportHandler) query(r *http.Request) *gorm.DB {
	return filterAdminList(h.db.WithContext(r.Context()).Model(&models.DMARCReportReceived{}), r, middleware.CapDomainsRead, "dmarc_reports_received")
}

func (h *DMARCReportHandler) list(r *http.Request, in listDMARCReportsInput) (endpoint.List[DMARCReport], error) {
	if err := dmarcReportReadAllowed(r); err != nil {
		return endpoint.List[DMARCReport]{}, err
	}
	limit, offset := pagination(in.Limit, in.Offset, 50, 200)
	query := h.query(r)
	if in.DomainID != nil {
		query = query.Where("domain_id = ?", *in.DomainID)
	}
	if in.Reporter != "" {
		query = query.Where("reporter ILIKE ? ESCAPE '\\'", "%"+escapeLike(in.Reporter)+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return endpoint.List[DMARCReport]{}, err
	}
	var rows []models.DMARCReportReceived
	if err := query.Order("received_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return endpoint.List[DMARCReport]{}, err
	}
	data := make([]DMARCReport, len(rows))
	for index, row := range rows {
		data[index] = dmarcReport(row)
	}
	return endpoint.List[DMARCReport]{Data: data, Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, nil
}

type dmarcReportPath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}

func (h *DMARCReportHandler) get(r *http.Request, in dmarcReportPath) (DMARCReport, error) {
	if err := dmarcReportReadAllowed(r); err != nil {
		return DMARCReport{}, err
	}
	var row models.DMARCReportReceived
	err := h.query(r).First(&row, in.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DMARCReport{}, endpoint.Refuse(http.StatusNotFound, "dmarc_report_not_found", "DMARC report not found")
	}
	return dmarcReport(row), err
}

func dmarcReport(row models.DMARCReportReceived) DMARCReport {
	row.StartDate = row.StartDate.UTC()
	row.EndDate = row.EndDate.UTC()
	row.ReceivedAt = row.ReceivedAt.UTC()
	return DMARCReport(row)
}

func dmarcReportReadAllowed(r *http.Request) error {
	if scopes, restricted := middleware.CapabilityScopes(r, middleware.CapDomainsRead); restricted {
		for _, scope := range scopes {
			if scope == grants.All || strings.HasPrefix(scope, "domain:") {
				return nil
			}
		}
		return endpoint.Refuse(http.StatusForbidden, "forbidden", "Insufficient permissions")
	}
	return nil
}
