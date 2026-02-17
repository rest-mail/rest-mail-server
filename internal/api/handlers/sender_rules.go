package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// SenderRuleHandler manages domain-level allow/block lists for email sender filtering.
type SenderRuleHandler struct {
	db *gorm.DB
}

// NewSenderRuleHandler creates a new SenderRuleHandler.
func NewSenderRuleHandler(db *gorm.DB) *SenderRuleHandler {
	return &SenderRuleHandler{db: db}
}

// ── Allowlist ────────────────────────────────────────────────────────

// ListAllowlist returns all sender rules where list_type="allow" for a domain.
// GET /api/v1/admin/domains/{id}/allowlist
func (h *SenderRuleHandler) ListAllowlist(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var rules []models.DomainSenderRule
	if err := h.db.Where("domain_id = ? AND list_type = ?", domainID, "allow").
		Order("created_at DESC").
		Find(&rules).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list allowlist rules")
		return
	}

	respond.List(w, rules, nil)
}

type addSenderRuleRequest struct {
	Pattern string `json:"pattern"`
	Reason  string `json:"reason"`
}

// AddToAllowlist creates a new sender rule with list_type="allow" for a domain.
// POST /api/v1/admin/domains/{id}/allowlist
func (h *SenderRuleHandler) AddToAllowlist(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var req addSenderRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Pattern == "" {
		respond.ValidationError(w, map[string]string{"pattern": "required"})
		return
	}

	// Verify the domain exists.
	var domain models.Domain
	if err := h.db.First(&domain, domainID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found")
		return
	}

	rule := models.DomainSenderRule{
		DomainID: uint(domainID),
		Pattern:  req.Pattern,
		ListType: "allow",
		Reason:   req.Reason,
	}

	if err := h.db.Create(&rule).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create allowlist rule")
		return
	}

	respond.Data(w, http.StatusCreated, rule)
}

// RemoveFromAllowlist deletes an allowlist rule for a domain.
// DELETE /api/v1/admin/domains/{id}/allowlist/{eid}
func (h *SenderRuleHandler) RemoveFromAllowlist(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	ruleID, err := strconv.ParseUint(chi.URLParam(r, "eid"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid rule ID")
		return
	}

	var rule models.DomainSenderRule
	if err := h.db.Where("id = ? AND domain_id = ? AND list_type = ?", ruleID, domainID, "allow").
		First(&rule).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Allowlist rule not found")
		return
	}

	if err := h.db.Delete(&rule).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete allowlist rule")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Blocklist ────────────────────────────────────────────────────────

// ListBlocklist returns all sender rules where list_type="block" for a domain.
// GET /api/v1/admin/domains/{id}/blocklist
func (h *SenderRuleHandler) ListBlocklist(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var rules []models.DomainSenderRule
	if err := h.db.Where("domain_id = ? AND list_type = ?", domainID, "block").
		Order("created_at DESC").
		Find(&rules).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list blocklist rules")
		return
	}

	respond.List(w, rules, nil)
}

// AddToBlocklist creates a new sender rule with list_type="block" for a domain.
// POST /api/v1/admin/domains/{id}/blocklist
func (h *SenderRuleHandler) AddToBlocklist(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var req addSenderRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Pattern == "" {
		respond.ValidationError(w, map[string]string{"pattern": "required"})
		return
	}

	// Verify the domain exists.
	var domain models.Domain
	if err := h.db.First(&domain, domainID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found")
		return
	}

	rule := models.DomainSenderRule{
		DomainID: uint(domainID),
		Pattern:  req.Pattern,
		ListType: "block",
		Reason:   req.Reason,
	}

	if err := h.db.Create(&rule).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create blocklist rule")
		return
	}

	respond.Data(w, http.StatusCreated, rule)
}

// RemoveFromBlocklist deletes a blocklist rule for a domain.
// DELETE /api/v1/admin/domains/{id}/blocklist/{eid}
func (h *SenderRuleHandler) RemoveFromBlocklist(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	ruleID, err := strconv.ParseUint(chi.URLParam(r, "eid"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid rule ID")
		return
	}

	var rule models.DomainSenderRule
	if err := h.db.Where("id = ? AND domain_id = ? AND list_type = ?", ruleID, domainID, "block").
		First(&rule).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Blocklist rule not found")
		return
	}

	if err := h.db.Delete(&rule).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete blocklist rule")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
