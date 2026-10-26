package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// BanHandler manages IP bans via admin API.
type BanHandler struct {
	db *gorm.DB
}

func NewBanHandler(db *gorm.DB) *BanHandler {
	return &BanHandler{db: db}
}

// listBansRequest filters active bans by default.
// GET /api/v1/admin/bans?protocol=smtp&active=true&limit=50&offset=0
type listBansRequest struct {
	Limit    *int    `query:"limit"`
	Offset   *int    `query:"offset"`
	Protocol *string `query:"protocol" enums:"smtp,imap,pop3,all" message:"protocol must be one of smtp, imap, pop3, all"`
	Active   *string `query:"active" enums:"true,false" message:"must be true or false"`
	IP       string  `query:"ip"`
}

func (h *BanHandler) list(_ *http.Request, in listBansRequest) (endpoint.List[Ban], error) {
	// Preserve the ban list's default for limits outside 1..200.
	limit, offset := 50, 0
	if in.Limit != nil && *in.Limit > 0 && *in.Limit <= 200 {
		limit = *in.Limit
	}
	if in.Offset != nil && *in.Offset >= 0 {
		offset = *in.Offset
	}

	query := h.db.Model(&models.Ban{})

	if in.Protocol != nil {
		protocol := *in.Protocol
		query = query.Where("protocol = ? OR protocol = 'all'", protocol)
	}
	if ip := in.IP; ip != "" {
		query = query.Where("ip = ?", ip)
	}
	// Default: only show active (non-expired) bans
	if in.Active == nil || *in.Active != "false" {
		query = query.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	}

	var total int64
	query.Count(&total)

	var bans []models.Ban
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&bans).Error; err != nil {
		return endpoint.List[Ban]{}, fmt.Errorf("list bans: %w", err)
	}

	out := make([]Ban, len(bans))
	for i, row := range bans {
		out[i] = banOf(row)
	}
	return endpoint.List[Ban]{Data: out, Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, nil
}

// createBanRequest adds or replaces an IP ban.
// POST /api/v1/admin/bans
type createBanRequest struct {
	IP        string  `json:"ip" minLength:"1" message:"IP address is required" missing:"IP address is required"`
	Reason    string  `json:"reason,omitempty"`
	Protocol  string  `json:"protocol" enums:"smtp,imap,pop3,all" message:"protocol must be one of smtp, imap, pop3, all" missing:"protocol must be one of smtp, imap, pop3, all"`
	Duration  *string `json:"duration,omitempty" pattern:"^[+-]?(0|(([0-9]+(\\.[0-9]*)?|\\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$" message:"invalid duration format (e.g. 24h, 168h)"`
	CreatedBy string  `json:"created_by" message:"required"`
}

func (h *BanHandler) create(r *http.Request, req createBanRequest) (endpoint.Status[Ban], error) {
	ban := models.Ban{
		IP:        req.IP,
		Reason:    req.Reason,
		Protocol:  req.Protocol,
		CreatedBy: req.CreatedBy,
	}

	if req.Duration != nil {
		d, err := time.ParseDuration(*req.Duration)
		if err != nil {
			return endpoint.Status[Ban]{}, endpoint.Invalid(map[string]string{"duration": "invalid duration format (e.g. 24h, 168h)"})
		}
		// Use the same configured clock as the persisted creation timestamp.
		exp := h.db.NowFunc().Add(d)
		ban.ExpiresAt = &exp
	}

	// Upsert: update if IP already banned
	var existing models.Ban
	err := h.db.WithContext(r.Context()).Where("ip = ?", req.IP).First(&existing).Error
	if err == nil {
		if err := h.db.WithContext(r.Context()).Model(&existing).Updates(map[string]any{
			"reason":     ban.Reason,
			"protocol":   ban.Protocol,
			"created_by": ban.CreatedBy,
			"expires_at": ban.ExpiresAt,
		}).Error; err != nil {
			return endpoint.Status[Ban]{}, fmt.Errorf("update ban: %w", err)
		}
		existing.Reason = ban.Reason
		existing.Protocol = ban.Protocol
		existing.CreatedBy = ban.CreatedBy
		existing.ExpiresAt = ban.ExpiresAt
		return endpoint.Status[Ban]{Code: http.StatusOK, Value: banOf(existing)}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return endpoint.Status[Ban]{}, fmt.Errorf("find ban: %w", err)
	}

	if err := h.db.WithContext(r.Context()).Create(&ban).Error; err != nil {
		return endpoint.Status[Ban]{}, fmt.Errorf("create ban: %w", err)
	}

	return endpoint.Status[Ban]{Code: http.StatusCreated, Value: banOf(ban)}, nil
}

// banPath names the ban to delete.
// DELETE /api/v1/admin/bans/{id}
type banPath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}

func (h *BanHandler) delete(_ *http.Request, in banPath) (endpoint.Empty, error) {
	id := in.ID

	if err := h.db.Delete(&models.Ban{}, id).Error; err != nil {
		return endpoint.Empty{}, fmt.Errorf("delete ban: %w", err)
	}

	return endpoint.Empty{}, nil
}

// unbanIPRequest names the address to unban.
// DELETE /api/v1/admin/bans/ip/{ip}
type unbanIPRequest struct {
	IP string `path:"ip" minLength:"1" message:"IP address required" missing:"IP address required" example:"192.0.2.10"`
}

func (h *BanHandler) unbanIP(_ *http.Request, in unbanIPRequest) (endpoint.Empty, error) {
	ip := in.IP

	result := h.db.Where("ip = ?", ip).Delete(&models.Ban{})
	if result.Error != nil {
		return endpoint.Empty{}, fmt.Errorf("unban address: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "No ban found for this IP")
	}

	return endpoint.Empty{}, nil
}
