package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// MTASTSHandler manages MTA-STS policies via admin API and serves
// the well-known policy file for external MTA consumption.
type MTASTSHandler struct {
	db *gorm.DB
}

func NewMTASTSHandler(db *gorm.DB) *MTASTSHandler {
	return &MTASTSHandler{db: db}
}

// GetPolicy returns the MTA-STS policy for a domain.
// GET /api/v1/admin/domains/{id}/mta-sts
func (h *MTASTSHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	// Verify domain exists
	var domain models.Domain
	if err := h.db.First(&domain, domainID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Domain not found")
		return
	}

	var policy models.MTASTSPolicy
	if err := h.db.Where("domain_id = ?", domainID).First(&policy).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "No MTA-STS policy found for this domain")
		return
	}

	respond.Data(w, http.StatusOK, policy)
}

// SetPolicy creates or updates the MTA-STS policy for a domain.
// PUT /api/v1/admin/domains/{id}/mta-sts
func (h *MTASTSHandler) SetPolicy(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	// Verify domain exists
	var domain models.Domain
	if err := h.db.First(&domain, domainID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Domain not found")
		return
	}

	var req struct {
		Mode    string `json:"mode"`
		MXHosts string `json:"mx_hosts"`
		MaxAge  *int   `json:"max_age"`
		Active  *bool  `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Validate mode
	if req.Mode != "" {
		switch req.Mode {
		case "none", "testing", "enforce":
			// valid
		default:
			respond.ValidationError(w, map[string]string{
				"mode": "Must be one of: none, testing, enforce",
			})
			return
		}
	}

	// Validate max_age
	if req.MaxAge != nil && *req.MaxAge < 0 {
		respond.ValidationError(w, map[string]string{
			"max_age": "Must be a non-negative integer (seconds)",
		})
		return
	}

	// Upsert: find existing or create new
	var policy models.MTASTSPolicy
	result := h.db.Where("domain_id = ?", domainID).First(&policy)

	if result.Error != nil {
		// Create new policy
		policy = models.MTASTSPolicy{
			DomainID: uint(domainID),
			Mode:     "testing",
			MaxAge:   604800,
			Active:   true,
		}
	}

	if req.Mode != "" {
		policy.Mode = req.Mode
	}
	if req.MXHosts != "" {
		policy.MXHosts = req.MXHosts
	}
	if req.MaxAge != nil {
		policy.MaxAge = *req.MaxAge
	}
	if req.Active != nil {
		policy.Active = *req.Active
	}

	if err := h.db.Save(&policy).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save MTA-STS policy")
		return
	}

	status := http.StatusOK
	if result.Error != nil {
		status = http.StatusCreated
	}
	respond.Data(w, status, policy)
}

// DeletePolicy removes the MTA-STS policy for a domain.
// DELETE /api/v1/admin/domains/{id}/mta-sts
func (h *MTASTSHandler) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	result := h.db.Where("domain_id = ?", domainID).Delete(&models.MTASTSPolicy{})
	if result.RowsAffected == 0 {
		respond.Error(w, http.StatusNotFound, "not_found", "No MTA-STS policy found for this domain")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ServePolicy serves the MTA-STS policy file at /.well-known/mta-sts.txt.
// It extracts the domain from the Host header and returns the appropriate
// text/plain policy file per RFC 8461.
// GET /.well-known/mta-sts.txt
func (h *MTASTSHandler) ServePolicy(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	// Strip port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	// MTA-STS policies are served from mta-sts.<domain>, so strip that prefix
	domainName := host
	if strings.HasPrefix(host, "mta-sts.") {
		domainName = strings.TrimPrefix(host, "mta-sts.")
	}

	// Look up the domain
	var domain models.Domain
	if err := h.db.Where("name = ?", domainName).First(&domain).Error; err != nil {
		http.Error(w, "domain not found", http.StatusNotFound)
		return
	}

	// Look up the MTA-STS policy
	var policy models.MTASTSPolicy
	if err := h.db.Where("domain_id = ? AND active = ?", domain.ID, true).First(&policy).Error; err != nil {
		http.Error(w, "no MTA-STS policy configured", http.StatusNotFound)
		return
	}

	// Build the policy text
	var sb strings.Builder
	sb.WriteString("version: STSv1\n")
	sb.WriteString(fmt.Sprintf("mode: %s\n", policy.Mode))

	// Write each MX host on its own line
	if policy.MXHosts != "" {
		for _, mx := range strings.Split(policy.MXHosts, ",") {
			mx = strings.TrimSpace(mx)
			if mx != "" {
				sb.WriteString(fmt.Sprintf("mx: %s\n", mx))
			}
		}
	}

	sb.WriteString(fmt.Sprintf("max_age: %d\n", policy.MaxAge))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(sb.String()))
}
