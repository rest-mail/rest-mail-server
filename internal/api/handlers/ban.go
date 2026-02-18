package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
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

// ListBans returns all active bans with optional filtering.
// GET /api/v1/admin/bans?protocol=smtp&active=true&limit=50&offset=0
func (h *BanHandler) ListBans(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	query := h.db.Model(&models.Ban{})

	if protocol := r.URL.Query().Get("protocol"); protocol != "" {
		query = query.Where("protocol = ? OR protocol = 'all'", protocol)
	}
	if ip := r.URL.Query().Get("ip"); ip != "" {
		query = query.Where("ip = ?", ip)
	}
	// Default: only show active (non-expired) bans
	if active := r.URL.Query().Get("active"); active != "false" {
		query = query.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	}

	var total int64
	query.Count(&total)

	var bans []models.Ban
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&bans).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list bans")
		return
	}

	respond.List(w, bans, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}

// CreateBan adds a new IP ban.
// POST /api/v1/admin/bans
func (h *BanHandler) CreateBan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP        string  `json:"ip"`
		Reason    string  `json:"reason"`
		Protocol  string  `json:"protocol"`   // "smtp", "imap", "pop3", "all"
		Duration  *string `json:"duration"`    // e.g. "24h", "168h"; nil = permanent
		CreatedBy string  `json:"created_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.IP == "" {
		respond.ValidationError(w, map[string]string{"ip": "IP address is required"})
		return
	}
	if req.Protocol == "" {
		req.Protocol = "all"
	}

	ban := models.Ban{
		IP:        req.IP,
		Reason:    req.Reason,
		Protocol:  req.Protocol,
		CreatedBy: req.CreatedBy,
	}

	if req.Duration != nil {
		d, err := time.ParseDuration(*req.Duration)
		if err != nil {
			respond.ValidationError(w, map[string]string{"duration": "invalid duration format (e.g. 24h, 168h)"})
			return
		}
		exp := time.Now().Add(d)
		ban.ExpiresAt = &exp
	}

	// Upsert: update if IP already banned
	var existing models.Ban
	if err := h.db.Where("ip = ?", req.IP).First(&existing).Error; err == nil {
		h.db.Model(&existing).Updates(map[string]any{
			"reason":     ban.Reason,
			"protocol":   ban.Protocol,
			"created_by": ban.CreatedBy,
			"expires_at": ban.ExpiresAt,
		})
		existing.Reason = ban.Reason
		existing.Protocol = ban.Protocol
		existing.CreatedBy = ban.CreatedBy
		existing.ExpiresAt = ban.ExpiresAt
		respond.Data(w, http.StatusOK, existing)
		return
	}

	if err := h.db.Create(&ban).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create ban")
		return
	}

	respond.Data(w, http.StatusCreated, ban)
}

// DeleteBan removes a ban by ID (unbans the IP).
// DELETE /api/v1/admin/bans/{id}
func (h *BanHandler) DeleteBan(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid ban ID")
		return
	}

	if err := h.db.Delete(&models.Ban{}, id).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete ban")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UnbanIP removes a ban by IP address.
// DELETE /api/v1/admin/bans/ip/{ip}
func (h *BanHandler) UnbanIP(w http.ResponseWriter, r *http.Request) {
	ip := chi.URLParam(r, "ip")
	if ip == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "IP address required")
		return
	}

	result := h.db.Where("ip = ?", ip).Delete(&models.Ban{})
	if result.RowsAffected == 0 {
		respond.Error(w, http.StatusNotFound, "not_found", "No ban found for this IP")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
