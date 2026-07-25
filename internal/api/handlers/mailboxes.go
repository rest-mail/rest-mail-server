package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

type MailboxHandler struct {
	db *gorm.DB
}

func NewMailboxHandler(db *gorm.DB) *MailboxHandler {
	return &MailboxHandler{db: db}
}

// revokeSessions best-effort invalidates every live session for a mailbox after
// a security-relevant change (password change, disable, delete), so an existing
// refresh token can no longer be exchanged for fresh access tokens. Failures are
// logged, not fatal: the primary mutation has already succeeded.
func (h *MailboxHandler) revokeSessions(mailboxID uint) {
	if h.db == nil {
		return
	}
	if err := repositories.NewRefreshTokenRepository(h.db).RevokeAllForSubject("mailbox", mailboxID); err != nil {
		slog.Warn("failed to revoke mailbox sessions", "mailbox_id", mailboxID, "error", err)
	}
}

func (h *MailboxHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r, defaultListLimit, maxListLimit)

	query := h.db.Model(&models.Mailbox{})
	if domainID := r.URL.Query().Get("domain_id"); domainID != "" {
		query = query.Where("domain_id = ?", domainID)
	}

	var total int64
	query.Count(&total)

	var mailboxes []models.Mailbox
	if err := query.Preload("Domain").Preload("QuotaUsage").Order("address ASC").Limit(limit).Offset(offset).Find(&mailboxes).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list mailboxes")
		return
	}
	respond.List(w, mailboxes, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}

type createMailboxRequest struct {
	Address     string `json:"address"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	QuotaBytes  *int64 `json:"quota_bytes"`
}

func (h *MailboxHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createMailboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	errs := map[string]string{}
	if req.Address == "" {
		errs["address"] = "required"
	}
	if req.Password == "" {
		errs["password"] = "required"
	} else if len(req.Password) < 8 {
		errs["password"] = "must be at least 8 characters"
	}
	if len(errs) > 0 {
		respond.ValidationError(w, errs)
		return
	}

	parts := strings.SplitN(req.Address, "@", 2)
	if len(parts) != 2 {
		respond.ValidationError(w, map[string]string{"address": "must be a valid email address"})
		return
	}
	localPart := parts[0]
	domainName := parts[1]

	var domain models.Domain
	if err := h.db.Where("name = ?", domainName).First(&domain).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain '"+domainName+"' not found")
		return
	}

	hashedPassword, err := auth.HashPassword(req.Password)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to hash password")
		return
	}

	mailbox := models.Mailbox{
		DomainID:    domain.ID,
		LocalPart:   localPart,
		Address:     req.Address,
		Password:    hashedPassword,
		DisplayName: req.DisplayName,
		Active:      true,
	}

	if req.QuotaBytes != nil {
		mailbox.QuotaBytes = *req.QuotaBytes
	} else {
		mailbox.QuotaBytes = domain.DefaultQuotaBytes
	}

	if err := h.db.Create(&mailbox).Error; err != nil {
		respond.Error(w, http.StatusConflict, "already_exists", "Mailbox already exists")
		return
	}

	// Create quota usage record
	h.db.Create(&models.QuotaUsage{MailboxID: mailbox.ID})

	respond.Data(w, http.StatusCreated, mailbox)
}

func (h *MailboxHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid mailbox ID")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.Preload("Domain").Preload("QuotaUsage").First(&mailbox, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		return
	}

	respond.Data(w, http.StatusOK, mailbox)
}

type updateMailboxRequest struct {
	Password    *string `json:"password"`
	DisplayName *string `json:"display_name"`
	QuotaBytes  *int64  `json:"quota_bytes"`
	Active      *bool   `json:"active"`
}

// mailboxUpdatableColumns is the exhaustive allowlist of DB columns the mailbox
// update handler may write. The write is bound to this set (via Select), so a
// field added to updateMailboxRequest — or a stray key threaded into the updates
// map — can never implicitly become a writable column (mass-assignment,
// CWE-915). Ownership/identity columns (domain_id, address, local_part) are
// deliberately absent. Any addition here must be reviewed.
var mailboxUpdatableColumns = map[string]bool{
	"password":     true,
	"display_name": true,
	"quota_bytes":  true,
	"active":       true,
}

func (h *MailboxHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid mailbox ID")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.First(&mailbox, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		return
	}

	var req updateMailboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.Password != nil {
		if len(*req.Password) < 8 {
			respond.ValidationError(w, map[string]string{"password": "must be at least 8 characters"})
			return
		}
		hashed, err := auth.HashPassword(*req.Password)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to hash password")
			return
		}
		updates["password"] = hashed
	}
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.QuotaBytes != nil {
		updates["quota_bytes"] = *req.QuotaBytes
	}
	if req.Active != nil {
		updates["active"] = *req.Active
	}

	// Bind the write to the reviewed column allowlist so only vetted columns can
	// ever be updated here, regardless of what the updates map contains.
	cols := make([]string, 0, len(updates))
	for k := range updates {
		if mailboxUpdatableColumns[k] {
			cols = append(cols, k)
		}
	}
	if len(cols) > 0 {
		if err := h.db.Model(&mailbox).Select(cols).Updates(updates).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update mailbox")
			return
		}
	}

	// A password change or a disable must not leave live sessions behind: revoke
	// every refresh token for this mailbox so it re-authenticates.
	if req.Password != nil || (req.Active != nil && !*req.Active) {
		h.revokeSessions(mailbox.ID)
	}

	h.db.Preload("Domain").Preload("QuotaUsage").First(&mailbox, id)
	respond.Data(w, http.StatusOK, mailbox)
}

func (h *MailboxHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid mailbox ID")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.First(&mailbox, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		return
	}

	if err := h.db.Delete(&mailbox).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete mailbox")
		return
	}

	// A deleted mailbox must not keep a usable session.
	h.revokeSessions(mailbox.ID)

	w.WriteHeader(http.StatusNoContent)
}

// CheckAddress verifies a recipient exists: GET /api/mailboxes?address=...
func (h *MailboxHandler) CheckAddress(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "address query parameter required")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.Where("address = ? AND active = ?", address, true).First(&mailbox).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// "Not local" is a valid answer to this query, not an error. The
			// SMTP gateway's apiclient (and its tests) expect 200 + exists=false
			// so authenticated submission can route external recipients to the
			// outbound queue — a 404 here surfaced as a client error and made
			// every external recipient temp-fail with 451 at RCPT.
			respond.Data(w, http.StatusOK, map[string]interface{}{
				"exists":  false,
				"address": address,
			})
			return
		}
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to check address")
		return
	}

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"exists":     true,
		"mailbox_id": mailbox.ID,
		"address":    mailbox.Address,
	})
}
