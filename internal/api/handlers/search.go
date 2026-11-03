package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/quiethours"
	"gorm.io/gorm"
)

type SearchHandler struct {
	db *gorm.DB
}

func NewSearchHandler(db *gorm.DB) *SearchHandler {
	return &SearchHandler{db: db}
}

// searchRequest filters an account's messages: q searches subject and body, from the
// sender, after and before the time it was received, has_attachment those with any. Bounds
// are read by CheckParameters, which alone knows whether the request names a zone.
type searchRequest struct {
	ID            uint32      `path:"id" minimum:"1" example:"1"`
	Q             string      `query:"q"`
	Folder        string      `query:"folder"`
	From          string      `query:"from"`
	After         searchBound `query:"after" pattern:"^$|^[0-9]{4}-[0-9]{2}-[0-9]{2}(T.*)?$" message:"must be an RFC 3339 date-time" description:"Received at or after this RFC 3339 date-time; with time_zone, or from the start of this calendar day (YYYY-MM-DD) in that zone"`
	Before        searchBound `query:"before" pattern:"^$|^[0-9]{4}-[0-9]{2}-[0-9]{2}(T.*)?$" message:"must be an RFC 3339 date-time" description:"Received at or before this RFC 3339 date-time; with time_zone, or up to the end of this calendar day (YYYY-MM-DD) in that zone"`
	TimeZone      string      `query:"time_zone" format:"iana-time-zone" pattern:"^$|^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$" message:"must be an IANA time zone name" example:"America/New_York" description:"The IANA time zone of the calendar days in after and before. An RFC 3339 date-time carries its own offset, so this does not change it."`
	HasAttachment string      `query:"has_attachment" enums:",true,false" message:"must be true or false"`
	Limit         *int        `query:"limit" description:"1 to 100; 50 otherwise"`

	receivedFrom, receivedUntil *time.Time
}

// searchBound is after or before as the request wrote it. A blank one is an omitted one.
type searchBound string

// Refusals of a bound that cannot be read. Without a zone a calendar day is not a bound at all.
const (
	boundNotAnInstant = "must be an RFC 3339 date-time"
	boundNotInZone    = "must be an RFC 3339 date-time or a calendar day (YYYY-MM-DD)"
)

// CheckParameters reads the zone and the bounds. A calendar day means its day in the zone, and
// both bounds include it, as they include an instant.
func (in *searchRequest) CheckParameters() map[string]string {
	fields := map[string]string{}
	var zone *time.Location
	if in.TimeZone != "" {
		var err error
		if zone, err = quiethours.Location(in.TimeZone); err != nil {
			fields["time_zone"] = "must be an IANA time zone name"
		}
	}
	read := func(name string, bound searchBound, edge func(time.Time, *time.Location) time.Time) *time.Time {
		if bound == "" {
			return nil
		}
		var at time.Time
		if at.UnmarshalText([]byte(bound)) == nil {
			return &at
		}
		if in.TimeZone == "" {
			fields[name] = boundNotAnInstant
			return nil
		}
		day, err := time.Parse(time.DateOnly, string(bound))
		if err != nil {
			fields[name] = boundNotInZone
			return nil
		}
		if zone == nil {
			return nil // The zone is what is refused.
		}
		at = edge(day, zone)
		return &at
	}
	in.receivedFrom = read("after", in.After, dayStart)
	in.receivedUntil = read("before", in.Before, dayEnd)
	return fields
}

// dayStart is the first instant of a calendar day in zone, the day being day's date. A clock
// change can skip midnight (America/Havana in March) or repeat it (in November), and time.Date
// then answers either side of the change, so the instant is searched for: the first second
// around that midnight whose date in the zone has reached the day. A day a zone skipped
// altogether (Pacific/Apia, 2011-12-30) starts when the next one does, and holds nothing.
func dayStart(day time.Time, zone *time.Location) time.Time {
	const margin = 48 * time.Hour // More than any change of a zone's offset.
	year, month, date := day.Date()
	wanted := calendarOrder(year, month, date)
	earliest := time.Date(year, month, date, 0, 0, 0, 0, zone).Add(-margin)
	seconds := sort.Search(int(2*margin/time.Second), func(i int) bool {
		y, m, d := earliest.Add(time.Duration(i) * time.Second).In(zone).Date()
		return calendarOrder(y, m, d) >= wanted
	})
	return earliest.Add(time.Duration(seconds) * time.Second)
}

// dayEnd is the last instant of a calendar day in zone that a stored time can be: PostgreSQL
// keeps microseconds.
func dayEnd(day time.Time, zone *time.Location) time.Time {
	return dayStart(day.AddDate(0, 0, 1), zone).Add(-time.Microsecond)
}

// calendarOrder orders dates by number.
func calendarOrder(year int, month time.Month, day int) int {
	return year*10000 + int(month)*100 + day
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
// GET /api/v1/accounts/:id/search?q=...&folder=...&from=...&after=...&before=...&time_zone=...
func (h *SearchHandler) search(r *http.Request, in searchRequest) (endpoint.List[Message], error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.List[Message]{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}

	mailboxID, err := resolveAccountMailbox(h.db, uint(in.ID), claims, models.CapMailRead)
	if err != nil {
		if errors.Is(err, errNoAccess) {
			return endpoint.List[Message]{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Account not found")
		}
		return endpoint.List[Message]{}, fmt.Errorf("resolve the searched account: %w", err)
	}

	query := h.db.Model(&models.Message{}).Where("mailbox_id = ? AND is_deleted = false AND imap_deleted = false", mailboxID)

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
	if in.receivedFrom != nil {
		query = query.Where("received_at >= ?", *in.receivedFrom)
	}
	if in.receivedUntil != nil {
		query = query.Where("received_at <= ?", *in.receivedUntil)
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

	if err := query.Count(&total).Error; err != nil {
		return endpoint.List[Message]{}, fmt.Errorf("count search results: %w", err)
	}
	if err := query.Select(messageThreadFields).Order("received_at DESC").Limit(limit).Find(&messages).Error; err != nil {
		return endpoint.List[Message]{}, fmt.Errorf("read search results: %w", err)
	}
	if err := attachAwaitingSince(h.db, mailboxID, messages); err != nil {
		return endpoint.List[Message]{}, err
	}

	return endpoint.List[Message]{Data: messagesOf(messages), Pagination: &respond.Pagination{
		HasMore: int64(limit) < total,
		Total:   total,
	}}, nil
}
