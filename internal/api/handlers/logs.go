package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// LogHandler provides admin endpoints for querying delivery and activity logs.
type LogHandler struct {
	db *gorm.DB
}

func NewLogHandler(db *gorm.DB) *LogHandler {
	return &LogHandler{db: db}
}

// DeliveryLog returns a filtered view of outbound delivery history.
// GET /api/v1/admin/logs/delivery?status=...&sender=...&recipient=...&since=...&until=...&limit=50&offset=0
func (h *LogHandler) DeliveryLog(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	query := h.db.Model(&models.OutboundQueue{})

	if status := r.URL.Query().Get("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if domain := r.URL.Query().Get("domain"); domain != "" {
		query = query.Where("domain = ?", domain)
	}
	if sender := r.URL.Query().Get("sender"); sender != "" {
		query = query.Where("sender ILIKE ?", "%"+sender+"%")
	}
	if recipient := r.URL.Query().Get("recipient"); recipient != "" {
		query = query.Where("recipient ILIKE ?", "%"+recipient+"%")
	}
	if since := r.URL.Query().Get("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			query = query.Where("created_at >= ?", t)
		}
	}
	if until := r.URL.Query().Get("until"); until != "" {
		if t, err := time.Parse(time.RFC3339, until); err == nil {
			query = query.Where("created_at <= ?", t)
		}
	}

	var total int64
	query.Count(&total)

	var entries []models.OutboundQueue
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&entries).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to query delivery log")
		return
	}

	respond.List(w, entries, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}

// ActivityLog returns admin/system activity events.
// GET /api/v1/admin/logs/activity?actor=...&action=...&since=...&until=...&limit=50&offset=0
func (h *LogHandler) ActivityLog(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	query := h.db.Model(&models.ActivityLog{})

	if actor := r.URL.Query().Get("actor"); actor != "" {
		query = query.Where("actor ILIKE ?", "%"+actor+"%")
	}
	if action := r.URL.Query().Get("action"); action != "" {
		query = query.Where("action = ?", action)
	}
	if resource := r.URL.Query().Get("resource_type"); resource != "" {
		query = query.Where("resource_type = ?", resource)
	}
	if since := r.URL.Query().Get("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			query = query.Where("created_at >= ?", t)
		}
	}
	if until := r.URL.Query().Get("until"); until != "" {
		if t, err := time.Parse(time.RFC3339, until); err == nil {
			query = query.Where("created_at <= ?", t)
		}
	}

	var total int64
	query.Count(&total)

	var logs []models.ActivityLog
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&logs).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to query activity log")
		return
	}

	respond.List(w, logs, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}
