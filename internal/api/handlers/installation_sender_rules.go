package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"gorm.io/gorm"
)

// InstallationSenderRule is the API representation of a sender rule every domain
// inherits (#1576), independent of the database model.
type InstallationSenderRule struct {
	ID        uint      `json:"id"`
	Pattern   string    `json:"pattern"`                       // "spam@evil.com" or "@evil.com"
	ListType  string    `json:"list_type" enums:"allow,block"` // "allow" or "block"
	Reason    string    `json:"reason,omitempty"`
	CreatedBy *uint     `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func installationSenderRuleOf(row models.InstallationSenderRule) InstallationSenderRule {
	return InstallationSenderRule{
		ID:        row.ID,
		Pattern:   row.Pattern,
		ListType:  row.ListType,
		Reason:    row.Reason,
		CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt,
	}
}

type addInstallationSenderRuleRequest struct {
	Pattern  string `json:"pattern" minLength:"1" message:"required"`
	ListType string `json:"list_type" enums:"allow,block" message:"Must be one of: allow, block" missing:"required"`
	Reason   string `json:"reason,omitempty"`
}

type installationSenderRulePath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}

// listInstallationSenderRules returns every installation sender rule, newest first.
// GET /api/v1/admin/sender-rules
func (h *SenderRuleHandler) listInstallationSenderRules(r *http.Request, _ struct{}) (endpoint.List[InstallationSenderRule], error) {
	var rules []models.InstallationSenderRule
	if err := h.db.WithContext(r.Context()).Order("created_at DESC, id DESC").Find(&rules).Error; err != nil {
		return endpoint.List[InstallationSenderRule]{}, fmt.Errorf("list installation sender rules: %w", err)
	}
	data := make([]InstallationSenderRule, len(rules))
	for i, row := range rules {
		data[i] = installationSenderRuleOf(row)
	}
	return endpoint.List[InstallationSenderRule]{Data: data}, nil
}

// addInstallationSenderRule creates an allow or block rule every domain inherits.
// POST /api/v1/admin/sender-rules
func (h *SenderRuleHandler) addInstallationSenderRule(r *http.Request, req addInstallationSenderRuleRequest) (InstallationSenderRule, error) {
	rule := models.InstallationSenderRule{
		Pattern:  req.Pattern,
		ListType: req.ListType,
		Reason:   req.Reason,
	}
	if claims := middleware.GetClaims(r); claims != nil && claims.AccountID != 0 {
		id := claims.AccountID
		rule.CreatedBy = &id
	}
	if err := h.db.WithContext(r.Context()).Create(&rule).Error; err != nil {
		if repositories.IsUniqueViolation(err) {
			return InstallationSenderRule{}, endpoint.Refuse(http.StatusConflict, "already_exists", "The installation already has this sender rule")
		}
		return InstallationSenderRule{}, fmt.Errorf("create installation sender rule: %w", err)
	}
	return installationSenderRuleOf(rule), nil
}

// removeInstallationSenderRule deletes an installation sender rule.
// DELETE /api/v1/admin/sender-rules/{id}
func (h *SenderRuleHandler) removeInstallationSenderRule(r *http.Request, in installationSenderRulePath) (endpoint.Empty, error) {
	result := h.db.WithContext(r.Context()).Delete(&models.InstallationSenderRule{}, in.ID)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Sender rule not found")
		}
		return endpoint.Empty{}, fmt.Errorf("delete installation sender rule: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Sender rule not found")
	}
	return endpoint.Empty{}, nil
}
