package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type QueueHandler struct {
	db *gorm.DB
}

func NewQueueHandler(db *gorm.DB) *QueueHandler {
	return &QueueHandler{db: db}
}

// ListQueue returns outbound queue entries with optional status filter.
// GET /api/v1/admin/queue?status=pending&limit=50&offset=0
func (h *QueueHandler) ListQueue(w http.ResponseWriter, r *http.Request) {
	query := h.db.Model(&models.OutboundQueue{}).Order("created_at DESC")

	if status := r.URL.Query().Get("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if domain := r.URL.Query().Get("domain"); domain != "" {
		query = query.Where("domain = ?", domain)
	}

	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	var total int64
	query.Count(&total)

	var items []models.OutboundQueue
	query.Limit(limit).Offset(offset).Find(&items)

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// GetQueueEntry returns details for a single queue entry.
// GET /api/v1/admin/queue/{id}
func (h *QueueHandler) GetQueueEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid queue entry ID")
		return
	}

	var item models.OutboundQueue
	if err := h.db.First(&item, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Queue entry not found")
		return
	}

	respond.Data(w, http.StatusOK, item)
}

// RetryQueueEntry forces an immediate retry of a deferred queue entry.
// POST /api/v1/admin/queue/{id}/retry
func (h *QueueHandler) RetryQueueEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid queue entry ID")
		return
	}

	var item models.OutboundQueue
	if err := h.db.First(&item, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Queue entry not found")
		return
	}

	if item.Status != "deferred" && item.Status != "pending" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Only pending or deferred entries can be retried")
		return
	}

	h.db.Model(&item).Updates(map[string]interface{}{
		"status":       "pending",
		"next_attempt": time.Now(),
	})

	respond.Data(w, http.StatusOK, map[string]string{"status": "retrying"})
}

// BounceQueueEntry forces a bounce for a queue entry.
// POST /api/v1/admin/queue/{id}/bounce
func (h *QueueHandler) BounceQueueEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid queue entry ID")
		return
	}

	var item models.OutboundQueue
	if err := h.db.First(&item, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Queue entry not found")
		return
	}

	h.db.Model(&item).Updates(map[string]interface{}{
		"status":     "bounced",
		"last_error": "manually bounced by admin",
	})

	respond.Data(w, http.StatusOK, map[string]string{"status": "bounced"})
}

// DeleteQueueEntry silently removes a queue entry.
// DELETE /api/v1/admin/queue/{id}
func (h *QueueHandler) DeleteQueueEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid queue entry ID")
		return
	}

	if err := h.db.Delete(&models.OutboundQueue{}, id).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete queue entry")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// QueueStats returns aggregate statistics for the outbound queue.
// GET /api/v1/admin/queue/stats
func (h *QueueHandler) QueueStats(w http.ResponseWriter, r *http.Request) {
	type statusCount struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
	}

	var counts []statusCount
	h.db.Model(&models.OutboundQueue{}).
		Select("status, count(*) as count").
		Group("status").
		Scan(&counts)

	stats := map[string]int64{}
	var total int64
	for _, c := range counts {
		stats[c.Status] = c.Count
		total += c.Count
	}
	stats["total"] = total

	respond.Data(w, http.StatusOK, stats)
}
