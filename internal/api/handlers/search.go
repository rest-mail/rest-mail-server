package handlers

import (
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
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

// searchRequest filters an account's messages: q searches subject and body, from the
// sender, after and before the time it was received, has_attachment those with any.
type searchRequest struct {
	ID            uint32     `path:"id" minimum:"1"`
	Q             string     `query:"q"`
	Folder        string     `query:"folder"`
	From          string     `query:"from"`
	After         *time.Time `query:"after" message:"must be an RFC 3339 date-time"`
	Before        *time.Time `query:"before" message:"must be an RFC 3339 date-time"`
	HasAttachment string     `query:"has_attachment" enums:",true,false" message:"must be true or false"`
	Limit         *int       `query:"limit" description:"1 to 100; 50 otherwise"`
}

// Register mounts searching an account's messages. Its route takes what the mail routes take;
// an account the session cannot reach with mail:read is 404 not_found.
func (h *SearchHandler) Register(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/search",
		OperationID: "searchMessages", Summary: "Search messages", Tags: []string{"Search"},
		Errors:   []int{http.StatusNotFound},
		Security: handlerChecked(models.CapMailRead, "CookieAuth", "BearerAuth", "APIKeyAuth", "BasicAuth")}, h.search)
}

// search performs full-text search across messages for an account.
// GET /api/v1/accounts/:id/search?q=...&folder=...&from=...&after=...&before=...
func (h *SearchHandler) search(r *http.Request, in searchRequest) (endpoint.List[Message], error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.List[Message]{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}

	mailboxID, err := resolveAccountMailbox(h.db, uint(in.ID), claims, models.CapMailRead)
	if err != nil {
		return endpoint.List[Message]{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Account not found")
	}

	query := h.db.Model(&models.Message{}).Where("mailbox_id = ? AND is_deleted = false", mailboxID)

	// Full-text search
	if in.Q != "" {
		query = query.Where(
			"to_tsvector('english', coalesce(subject, '') || ' ' || coalesce(body_text, '')) @@ plainto_tsquery('english', ?)", in.Q,
		)
	}

	// Folder filter
	if in.Folder != "" {
		query = query.Where("folder = ?", in.Folder)
	}

	// From filter (LIKE metacharacters escaped so they match literally)
	if in.From != "" {
		query = query.Where("sender ILIKE ? ESCAPE '\\'", "%"+escapeLike(in.From)+"%")
	}

	// Date filters
	if in.After != nil {
		query = query.Where("received_at >= ?", *in.After)
	}
	if in.Before != nil {
		query = query.Where("received_at <= ?", *in.Before)
	}

	// Has attachment filter
	if in.HasAttachment == "true" {
		query = query.Where("has_attachments = true")
	}

	limit := 50
	if in.Limit != nil && *in.Limit > 0 && *in.Limit <= 100 {
		limit = *in.Limit
	}

	var messages []models.Message
	var total int64

	query.Count(&total)
	query.Order("received_at DESC").Limit(limit).Find(&messages)

	return endpoint.List[Message]{Data: messagesOf(messages), Pagination: &respond.Pagination{
		HasMore: int64(limit) < total,
		Total:   total,
	}}, nil
}
