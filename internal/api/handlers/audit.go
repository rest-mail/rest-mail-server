package handlers

import (
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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

const (
	auditDefaultLimit = 50
	auditMaxLimit     = 200
)

// List returns audit entries, newest first.
// GET /api/v1/admin/audit?actor_kind=&actor_id=&actor=&action=&outcome=&since=&until=&before_seq=&limit=
//
// A page is followed by the next with before_seq set to the cursor. A filter
// that does not parse is refused rather than ignored: an auditor must never
// read "everything" believing it is a filtered view.
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	invalid := map[string]string{}
	query := h.db.Model(&models.AuditEntry{})

	if kind := q.Get("actor_kind"); kind != "" {
		switch kind {
		case models.AuditActorAccount, models.AuditActorSystem, models.AuditActorAnonymous:
			query = query.Where("actor_kind = ?", kind)
		default:
			invalid["actor_kind"] = "must be account, system or anonymous"
		}
	}
	if v := q.Get("actor_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			query = query.Where("actor_id = ?", id)
		} else {
			invalid["actor_id"] = "must be a whole number"
		}
	}
	if actor := q.Get("actor"); actor != "" {
		query = query.Where("actor_name ILIKE ? ESCAPE '\\'", "%"+escapeLike(actor)+"%")
	}
	if action := q.Get("action"); action != "" {
		query = query.Where("action = ?", action)
	}
	if outcome := q.Get("outcome"); outcome != "" {
		switch outcome {
		case models.AuditOutcomeSuccess, models.AuditOutcomeDenied, models.AuditOutcomeFailed, models.AuditOutcomeError:
			query = query.Where("outcome = ?", outcome)
		default:
			invalid["outcome"] = "must be success, denied, failed or error"
		}
	}
	for _, f := range []struct{ param, cond string }{
		{"since", "occurred_at >= ?"},
		{"until", "occurred_at <= ?"},
	} {
		if v := q.Get(f.param); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				query = query.Where(f.cond, t)
			} else {
				invalid[f.param] = "must be an RFC 3339 time"
			}
		}
	}
	if v := q.Get("before_seq"); v != "" {
		if seq, err := strconv.ParseInt(v, 10, 64); err == nil && seq > 0 {
			query = query.Where("seq < ?", seq)
		} else {
			invalid["before_seq"] = "must be a positive whole number"
		}
	}
	limit := auditDefaultLimit
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= auditMaxLimit {
			limit = n
		} else {
			invalid["limit"] = "must be between 1 and 200"
		}
	}
	if len(invalid) > 0 {
		respond.ValidationError(w, invalid)
		return
	}

	// One more than a page says whether there is another.
	entries := []models.AuditEntry{}
	if err := query.Order("seq DESC").Limit(limit + 1).Find(&entries).Error; err != nil {
		slog.Error("reading the audit log failed", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read the audit log")
		return
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
	respond.List(w, entries, page)
}

// Verify walks the whole chain from its lowest seq and reports whether every
// entry holds, or the first that does not.
// GET /api/v1/admin/audit/verify?anchor_seq=&anchor_hash=
//
// Given an anchor (both parameters, a (seq, hash) kept outside the server),
// the chain must also still hold that entry with that hash: a log rewritten
// and rehashed by whoever owns the database holds as a chain, but not to the
// anchor.
func (h *AuditHandler) Verify(w http.ResponseWriter, r *http.Request) {
	anchor, invalid := parseAuditAnchor(r)
	if len(invalid) > 0 {
		respond.ValidationError(w, invalid)
		return
	}
	res, err := audit.VerifyDBAnchored(h.db.WithContext(r.Context()), anchor)
	if err != nil {
		slog.Error("verifying the audit log failed", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify the audit log")
		return
	}
	respond.Data(w, http.StatusOK, res)
}

// parseAuditAnchor reads an optional anchor: anchor_seq and anchor_hash, both
// or neither. The hash is 64 hex digits, either case.
func parseAuditAnchor(r *http.Request) (*audit.Anchor, map[string]string) {
	q := r.URL.Query()
	seqParam, hashParam := q.Get("anchor_seq"), q.Get("anchor_hash")
	if seqParam == "" && hashParam == "" {
		return nil, nil
	}
	invalid := map[string]string{}
	seq, err := strconv.ParseInt(seqParam, 10, 64)
	if err != nil || seq <= 0 {
		invalid["anchor_seq"] = "must be a positive whole number, given with anchor_hash"
	}
	hash := strings.ToLower(hashParam)
	if b, err := hex.DecodeString(hash); err != nil || len(b) != 32 {
		invalid["anchor_hash"] = "must be 64 hex digits, given with anchor_seq"
	}
	if len(invalid) > 0 {
		return nil, invalid
	}
	return &audit.Anchor{Seq: seq, Hash: hash}, nil
}
