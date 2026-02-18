package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dns"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type DomainHandler struct {
	db  *gorm.DB
	dns dns.Provider
}

func NewDomainHandler(db *gorm.DB, dnsProvider dns.Provider) *DomainHandler {
	return &DomainHandler{db: db, dns: dnsProvider}
}

func (h *DomainHandler) List(w http.ResponseWriter, r *http.Request) {
	var domains []models.Domain
	if err := h.db.Order("name ASC").Find(&domains).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list domains")
		return
	}
	respond.List(w, domains, nil)
}

type createDomainRequest struct {
	Name              string `json:"name"`
	ServerType        string `json:"server_type"`
	DefaultQuotaBytes *int64 `json:"default_quota_bytes"`
}

func (h *DomainHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Name == "" {
		respond.ValidationError(w, map[string]string{"name": "required"})
		return
	}

	if req.ServerType == "" {
		req.ServerType = "traditional"
	}
	if req.ServerType != "traditional" && req.ServerType != "restmail" {
		respond.ValidationError(w, map[string]string{"server_type": "must be 'traditional' or 'restmail'"})
		return
	}

	domain := models.Domain{
		Name:       req.Name,
		ServerType: req.ServerType,
		Active:     true,
	}
	if req.DefaultQuotaBytes != nil {
		domain.DefaultQuotaBytes = *req.DefaultQuotaBytes
	}

	if err := h.db.Create(&domain).Error; err != nil {
		respond.Error(w, http.StatusConflict, "already_exists", "Domain already exists")
		return
	}

	// Create default inbound and outbound pipelines for the new domain.
	inboundJSON := pipeline.DefaultPipelineJSON("inbound")
	outboundJSON := pipeline.DefaultPipelineJSON("outbound")

	if err := h.db.Create(&models.Pipeline{
		DomainID:  domain.ID,
		Direction: "inbound",
		Filters:   inboundJSON,
		Active:    true,
	}).Error; err != nil {
		slog.Error("failed to create default inbound pipeline", "domain", domain.Name, "error", err)
	}

	if err := h.db.Create(&models.Pipeline{
		DomainID:  domain.ID,
		Direction: "outbound",
		Filters:   outboundJSON,
		Active:    true,
	}).Error; err != nil {
		slog.Error("failed to create default outbound pipeline", "domain", domain.Name, "error", err)
	}

	respond.Data(w, http.StatusCreated, domain)
}

func (h *DomainHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found")
		return
	}

	respond.Data(w, http.StatusOK, domain)
}

type updateDomainRequest struct {
	Active            *bool  `json:"active"`
	DefaultQuotaBytes *int64 `json:"default_quota_bytes"`
}

func (h *DomainHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found")
		return
	}

	var req updateDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.Active != nil {
		updates["active"] = *req.Active
	}
	if req.DefaultQuotaBytes != nil {
		updates["default_quota_bytes"] = *req.DefaultQuotaBytes
	}

	if len(updates) > 0 {
		if err := h.db.Model(&domain).Updates(updates).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update domain")
			return
		}
	}

	h.db.First(&domain, id)
	respond.Data(w, http.StatusOK, domain)
}

func (h *DomainHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found")
		return
	}

	if err := h.db.Delete(&domain).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete domain")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DNSCheck verifies DNS records for a domain (MX, SPF, DKIM, DMARC, MTA-STS).
