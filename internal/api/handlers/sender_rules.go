package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
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

// listAllowlist returns all sender rules where list_type="allow" for a domain.
// GET /api/v1/admin/domains/{id}/allowlist
func (h *SenderRuleHandler) listAllowlist(_ *http.Request, in domainPath) (endpoint.List[SenderRule], error) {
	domainID := in.ID

	var rules []models.DomainSenderRule
	if err := h.db.Where("domain_id = ? AND list_type = ?", domainID, "allow").
		Order("created_at DESC").
		Find(&rules).Error; err != nil {
		return endpoint.List[SenderRule]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list allowlist rules")
	}

	data := make([]SenderRule, len(rules))
	for i, row := range rules {
		data[i] = senderRuleOf(row)
	}
	return endpoint.List[SenderRule]{Data: data}, nil
}

type addSenderRuleRequest struct {
	Pattern string `json:"pattern" minLength:"1" message:"required"`
	Reason  string `json:"reason,omitempty"`
}

// addToAllowlist creates a new sender rule with list_type="allow" for a domain.
// POST /api/v1/admin/domains/{id}/allowlist
func (h *SenderRuleHandler) addToAllowlist(_ *http.Request, in addSenderRuleInput) (SenderRule, error) {
	domainID := in.ID

	req := in.Body

	// Verify the domain exists.
	var domain models.Domain
	if err := h.db.First(&domain, domainID).Error; err != nil {
		return SenderRule{}, endpoint.Refuse(http.StatusNotFound, "domain_not_found", "Domain not found")
	}

	rule := models.DomainSenderRule{
		DomainID: uint(domainID),
		Pattern:  req.Pattern,
		ListType: "allow",
		Reason:   req.Reason,
	}

	if err := h.db.Create(&rule).Error; err != nil {
		if repositories.IsUniqueViolation(err) {
			return SenderRule{}, endpoint.Refuse(http.StatusConflict, "already_exists", "Sender is already on the allowlist")
		}
		return SenderRule{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to create allowlist rule")
	}

	return senderRuleOf(rule), nil
}

// removeFromAllowlist deletes an allowlist rule for a domain.
// DELETE /api/v1/admin/domains/{id}/allowlist/{eid}
func (h *SenderRuleHandler) removeFromAllowlist(_ *http.Request, in senderRulePath) (endpoint.Empty, error) {
	domainID := in.ID

	ruleID := in.EID

	var rule models.DomainSenderRule
	if err := h.db.Where("id = ? AND domain_id = ? AND list_type = ?", ruleID, domainID, "allow").
		First(&rule).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Allowlist rule not found")
		}
		return endpoint.Empty{}, fmt.Errorf("look up allowlist rule for deletion: %w", err)
	}

	if err := h.db.Delete(&rule).Error; err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to delete allowlist rule")
	}

	return endpoint.Empty{}, nil
}

// ── Blocklist ────────────────────────────────────────────────────────

// listBlocklist returns all sender rules where list_type="block" for a domain.
// GET /api/v1/admin/domains/{id}/blocklist
func (h *SenderRuleHandler) listBlocklist(_ *http.Request, in domainPath) (endpoint.List[SenderRule], error) {
	domainID := in.ID

	var rules []models.DomainSenderRule
	if err := h.db.Where("domain_id = ? AND list_type = ?", domainID, "block").
		Order("created_at DESC").
		Find(&rules).Error; err != nil {
		return endpoint.List[SenderRule]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list blocklist rules")
	}

	data := make([]SenderRule, len(rules))
	for i, row := range rules {
		data[i] = senderRuleOf(row)
	}
	return endpoint.List[SenderRule]{Data: data}, nil
}

// addToBlocklist creates a new sender rule with list_type="block" for a domain.
// POST /api/v1/admin/domains/{id}/blocklist
func (h *SenderRuleHandler) addToBlocklist(_ *http.Request, in addSenderRuleInput) (SenderRule, error) {
	domainID := in.ID

	req := in.Body

	// Verify the domain exists.
	var domain models.Domain
	if err := h.db.First(&domain, domainID).Error; err != nil {
		return SenderRule{}, endpoint.Refuse(http.StatusNotFound, "domain_not_found", "Domain not found")
	}

	rule := models.DomainSenderRule{
		DomainID: uint(domainID),
		Pattern:  req.Pattern,
		ListType: "block",
		Reason:   req.Reason,
	}

	if err := h.db.Create(&rule).Error; err != nil {
		if repositories.IsUniqueViolation(err) {
			return SenderRule{}, endpoint.Refuse(http.StatusConflict, "already_exists", "Sender is already on the blocklist")
		}
		return SenderRule{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to create blocklist rule")
	}

	return senderRuleOf(rule), nil
}

// removeFromBlocklist deletes a blocklist rule for a domain.
// DELETE /api/v1/admin/domains/{id}/blocklist/{eid}
func (h *SenderRuleHandler) removeFromBlocklist(_ *http.Request, in senderRulePath) (endpoint.Empty, error) {
	domainID := in.ID

	ruleID := in.EID

	var rule models.DomainSenderRule
	if err := h.db.Where("id = ? AND domain_id = ? AND list_type = ?", ruleID, domainID, "block").
		First(&rule).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Blocklist rule not found")
		}
		return endpoint.Empty{}, fmt.Errorf("look up blocklist rule for deletion: %w", err)
	}

	if err := h.db.Delete(&rule).Error; err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to delete blocklist rule")
	}

	return endpoint.Empty{}, nil
}
