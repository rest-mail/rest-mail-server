package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type QueueHandler struct {
	db *gorm.DB
}

func NewQueueHandler(db *gorm.DB) *QueueHandler {
	return &QueueHandler{db: db}
}

// queuePageNumber keeps an explicitly empty pagination parameter equivalent to
// omission, as the queue list has always treated it. Other malformed numbers fail binding.
type queuePageNumber int

func (n *queuePageNumber) UnmarshalText(raw []byte) error {
	if len(raw) == 0 {
		*n = 0
		return nil
	}
	value, err := strconv.ParseInt(string(raw), 10, strconv.IntSize)
	if err != nil {
		return err
	}
	*n = queuePageNumber(value)
	return nil
}

type listQueueRequest struct {
	Status    *string          `query:"status" enums:"pending,delivering,deferred,delivered,bounced,expired" message:"must be one of pending, delivering, deferred, delivered, bounced, expired"`
	Domain    string           `query:"domain"`
	Sender    string           `query:"sender"`
	Recipient string           `query:"recipient"`
	Limit     *queuePageNumber `query:"limit"`
	Offset    *queuePageNumber `query:"offset"`
}

// QueueList preserves the queue's established pagination envelope.
type QueueList struct {
	Items  []QueueEntry `json:"items"`
	Total  int64        `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

func (h *QueueHandler) listQueue(_ *http.Request, in listQueueRequest) (QueueList, error) {
	query := h.db.Model(&models.OutboundQueue{}).Order("created_at DESC")

	if in.Status != nil {
		query = query.Where("status = ?", *in.Status)
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

	limit, offset := 50, 0
	if in.Limit != nil && *in.Limit > 0 && *in.Limit <= 200 {
		limit = int(*in.Limit)
	}
	if in.Offset != nil && *in.Offset >= 0 {
		offset = int(*in.Offset)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return QueueList{}, err
	}

	var items []models.OutboundQueue
	if err := query.Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return QueueList{}, err
	}

	entries := make([]QueueEntry, len(items))
	for i, item := range items {
		entries[i] = queueEntryOf(item)
	}
	return QueueList{Items: entries, Total: total, Limit: limit, Offset: offset}, nil
}

type queuePath struct {
	ID uint32 `path:"id" minimum:"1"`
}

var errQueueNotFound = endpoint.Refuse(http.StatusNotFound, "not_found", "Queue entry not found")

func (h *QueueHandler) findQueue(id uint32) (models.OutboundQueue, error) {
	var item models.OutboundQueue
	if err := h.db.First(&item, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return item, errQueueNotFound
		}
		return item, err
	}
	return item, nil
}

func (h *QueueHandler) getQueueEntry(_ *http.Request, in queuePath) (QueueEntry, error) {
	item, err := h.findQueue(in.ID)
	if err != nil {
		return QueueEntry{}, err
	}
	return queueEntryOf(item), nil
}

type QueueAction struct {
	Status string `json:"status"`
}

func (h *QueueHandler) retryQueueEntry(_ *http.Request, in queuePath) (QueueAction, error) {
	item, err := h.findQueue(in.ID)
	if err != nil {
		return QueueAction{}, err
	}
	if item.Status != "deferred" && item.Status != "pending" {
		return QueueAction{}, endpoint.Refuse(http.StatusBadRequest, "bad_request", "Only pending or deferred entries can be retried")
	}
	if err := h.db.Model(&item).Updates(map[string]interface{}{"status": "pending", "next_attempt": time.Now()}).Error; err != nil {
		return QueueAction{}, err
	}
	return QueueAction{Status: "retrying"}, nil
}

func (h *QueueHandler) bounceQueueEntry(_ *http.Request, in queuePath) (QueueAction, error) {
	item, err := h.findQueue(in.ID)
	if err != nil {
		return QueueAction{}, err
	}
	if err := h.db.Model(&item).Updates(map[string]interface{}{"status": "bounced", "last_error": "manually bounced by admin"}).Error; err != nil {
		return QueueAction{}, err
	}
	return QueueAction{Status: "bounced"}, nil
}

// deleteQueueEntry answers 404 when no entry exists, like other admin deletes.
func (h *QueueHandler) deleteQueueEntry(_ *http.Request, in queuePath) (endpoint.Empty, error) {
	result := h.db.Delete(&models.OutboundQueue{}, in.ID)
	if result.Error != nil {
		return endpoint.Empty{}, fmt.Errorf("delete queue entry: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return endpoint.Empty{}, errQueueNotFound
	}
	return endpoint.Empty{}, nil
}

// queueStats returns aggregate statistics for the outbound queue.
// GET /api/v1/admin/queue/stats
func (h *QueueHandler) queueStats(_ *http.Request, _ struct{}) (QueueStatistics, error) {
	type statusCount struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
	}

	var counts []statusCount
	if err := h.db.Model(&models.OutboundQueue{}).
		Select("status, count(*) as count").
		Group("status").
		Scan(&counts).Error; err != nil {
		return QueueStatistics{}, err
	}

	stats := QueueStatistics{}
	for _, c := range counts {
		stats.Total += c.Count
		switch c.Status {
		case "pending":
			stats.Pending = c.Count
		case "deferred":
			stats.Deferred = c.Count
		case "delivered":
			stats.Delivered = c.Count
		case "bounced":
			stats.Bounced = c.Count
		case "delivering":
			stats.Delivering = c.Count
		case "expired":
			stats.Expired = c.Count
		}
	}

	return stats, nil
}

type bulkQueueRequest struct {
	IDs    []uint `json:"ids,omitempty" maxItems:"1000" message:"max 1000 IDs per request"`
	Filter *struct {
		Status    string `json:"status,omitempty"`
		Domain    string `json:"domain,omitempty"`
		Sender    string `json:"sender,omitempty"`
		Recipient string `json:"recipient,omitempty"`
	} `json:"filter,omitempty"`
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
		if req.Filter.Sender != "" {
			query = query.Where("sender ILIKE ? ESCAPE '\\'", "%"+escapeLike(req.Filter.Sender)+"%")
		}
		if req.Filter.Recipient != "" {
			query = query.Where("recipient ILIKE ? ESCAPE '\\'", "%"+escapeLike(req.Filter.Recipient)+"%")
		}
	}
	return query
}

// bulkRetry forces immediate retry for multiple queue entries.
// POST /api/v1/admin/queue/bulk-retry
func (h *QueueHandler) bulkRetry(_ *http.Request, req bulkQueueRequest) (BulkQueueResult, error) {
	if len(req.IDs) == 0 && req.Filter == nil {
		return BulkQueueResult{}, endpoint.Refuse(http.StatusBadRequest, "bad_request", "ids or filter required")
	}

	result := h.buildBulkQuery(req).
		Where("status IN ?", []string{"deferred", "pending"}).
		Updates(map[string]interface{}{
			"status":       "pending",
			"next_attempt": time.Now(),
		})

	if result.Error != nil {
		return BulkQueueResult{}, result.Error
	}
	return BulkQueueResult{Affected: result.RowsAffected}, nil
}

// bulkBounce forces a bounce for multiple queue entries.
// POST /api/v1/admin/queue/bulk-bounce
func (h *QueueHandler) bulkBounce(_ *http.Request, req bulkQueueRequest) (BulkQueueResult, error) {
	if len(req.IDs) == 0 && req.Filter == nil {
		return BulkQueueResult{}, endpoint.Refuse(http.StatusBadRequest, "bad_request", "ids or filter required")
	}

	// Only bounce mail that is still in flight. Without this guard a bulk bounce
	// by filter would also flip already-delivered (or expired/bounced) rows to
	// "bounced", corrupting their final state.
	result := h.buildBulkQuery(req).
		Where("status IN ?", []string{"pending", "deferred", "delivering"}).
		Updates(map[string]interface{}{
			"status":     "bounced",
			"last_error": "manually bounced by admin (bulk)",
		})

	if result.Error != nil {
		return BulkQueueResult{}, result.Error
	}
	return BulkQueueResult{Affected: result.RowsAffected}, nil
}

// bulkDelete removes multiple queue entries.
// DELETE /api/v1/admin/queue/bulk-delete
func (h *QueueHandler) bulkDelete(_ *http.Request, req bulkQueueRequest) (BulkQueueResult, error) {
	if len(req.IDs) == 0 && req.Filter == nil {
		return BulkQueueResult{}, endpoint.Refuse(http.StatusBadRequest, "bad_request", "ids or filter required")
	}

	result := h.buildBulkQuery(req).Delete(&models.OutboundQueue{})

	if result.Error != nil {
		return BulkQueueResult{}, result.Error
	}
	return BulkQueueResult{Affected: result.RowsAffected}, nil
}

type QueueEntry struct {
	ID        uint   `json:"id"`
	MessageID *uint  `json:"message_id"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Domain    string `json:"domain"` // destination domain for MX lookup
	// BodyType is the SMTP BODY= parameter declared for this message at submission
	// (7BIT, 8BITMIME, or BINARYMIME; empty when the client declared none). The
	// outbound worker consults it so it never relays 8-bit content to a next hop
	// that does not advertise 8BITMIME (RFC 6152 §3).
	BodyType string `json:"body_type"`
	Status   string `json:"status" enums:"pending,delivering,deferred,delivered,bounced,expired"`
	// Status values: pending, delivering, deferred, delivered, bounced, expired
	Attempts      int        `json:"attempts"`
	LastAttempt   *time.Time `json:"last_attempt"`
	NextAttempt   time.Time  `json:"next_attempt"`
	LastError     string     `json:"last_error"`
	LastErrorCode int        `json:"last_error_code"`
	// FailureCategory is what kind of failure the last attempt was, classified
	// from the remote's reply (queue.ClassifyFailure); empty until an attempt
	// fails. It decides how the message is retried, and whether at all.
	FailureCategory string    `json:"failure_category" enums:",bad_mailbox,bad_domain,policy,other_permanent,mailbox_full,greylisted,rate_limited,connection,tls,dns,other_transient"`
	MaxRetries      int       `json:"max_retries"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

func queueEntryOf(q models.OutboundQueue) QueueEntry {
	return QueueEntry{
		ID: q.ID, MessageID: q.MessageID, Sender: q.Sender, Recipient: q.Recipient,
		Domain: q.Domain, BodyType: q.BodyType, Status: q.Status, Attempts: q.Attempts,
		LastAttempt: q.LastAttempt, NextAttempt: q.NextAttempt, LastError: q.LastError,
		LastErrorCode: q.LastErrorCode, FailureCategory: string(q.FailureCategory),
		MaxRetries: q.MaxRetries, CreatedAt: q.CreatedAt, ExpiresAt: q.ExpiresAt,
	}
}

// QueueStatistics always includes the four original counters and total. Delivering
// and expired appear only when present, preserving the production JSON shape.
type QueueStatistics struct {
	Pending    int64 `json:"pending"`
	Deferred   int64 `json:"deferred"`
	Delivered  int64 `json:"delivered"`
	Bounced    int64 `json:"bounced"`
	Total      int64 `json:"total"`
	Delivering int64 `json:"delivering,omitempty"`
	Expired    int64 `json:"expired,omitempty"`
}

type BulkQueueResult struct {
	Affected int64 `json:"affected"`
}

type QueueRouteChecks struct {
	At func(string) endpoint.Middleware
}

func (h *QueueHandler) Register(rs endpoint.Routes, checks QueueRouteChecks) {
	tags := []string{"Admin: Queue"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/queue",
		OperationID: "listQueue", Summary: "List outbound queue entries", Tags: tags, Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueRead)}}, h.listQueue)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/queue/stats",
		OperationID: "getQueueStats", Summary: "Get queue status counts", Tags: tags, Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueRead)}}, h.queueStats)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/queue/bulk-retry",
		OperationID: "bulkRetryQueue", Summary: "Retry multiple queue entries", Tags: tags, Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueManage)}}, h.bulkRetry)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/queue/bulk-bounce",
		OperationID: "bulkBounceQueue", Summary: "Bounce multiple queue entries", Tags: tags, Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueManage)}}, h.bulkBounce)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/queue/bulk-delete",
		OperationID: "bulkDeleteQueue", Summary: "Delete multiple queue entries", Tags: tags, Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueManage)}}, h.bulkDelete)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/queue/{id}",
		OperationID: "getQueueEntry", Summary: "Get a single queue entry", Tags: tags, Errors: []int{http.StatusNotFound}, Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueRead)}}, h.getQueueEntry)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/queue/{id}/retry",
		OperationID: "retryQueueEntry", Summary: "Retry a single queue entry", Tags: tags, Errors: []int{http.StatusNotFound, http.StatusBadRequest}, Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueManage)}}, h.retryQueueEntry)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/queue/{id}/bounce",
		OperationID: "bounceQueueEntry", Summary: "Bounce a single queue entry", Tags: tags, Errors: []int{http.StatusNotFound}, Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueManage)}}, h.bounceQueueEntry)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/queue/{id}",
		OperationID: "deleteQueueEntry", Summary: "Delete a queue entry", Tags: tags, Status: http.StatusNoContent, Errors: []int{http.StatusNotFound}, Middleware: []endpoint.Middleware{checks.At(middleware.CapQueueManage)}}, h.deleteQueueEntry)
}
