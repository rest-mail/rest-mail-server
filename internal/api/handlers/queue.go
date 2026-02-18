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
	if sender := r.URL.Query().Get("sender"); sender != "" {
		query = query.Where("sender ILIKE ?", "%"+sender+"%")
	}
	if recipient := r.URL.Query().Get("recipient"); recipient != "" {
		query = query.Where("recipient ILIKE ?", "%"+recipient+"%")
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

type bulkQueueRequest struct {
	IDs    []uint `json:"ids"`
	Filter *struct {
		Status string `json:"status"`
		Domain string `json:"domain"`
	} `json:"filter"`
}

func (h *QueueHandler) buildBulkQuery(req bulkQueueRequest) *gorm.DB {
	query := h.db.Model(&models.OutboundQueue{})
	if len(req.IDs) > 0 {
		return query.Where("id IN ?", req.IDs)
	}
	if req.Filter != nil {
		if req.Filter.Status != "" {
			query = query.Where("status = ?", req.Filter.Status)
		}
		if req.Filter.Domain != "" {
			query = query.Where("domain = ?", req.Filter.Domain)
		}
	}
	return query
}

// BulkRetry forces immediate retry for multiple queue entries.
// POST /api/v1/admin/queue/bulk-retry
func (h *QueueHandler) BulkRetry(w http.ResponseWriter, r *http.Request) {
	var req bulkQueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if len(req.IDs) == 0 && req.Filter == nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "ids or filter required")
		return
	}
	if len(req.IDs) > 1000 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "max 1000 IDs per request")
		return
	}

	result := h.buildBulkQuery(req).
		Where("status IN ?", []string{"deferred", "pending"}).
		Updates(map[string]interface{}{
			"status":       "pending",
			"next_attempt": time.Now(),
		})

	respond.Data(w, http.StatusOK, map[string]int64{"affected": result.RowsAffected})
}

// BulkBounce forces a bounce for multiple queue entries.
// POST /api/v1/admin/queue/bulk-bounce
func (h *QueueHandler) BulkBounce(w http.ResponseWriter, r *http.Request) {
	var req bulkQueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if len(req.IDs) == 0 && req.Filter == nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "ids or filter required")
		return
	}
	if len(req.IDs) > 1000 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "max 1000 IDs per request")
		return
	}

	result := h.buildBulkQuery(req).Updates(map[string]interface{}{
		"status":     "bounced",
		"last_error": "manually bounced by admin (bulk)",
	})

	respond.Data(w, http.StatusOK, map[string]int64{"affected": result.RowsAffected})
}

// BulkDelete removes multiple queue entries.
// DELETE /api/v1/admin/queue/bulk-delete
func (h *QueueHandler) BulkDelete(w http.ResponseWriter, r *http.Request) {
	var req bulkQueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if len(req.IDs) == 0 && req.Filter == nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "ids or filter required")
		return
	}
	if len(req.IDs) > 1000 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "max 1000 IDs per request")
		return
	}

	result := h.buildBulkQuery(req).Delete(&models.OutboundQueue{})

	respond.Data(w, http.StatusOK, map[string]int64{"affected": result.RowsAffected})
}
