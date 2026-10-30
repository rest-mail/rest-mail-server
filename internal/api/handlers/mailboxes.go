package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	apitypes "github.com/restmail/restmail/internal/api/types"
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

// forRequest gives the handler's SQL work the HTTP request's budget and
// cancellation without changing the shared handler.
func (h *MailboxHandler) forRequest(r *http.Request) *MailboxHandler {
	if h.db == nil {
		return h
	}
	scoped := *h
	scoped.db = h.db.WithContext(r.Context())
	return &scoped
}

func (h *MailboxHandler) list(r *http.Request, in adminAddressListRequest) (endpoint.List[Mailbox], error) {
	h = h.forRequest(r)
	limit, offset := pagination(in.Limit, in.Offset, defaultListLimit, maxListLimit)

	query := filterAdminList(h.db.Model(&models.Mailbox{}), r, middleware.CapMailboxesRead, "mailboxes")
	if in.DomainID != nil {
		query = query.Where("domain_id = ?", *in.DomainID)
	}

	var total int64
	query.Count(&total)

	var mailboxes []models.Mailbox
	if err := query.Preload("Domain").Preload("QuotaUsage").Order("address ASC").Limit(limit).Offset(offset).Find(&mailboxes).Error; err != nil {
		return endpoint.List[Mailbox]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list mailboxes")
	}
	for i := range mailboxes {
		redactMailboxDomain(r, &mailboxes[i])
	}
	out := make([]Mailbox, len(mailboxes))
	for i, row := range mailboxes {
		out[i] = mailboxOf(row)
	}
	return endpoint.List[Mailbox]{Data: out, Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, nil
}

func (h *MailboxHandler) get(r *http.Request, in adminAddressPath) (Mailbox, error) {
	h = h.forRequest(r)
	id := in.ID

	var mailbox models.Mailbox
	if err := h.db.Preload("Domain").Preload("QuotaUsage").First(&mailbox, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Mailbox{}, endpoint.Refuse(http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		}
		return Mailbox{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to get mailbox")
	}

	redactMailboxDomain(r, &mailbox)
	return mailboxOf(mailbox), nil
}

type updateMailboxRequest = apitypes.UpdateMailboxRequest

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

func (h *MailboxHandler) update(r *http.Request, in updateMailboxInput) (Mailbox, error) {
	h = h.forRequest(r)
	id := in.ID

	var mailbox models.Mailbox
	if err := h.db.First(&mailbox, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Mailbox{}, endpoint.Refuse(http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		}
		return Mailbox{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to update mailbox")
	}
	// The answer is the mailbox as stored, read back in the change's own
	// transaction: a read-back that fails undoes the change rather than
	// answering with what was there before it.
	reload := func(db *gorm.DB) error {
		return db.Preload("Domain").Preload("QuotaUsage").First(&mailbox, id).Error
	}

	req := in.Body

	updates := map[string]interface{}{}
	var hashed string
	var err error
	if req.Password != nil {
		if len(*req.Password) < minChosenPasswordLength {
			return Mailbox{}, endpoint.Invalid(map[string]string{"password": fmt.Sprintf("must be at least %d characters", minChosenPasswordLength)})
		}
		if hashed, err = hashPassword(*req.Password); err != nil {
			return Mailbox{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to hash password")
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
		change := func(tx *gorm.DB) error {
			if req.QuotaBytes != nil {
				chain, err := repositories.StorageQuotaChain(tx, mailbox.ID, true)
				if err != nil {
					return err
				}
				if cap := repositories.StorageQuotaCeiling(chain[1:]); cap > 0 && *req.QuotaBytes > cap {
					return endpoint.Invalid(map[string]string{"quota_bytes": "must not exceed the owner's storage quota"})
				}
			}
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
		}
		err := h.db.Transaction(func(tx *gorm.DB) error {
			if err := change(tx); err != nil {
				return err
			}
			return reload(tx)
		})
		if err != nil {
			var refusal *endpoint.Error
			if errors.As(err, &refusal) {
				return Mailbox{}, err
			}
			return Mailbox{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to update mailbox")
		}
		if hashed != "" || req.Active != nil {
			h.sessions.Forget(mailbox.ID)
		}
	} else if err := reload(h.db); err != nil {
		return Mailbox{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to update mailbox")
	}

	redactMailboxDomain(r, &mailbox)
	return mailboxOf(mailbox), nil
}

// delete deletes an account. It always takes a session signed in with 2FA:
// an account and all its mail are gone at once, so a password alone, or any
// machine credential, never deletes one. Its credentials are revoked (the rows
// stay, for the audit trail) and its sessions end. Every grant it held is
// removed under the grants API's rules, since deleting an admin takes away
// what it held: the session must hold everything removed, and the last
// superadmin cannot go. Every grant others held on it (owners) is removed
// too, so a new account at the same address inherits nobody. Each removal has
// its audit entry, in the same transaction.
func (h *MailboxHandler) delete(r *http.Request, in adminAddressPath) (endpoint.Empty, error) {
	id := in.ID

	var mailbox models.Mailbox
	if err := h.db.WithContext(r.Context()).First(&mailbox, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		}
		return endpoint.Empty{}, fmt.Errorf("find mailbox for deletion: %w", err)
	}
	claims := middleware.GetClaims(r)
	if claims == nil || claims.CredentialID != 0 || !claims.HasAMR(auth.AMROTP) {
		return endpoint.Empty{}, mailboxGrantError(errTwoFactorRequired)
	}

	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var heldBy []models.Grant
		if err := tx.Preload("Role").Where("holder_kind = ? AND holder_id = ?", models.GrantHolderAccount, mailbox.ID).
			Order("id").Find(&heldBy).Error; err != nil {
			return err
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
			models.CredentialKindPassword, models.CredentialKindTOTP, models.CredentialKindRecoveryCode, models.CredentialKindAppPassword, models.CredentialKindAPIKey, models.CredentialKindClientSecret); err != nil {
			return err
		}
		if err := repositories.NewRefreshTokenRepository(tx).RevokeAllForAccount(mailbox.ID); err != nil {
			return err
		}
		return tx.Delete(&mailbox).Error
	})
	if err != nil {
		return endpoint.Empty{}, mailboxGrantError(err)
	}
	h.cache.Invalidate()
	h.sessions.Forget(mailbox.ID)
	return endpoint.Empty{}, nil
}

// endSessions signs an account out everywhere: every refresh token it holds is
// revoked and its credential epoch moves on, so each access token it was
// issued is refused at its next request, on the event stream and at refresh.
// Its passwords and app passwords stay: a mail client signed in with an app
// password keeps working, and is revoked on its own.
// DELETE /api/v1/admin/mailboxes/{id}/sessions
func (h *MailboxHandler) endSessions(r *http.Request, in adminAddressPath) (endpoint.Empty, error) {
	id := in.ID
	var mailbox models.Mailbox
	if err := h.db.WithContext(r.Context()).Select("id").First(&mailbox, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		}
		return endpoint.Empty{}, fmt.Errorf("find mailbox for session ending: %w", err)
	}
	if err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error { return endSessions(tx, mailbox.ID) }); err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to end the sessions")
	}
	h.sessions.Forget(mailbox.ID)
	return endpoint.Empty{}, nil
}

// AddressCheck is whether an address is a mailbox here, and which. Its fields are in the order
// the map it replaced was written in, so the bytes are unchanged.
type AddressCheck = apitypes.AddressCheck

// addressCheckQuery names the recipient to check. A blank one is the handler's 422.
type addressCheckQuery struct {
	Address string `query:"address,required" example:"admin@restmail.test"`
}

// RegisterAddressCheck mounts the recipient check the protocol gateways ask before they take a
// message: on the internal mTLS listener, or the public one where internal mTLS is off.
func (h *MailboxHandler) RegisterAddressCheck(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/mailboxes",
		OperationID: "checkMailboxAddress", Summary: "Check whether an address is a mailbox here (protocol gateways)", Tags: []string{"Mailboxes"},
		Description: "Machine to machine: no user token. An address that is not a mailbox here is 200 with exists " +
			"false: the gateway then routes it outbound.",
		Errors: []int{http.StatusUnprocessableEntity}, Security: oapifly.Public}, h.checkAddress)
}

// checkAddress verifies a recipient exists: GET /api/mailboxes?address=...
func (h *MailboxHandler) checkAddress(r *http.Request, in addressCheckQuery) (AddressCheck, error) {
	h = h.forRequest(r)
	address := in.Address
	if address == "" {
		return AddressCheck{}, endpoint.Invalid(map[string]string{"address": "address query parameter required"})
	}

	var mailbox models.Mailbox
	if err := repositories.FindReceivingMailboxByAddress(h.db, address, &mailbox); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// "Not local" is a valid answer to this query, not an error. The
			// SMTP gateway's apiclient (and its tests) expect 200 + exists=false
			// so authenticated submission can route external recipients to the
			// outbound queue — a 404 here surfaced as a client error and made
			// every external recipient temp-fail with 451 at RCPT.
			return AddressCheck{Address: address, Exists: false}, nil
		}
		return AddressCheck{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to check address")
	}
	return AddressCheck{Address: mailbox.Address, Exists: true, MailboxID: mailbox.ID}, nil
}
