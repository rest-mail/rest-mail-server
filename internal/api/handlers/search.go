package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type SearchHandler struct {
	db *gorm.DB
}

func NewSearchHandler(db *gorm.DB) *SearchHandler {
	return &SearchHandler{db: db}
}

// Search performs full-text search across messages for an account.
// GET /api/v1/accounts/:id/search?q=...&folder=...&from=...&after=...&before=...
func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	// Resolve mailbox (reuse message handler's logic)
	var account models.WebmailAccount
	if err := h.db.First(&account, accountID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Account not found")
		return
	}
	if account.ID != claims.WebmailAccountID {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}
	mailboxID := account.PrimaryMailboxID

	query := h.db.Model(&models.Message{}).Where("mailbox_id = ? AND is_deleted = false", mailboxID)

	// Full-text search
	if q := r.URL.Query().Get("q"); q != "" {
		query = query.Where(
			"to_tsvector('english', coalesce(subject, '') || ' ' || coalesce(body_text, '')) @@ plainto_tsquery('english', ?)", q,
		)
	}

	// Folder filter
	if folder := r.URL.Query().Get("folder"); folder != "" {
		query = query.Where("folder = ?", folder)
	}

	// From filter
	if from := r.URL.Query().Get("from"); from != "" {
		query = query.Where("sender ILIKE ?", "%"+from+"%")
	}

	// Date filters
	if after := r.URL.Query().Get("after"); after != "" {
		query = query.Where("received_at >= ?", after)
	}
	if before := r.URL.Query().Get("before"); before != "" {
		query = query.Where("received_at <= ?", before)
	}

	// Has attachment filter
	if r.URL.Query().Get("has:attachment") == "true" {
		query = query.Where("has_attachments = true")
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	var messages []models.Message
	var total int64

	query.Count(&total)
	query.Order("received_at DESC").Limit(limit).Find(&messages)

	respond.List(w, messages, &respond.Pagination{
		HasMore: int64(limit) < total,
		Total:   total,
	})
}
