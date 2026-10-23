package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// LogHandler provides the admin endpoint for querying the delivery log.
type LogHandler struct {
	db *gorm.DB
}

func NewLogHandler(db *gorm.DB) *LogHandler {
	return &LogHandler{db: db}
}

// DeliveryLog returns a filtered view of outbound delivery history.
// GET /api/v1/admin/logs/delivery?status=...&sender=...&recipient=...&since=...&until=...&limit=50&offset=0
type deliveryLogRequest struct {
	Limit     *int       `query:"limit"`
	Offset    *int       `query:"offset"`
	Status    *string    `query:"status" enums:"pending,delivering,deferred,delivered,bounced,expired" message:"must be one of pending, delivering, deferred, delivered, bounced, expired"`
	Domain    string     `query:"domain"`
	Sender    string     `query:"sender"`
	Recipient string     `query:"recipient"`
	Since     *time.Time `query:"since" message:"must be an RFC 3339 time"`
	Until     *time.Time `query:"until" message:"must be an RFC 3339 time"`
}

func (h *LogHandler) deliveryLog(_ *http.Request, in deliveryLogRequest) (endpoint.List[DeliveryLogEntry], error) {
	// Preserve the delivery log's default for limits outside 1..200.
	limit, offset := 50, 0
	if in.Limit != nil && *in.Limit > 0 && *in.Limit <= 200 {
		limit = *in.Limit
	}
	if in.Offset != nil && *in.Offset >= 0 {
		offset = *in.Offset
	}

	query := h.db.Model(&models.OutboundQueue{})

	if in.Status != nil {
		status := *in.Status
		query = query.Where("status = ?", status)
	}
	if domain := in.Domain; domain != "" {
		query = query.Where("domain = ?", domain)
	}
	if sender := in.Sender; sender != "" {
		query = query.Where("sender ILIKE ? ESCAPE '\\'", "%"+escapeLike(sender)+"%")
	}
	if recipient := in.Recipient; recipient != "" {
		query = query.Where("recipient ILIKE ? ESCAPE '\\'", "%"+escapeLike(recipient)+"%")
	}
	if in.Since != nil {
		t := *in.Since
		query = query.Where("created_at >= ?", t)
	}
	if in.Until != nil {
		t := *in.Until
		query = query.Where("created_at <= ?", t)
	}

	var total int64
	query.Count(&total)

	var entries []models.OutboundQueue
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&entries).Error; err != nil {
		return endpoint.List[DeliveryLogEntry]{}, fmt.Errorf("delivery log: %w", err)
	}

	out := make([]DeliveryLogEntry, len(entries))
	for i, row := range entries {
		out[i] = deliveryLogEntryOf(row)
	}
	return endpoint.List[DeliveryLogEntry]{Data: out, Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, nil
}