// GET /api/v1/admin/domains/:id/dns
func (h *DomainHandler) DNSCheck(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found")
		return
	}

	type recordCheck struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Status  string `json:"status"` // "ok", "missing", "error"
		Value   string `json:"value,omitempty"`
		Message string `json:"message,omitempty"`
	}

	var checks []recordCheck

	// MX records
	mxs, err := net.LookupMX(domain.Name)
	if err != nil {
		checks = append(checks, recordCheck{
			Type: "MX", Name: domain.Name, Status: "missing",
			Message: fmt.Sprintf("MX lookup failed: %v", err),
		})
	} else if len(mxs) == 0 {
		checks = append(checks, recordCheck{
			Type: "MX", Name: domain.Name, Status: "missing",
			Message: "No MX records found",
		})
	} else {
		hosts := make([]string, len(mxs))
		for i, mx := range mxs {
			hosts[i] = fmt.Sprintf("%s (pri %d)", mx.Host, mx.Pref)
		}
		checks = append(checks, recordCheck{
			Type: "MX", Name: domain.Name, Status: "ok",
			Value: strings.Join(hosts, ", "),
		})
	}

	// SPF (TXT record)
	spfFound := false
	txts, err := net.LookupTXT(domain.Name)
	if err == nil {
		for _, txt := range txts {
			if strings.HasPrefix(txt, "v=spf1") {
				spfFound = true
				checks = append(checks, recordCheck{
					Type: "SPF", Name: domain.Name, Status: "ok",
					Value: txt,
				})
				break
			}
		}
	}
	if !spfFound {
		checks = append(checks, recordCheck{
			Type: "SPF", Name: domain.Name, Status: "missing",
			Message: "No SPF record (v=spf1...) found in TXT records",
		})
	}

	// DMARC
	dmarcFound := false
	dmarcTxts, err := net.LookupTXT("_dmarc." + domain.Name)
	if err == nil {
		for _, txt := range dmarcTxts {
			if strings.HasPrefix(txt, "v=DMARC1") {
				dmarcFound = true
				checks = append(checks, recordCheck{
					Type: "DMARC", Name: "_dmarc." + domain.Name, Status: "ok",
					Value: txt,
				})
				break
			}
		}
	}
	if !dmarcFound {
		checks = append(checks, recordCheck{
			Type: "DMARC", Name: "_dmarc." + domain.Name, Status: "missing",
			Message: "No DMARC record found",
		})
	}

	// DKIM (check for default selector)
	dkimName := "default._domainkey." + domain.Name
	dkimTxts, err := net.LookupTXT(dkimName)
	if err != nil || len(dkimTxts) == 0 {
		checks = append(checks, recordCheck{
			Type: "DKIM", Name: dkimName, Status: "missing",
			Message: "No DKIM record found for default selector",
		})
	} else {
		checks = append(checks, recordCheck{
			Type: "DKIM", Name: dkimName, Status: "ok",
			Value: dkimTxts[0],
		})
	}

	// MTA-STS
	mtaSTSName := "_mta-sts." + domain.Name
	mtaSTSTxts, err := net.LookupTXT(mtaSTSName)
	if err != nil || len(mtaSTSTxts) == 0 {
		checks = append(checks, recordCheck{
			Type: "MTA-STS", Name: mtaSTSName, Status: "missing",
			Message: "No MTA-STS TXT record found",
		})
	} else {
		found := false
		for _, txt := range mtaSTSTxts {
			if strings.HasPrefix(txt, "v=STSv1") {
				found = true
				checks = append(checks, recordCheck{
					Type: "MTA-STS", Name: mtaSTSName, Status: "ok",
					Value: txt,
				})
				break
			}
		}
		if !found {
			checks = append(checks, recordCheck{
				Type: "MTA-STS", Name: mtaSTSName, Status: "missing",
				Message: "TXT records exist but none start with v=STSv1",
			})
		}
	}

	// TLS-RPT
	tlsRPTName := "_smtp._tls." + domain.Name
	tlsRPTTxts, err := net.LookupTXT(tlsRPTName)
	if err != nil || len(tlsRPTTxts) == 0 {
		checks = append(checks, recordCheck{
			Type: "TLS-RPT", Name: tlsRPTName, Status: "missing",
			Message: "No TLS-RPT TXT record found",
		})
	} else {
		found := false
		for _, txt := range tlsRPTTxts {
			if strings.HasPrefix(txt, "v=TLSRPTv1") {
				found = true
				checks = append(checks, recordCheck{
					Type: "TLS-RPT", Name: tlsRPTName, Status: "ok",
					Value: txt,
				})
				break
			}
		}
		if !found {
			checks = append(checks, recordCheck{
				Type: "TLS-RPT", Name: tlsRPTName, Status: "missing",
				Message: "TXT records exist but none start with v=TLSRPTv1",
			})
		}
	}

	// Count results
	okCount := 0
	for _, c := range checks {
		if c.Status == "ok" {
			okCount++
		}
	}

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"domain":  domain.Name,
		"records": checks,
		"summary": fmt.Sprintf("%d/%d records verified", okCount, len(checks)),
	})
}
