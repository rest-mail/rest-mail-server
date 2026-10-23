package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// AuditHandler serves the audit log to admins holding audit:read: the entries,
// and a check of their chain. Both are themselves audited (audit.read).
type AuditHandler struct {
	db *gorm.DB
}

func NewAuditHandler(db *gorm.DB) *AuditHandler {
	return &AuditHandler{db: db}
}

const auditDefaultLimit = 50

// listAuditRequest filters audit entries, newest first.
// GET /api/v1/admin/audit?actor_kind=&actor_id=&actor=&action=&outcome=&since=&until=&before_seq=&limit=
//
// A page is followed by the next with before_seq set to the cursor. A filter
// that does not parse is refused rather than ignored: an auditor must never
// read "everything" believing it is a filtered view.
type listAuditRequest struct {
	ActorKind string     `query:"actor_kind" enums:",account,system,anonymous" message:"must be account, system or anonymous"`
	ActorID   *uint64    `query:"actor_id" minimum:"0" message:"must be a whole number"`
	Actor     string     `query:"actor"`
	Action    string     `query:"action"`
	Outcome   string     `query:"outcome" enums:",success,denied,failed,error" message:"must be success, denied, failed or error"`
	Since     *time.Time `query:"since" message:"must be an RFC 3339 time"`
	Until     *time.Time `query:"until" message:"must be an RFC 3339 time"`
	BeforeSeq *int64     `query:"before_seq" minimum:"1" message:"must be a positive whole number"`
	Limit     *int       `query:"limit" minimum:"1" maximum:"200" message:"must be between 1 and 200"`
}

func (h *AuditHandler) list(_ *http.Request, in listAuditRequest) (endpoint.List[AuditEntry], error) {
	query := h.db.Model(&models.AuditEntry{})
	if in.ActorKind != "" {
		query = query.Where("actor_kind = ?", in.ActorKind)
	}
	if in.ActorID != nil {
		query = query.Where("actor_id = ?", *in.ActorID)
	}
	if in.Actor != "" {
		query = query.Where("actor_name ILIKE ? ESCAPE '\\'", "%"+escapeLike(in.Actor)+"%")
	}
	if in.Action != "" {
		query = query.Where("action = ?", in.Action)
	}
	if in.Outcome != "" {
		query = query.Where("outcome = ?", in.Outcome)
	}
	if in.Since != nil {
		query = query.Where("occurred_at >= ?", *in.Since)
	}
	if in.Until != nil {
		query = query.Where("occurred_at <= ?", *in.Until)
	}
	if in.BeforeSeq != nil {
		query = query.Where("seq < ?", *in.BeforeSeq)
	}
	limit := auditDefaultLimit
	if in.Limit != nil {
		limit = *in.Limit
	}

	// One more than a page says whether there is another.
	entries := []models.AuditEntry{}
	if err := query.Order("seq DESC").Limit(limit + 1).Find(&entries).Error; err != nil {
		return endpoint.List[AuditEntry]{}, fmt.Errorf("read audit log: %w", err)
	}
	// Stored as timestamptz, read back in the server's zone: show UTC, as hashed.
	for i := range entries {
		entries[i].OccurredAt = entries[i].OccurredAt.UTC()
	}
	page := &respond.Pagination{}
	if len(entries) > limit {
		entries = entries[:limit]
		page.HasMore = true
		page.Cursor = strconv.FormatInt(entries[limit-1].Seq, 10)
	}
	out := make([]AuditEntry, len(entries))
	for i, row := range entries {
		out[i] = auditEntryOf(row)
	}
	return endpoint.List[AuditEntry]{Data: out, Pagination: page}, nil
}

// verifyAuditRequest checks the whole chain and reports whether every
// entry holds, or the first that does not.
// GET /api/v1/admin/audit/verify?anchor=<seq>:<hash>
//
// Given an anchor (a (seq, hash) kept outside the server), the chain must also
// still hold that entry with that hash: a log rewritten and rehashed by
// whoever owns the database holds as a chain, but not to the anchor.
type verifyAuditRequest struct {
	Anchor string `query:"anchor" pattern:"^$|^[1-9][0-9]{0,17}:[0-9a-fA-F]{64}$" message:"must be <seq>:<hash>, a positive whole number and 64 hex digits"`
}

func (h *AuditHandler) verify(r *http.Request, in verifyAuditRequest) (audit.Result, error) {
	var anchor *audit.Anchor
	if in.Anchor != "" {
		parts := strings.SplitN(in.Anchor, ":", 2)
		seq, _ := strconv.ParseInt(parts[0], 10, 64) // binding permits at most 18 digits
		anchor = &audit.Anchor{Seq: seq, Hash: strings.ToLower(parts[1])}
	}

	res, err := audit.VerifyDBAnchored(h.db.WithContext(r.Context()), anchor)
	if err != nil {
		return audit.Result{}, fmt.Errorf("verify audit log: %w", err)
	}
	return res, nil
}
