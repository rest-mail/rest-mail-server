package handlers

import (
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
)

// AdminLogRouteChecks keeps audit recording before the capability check, including refusals.
type AdminLogRouteChecks struct {
	At        func(string) endpoint.Middleware
	AuditRead endpoint.Middleware
}

func (h *BanHandler) Register(rs endpoint.Routes, at func(string) endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/bans",
		OperationID: "listBans", Summary: "List IP bans", Tags: []string{"Admin: Bans"},
		Middleware: []endpoint.Middleware{at(middleware.CapBansRead)}}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/bans",
		OperationID: "createBan", Summary: "Create an IP ban", Tags: []string{"Admin: Bans"},
		Status: http.StatusCreated, SuccessStatuses: []int{http.StatusOK},
		Middleware: []endpoint.Middleware{at(middleware.CapBansWrite)}}, h.create)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/bans/{id}",
		OperationID: "deleteBan", Summary: "Delete a ban by ID", Tags: []string{"Admin: Bans"},
		Status:     http.StatusNoContent,
		Middleware: []endpoint.Middleware{at(middleware.CapBansDelete)}}, h.delete)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/bans/ip/{ip}",
		OperationID: "unbanIP", Summary: "Remove all bans for a specific IP", Tags: []string{"Admin: Bans"},
		Status: http.StatusNoContent, Errors: []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{at(middleware.CapBansDelete)}}, h.unbanIP)
}

func (h *LogHandler) Register(rs endpoint.Routes, at func(string) endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/logs/delivery",
		OperationID: "getDeliveryLogs", Summary: "Query delivery log (outbound queue history)", Tags: []string{"Admin: Logs"},
		Middleware: []endpoint.Middleware{at(middleware.CapMessagesRead)}}, h.deliveryLog)
}

func (h *AuditHandler) Register(rs endpoint.Routes, checks AdminLogRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/audit",
		OperationID: "listAuditEntries", Summary: "Read the audit log, newest first", Tags: []string{"Admin: Audit"},
		Middleware: []endpoint.Middleware{checks.AuditRead, checks.At(middleware.CapAuditRead)}}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/audit/verify",
		OperationID: "verifyAuditLog", Summary: "Check the audit log's hash chain", Tags: []string{"Admin: Audit"},
		Middleware: []endpoint.Middleware{checks.AuditRead, checks.At(middleware.CapAuditRead)}}, h.verify)
}

func (h *StatsHandler) Register(rs endpoint.Routes, at func(string) endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/stats",
		OperationID: "getDashboardStats", Summary: "Dashboard statistics", Tags: []string{"Admin: Stats"},
		Middleware: []endpoint.Middleware{at(middleware.CapDomainsRead)}}, h.dashboardStats)
}
