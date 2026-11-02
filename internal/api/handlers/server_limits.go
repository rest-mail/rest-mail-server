package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

const (
	// ActionServerLimitsUpdate audits a change to the server's limits.
	ActionServerLimitsUpdate = "server_limits.update"
	// defaultContactImportMaxBytes is the largest contact import before an administrator
	// changes it: far more people than anybody knows.
	defaultContactImportMaxBytes int64 = 10 << 20
	serverLimitsRowID            uint  = 1
)

// serverLimitsResponse is what an administrator can set for the whole server.
type serverLimitsResponse struct {
	ContactImportMaxBytes int64 `json:"contact_import_max_bytes" minimum:"1" example:"10485760"`
}

type updateServerLimitsRequest struct {
	ContactImportMaxBytes int64 `json:"contact_import_max_bytes" minimum:"1" message:"must be at least 1" missing:"required"`
}

// contactImportMaxBytes is the limit in force.
func contactImportMaxBytes(db *gorm.DB) (int64, error) {
	var limits models.ServerLimits
	err := db.Take(&limits, serverLimitsRowID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultContactImportMaxBytes, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read the server limits: %w", err)
	}
	return limits.ContactImportMaxBytes, nil
}

func serverLimitsError(err error) error {
	if errors.Is(err, grants.ErrSuperadminRequired) {
		return endpoint.Refuse(http.StatusForbidden, "superadmin_required", "only a superadmin can read or change the server's limits")
	}
	return err
}

// serverLimits reads the limits in force; superadmin only.
// GET /api/v1/admin/server-limits
func (h *GrantHandler) serverLimits(r *http.Request, _ struct{}) (serverLimitsResponse, error) {
	h = h.forRequest(r)
	if err := grants.RequireSuperadmin(h.db, middleware.GetClaims(r).AccountID); err != nil {
		return serverLimitsResponse{}, serverLimitsError(err)
	}
	limit, err := contactImportMaxBytes(h.db)
	if err != nil {
		return serverLimitsResponse{}, err
	}
	return serverLimitsResponse{ContactImportMaxBytes: limit}, nil
}

// updateServerLimits changes the limits; superadmin only. The change and its audit entry
// commit together.
// PUT /api/v1/admin/server-limits {"contact_import_max_bytes":20971520}
func (h *GrantHandler) updateServerLimits(r *http.Request, req updateServerLimitsRequest) (serverLimitsResponse, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.RequireSuperadmin(tx, claims.AccountID); err != nil {
			return err
		}
		previous, err := contactImportMaxBytes(tx)
		if err != nil {
			return err
		}
		if err := tx.Save(&models.ServerLimits{ID: serverLimitsRowID, ContactImportMaxBytes: req.ContactImportMaxBytes}).Error; err != nil {
			return err
		}
		e := middleware.NewAuditEntry(ActionServerLimitsUpdate, r, time.Now())
		e.Target["previous_contact_import_max_bytes"] = strconv.FormatInt(previous, 10)
		e.Target["contact_import_max_bytes"] = strconv.FormatInt(req.ContactImportMaxBytes, 10)
		return audit.Append(tx, e)
	})
	if err != nil {
		return serverLimitsResponse{}, serverLimitsError(err)
	}
	return serverLimitsResponse{ContactImportMaxBytes: req.ContactImportMaxBytes}, nil
}
