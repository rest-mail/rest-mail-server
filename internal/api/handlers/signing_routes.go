package handlers

import (
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
)

// DKIMRouteChecks retain global list access and domain-scoped key management.
type DKIMRouteChecks struct {
	At       func(string) endpoint.Middleware
	OnDomain func(string) endpoint.Middleware
}

func (h *DKIMHandler) Register(rs endpoint.Routes, checks DKIMRouteChecks) {
	tags := []string{"Admin: DKIM"}
	missing := []int{http.StatusNotFound}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/dkim",
		OperationID: "listDKIMKeys", Summary: "List DKIM key entries for all domains", Tags: tags,
		Middleware: []endpoint.Middleware{checks.At(middleware.CapDomainsRead)}}, h.listKeys)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/dkim/{id}/rotation",
		OperationID: "getDKIMRotation", Summary: "Read a domain's DKIM rotation interval", Tags: tags, Errors: missing,
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.getRotation)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/admin/dkim/{id}/rotation",
		OperationID: "setDKIMRotation", Summary: "Set a domain's DKIM rotation interval", Tags: tags, Errors: missing,
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.setRotation)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/admin/dkim/{id}",
		OperationID: "setDKIMKey", Summary: "Set or replace DKIM key for a domain", Tags: tags, Errors: missing,
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.setKey)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/dkim/{id}",
		OperationID: "deleteDKIMKey", Summary: "Delete DKIM key for a domain", Tags: tags, Errors: missing, Status: http.StatusNoContent,
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.deleteKey)
}

type CertificateRouteChecks struct {
	At func(string) endpoint.Middleware
}

func (h *CertificateHandler) Register(rs endpoint.Routes, checks CertificateRouteChecks) {
	tags := []string{"Admin: Certificates"}
	missing := []int{http.StatusNotFound}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/certificates",
		OperationID: "listCertificates", Summary: "List TLS certificates", Tags: tags,
		Middleware: []endpoint.Middleware{checks.At(middleware.CapDomainsRead)}}, h.listCertificates)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/certificates/{id}",
		OperationID: "getCertificate", Summary: "Get certificate details (includes cert_pem)", Tags: tags, Errors: missing,
		Middleware: []endpoint.Middleware{checks.At(middleware.CapDomainsRead)}}, h.getCertificate)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/certificates",
		OperationID: "createCertificate", Summary: "Upload a new TLS certificate", Tags: tags, Errors: missing, Status: http.StatusCreated,
		Middleware: []endpoint.Middleware{checks.At(middleware.CapDomainsWrite)}}, h.createCertificate)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/certificates/{id}",
		OperationID: "deleteCertificate", Summary: "Delete a certificate", Tags: tags, Errors: missing, Status: http.StatusNoContent,
		Middleware: []endpoint.Middleware{checks.At(middleware.CapDomainsWrite)}}, h.deleteCertificate)
}
