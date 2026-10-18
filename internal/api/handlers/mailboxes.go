package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// MailboxHandler serves the admin API's accounts: mailboxes, which are the
// accounts. Creating one is POST /api/v1/accounts, the same for everyone.
type MailboxHandler struct {
	db *gorm.DB
	// cache is the grants cache RequireCapability reads; deleting an account
	// removes grants, and empties it.
	cache *grants.Cache
	// sessions is the cache RejectStaleSessions reads; a new password, a
	// disable, a delete or ending the sessions empties the account's entry.
	sessions *SessionCache
}

func NewMailboxHandler(db *gorm.DB, cache *grants.Cache, sessions *SessionCache) *MailboxHandler {
	return &MailboxHandler{db: db, cache: cache, sessions: sessions}
}

func (h *MailboxHandler) List(w http.ResponseWriter, r *http.Request) {
	if !validateIntegerQuery(w, r, 0, "domain_id") {
		return
	}
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
	var hashed string
	if req.Password != nil {
		if len(*req.Password) < minChosenPasswordLength {
			respond.ValidationError(w, map[string]string{"password": fmt.Sprintf("must be at least %d characters", minChosenPasswordLength)})
			return
		}
		if hashed, err = hashPassword(*req.Password); err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to hash password")
			return
		}
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
	if len(cols) > 0 || hashed != "" {
		err := h.db.Transaction(func(tx *gorm.DB) error {
			if len(cols) > 0 {
				if err := tx.Model(&mailbox).Select(cols).Updates(updates).Error; err != nil {
					return err
				}
			}
			// A new password replaces every password the account had, revokes
			// every app password made under the old one and ends every session,
			// in this transaction: none may outlive it.
			if hashed != "" {
				var setBy *uint
				if claims := middleware.GetClaims(r); claims != nil {
					id := claims.AccountID
					setBy = &id
				}
				return setAccountPassword(tx, r, mailbox.ID, hashed, setBy, time.Now())
			}
			// A disable ends every session too, so enabling the account again
			// brings none of them back.
			if req.Active != nil && !*req.Active {
				return endSessions(tx, mailbox.ID)
			}
			return nil
		})
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update mailbox")
			return
		}
		if hashed != "" || req.Active != nil {
			h.sessions.Forget(mailbox.ID)
		}
	}

	h.db.Preload("Domain").Preload("QuotaUsage").First(&mailbox, id)
	respond.Data(w, http.StatusOK, mailbox)
}

// Delete deletes an account. Its credentials are revoked (the rows stay, for
// the audit trail) and its sessions end. Every grant it held is removed under
// the grants API's rules, since deleting an admin takes away what it held: the
// session must have signed in with 2FA and hold everything removed, and the
// last superadmin cannot go. Every grant others held on it (owners) is removed
// too, so a new account at the same address inherits nobody. Each removal has
// its audit entry, in the same transaction.
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
	claims := middleware.GetClaims(r)

	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var heldBy []models.Grant
		if err := tx.Preload("Role").Where("holder_kind = ? AND holder_id = ?", models.GrantHolderAccount, mailbox.ID).
			Order("id").Find(&heldBy).Error; err != nil {
			return err
		}
		if len(heldBy) > 0 && (claims == nil || !claims.HasAMR(auth.AMROTP)) {
			return errTwoFactorRequired
		}
		for i := range heldBy {
			if err := (grantChange{r: r, claims: claims}).remove(tx, &heldBy[i], http.StatusNoContent); err != nil {
				return err
			}
		}
		var heldOn []models.Grant
		if err := tx.Preload("Role").Where("scope = ?", grants.Mailbox(mailbox.Address)).Order("id").Find(&heldOn).Error; err != nil {
			return err
		}
		for i := range heldOn {
			if err := grants.Remove(tx, &heldOn[i], middleware.RequestEntry(r, grants.ActionDelete, http.StatusNoContent)); err != nil {
				return err
			}
		}
		if _, err := repositories.NewCredentialRepository(tx).RevokeKinds(mailbox.ID, time.Now(),
			models.CredentialKindPassword, models.CredentialKindTOTP, models.CredentialKindRecoveryCode, models.CredentialKindAppPassword); err != nil {
			return err
		}
		if err := repositories.NewRefreshTokenRepository(tx).RevokeAllForAccount(mailbox.ID); err != nil {
			return err
		}
		return tx.Delete(&mailbox).Error
	})
	if grantChangeFailed(w, err) {
		return
	}
	h.cache.Invalidate()
	h.sessions.Forget(mailbox.ID)
	w.WriteHeader(http.StatusNoContent)
}

// EndSessions signs an account out everywhere: every refresh token it holds is
// revoked and its credential epoch moves on, so each access token it was
// issued is refused at its next request, on the event stream and at refresh.
// Its passwords and app passwords stay: a mail client signed in with an app
// password keeps working, and is revoked on its own.
// DELETE /api/v1/admin/mailboxes/{id}/sessions
func (h *MailboxHandler) EndSessions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid mailbox ID")
		return
	}
	var mailbox models.Mailbox
	if err := h.db.Select("id").First(&mailbox, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		return
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error { return endSessions(tx, mailbox.ID) }); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to end the sessions")
		return
	}
	h.sessions.Forget(mailbox.ID)
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
