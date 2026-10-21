package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// Escalation (#346): an admin switches one of its own eligible grants on for a
// while, with a code from its authenticator and a reason, and may switch it
// off before it runs out. That is how the owner goes from everyday admin to
// superadmin: a stolen phone or session gets the everyday rights, never god
// mode. Only the grant's holder can switch it on or off; anyone else is told
// there is no such grant. Each step, and its audit entry, commit together.

// maxReasonLength bounds the reason an activation records.
const maxReasonLength = 500

type activationResponse struct {
	ID          uint   `json:"id"`
	ActivatedAt string `json:"activated_at"`
	ExpiresAt   string `json:"expires_at"`
	Reason      string `json:"reason"`
	SecondsLeft int64  `json:"seconds_left"`
}

func toActivationResponse(a *models.GrantActivation, now time.Time) *activationResponse {
	left := int64(a.ExpiresAt.Sub(now) / time.Second)
	if left < 0 {
		left = 0
	}
	return &activationResponse{
		ID:          a.ID,
		ActivatedAt: a.ActivatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:   a.ExpiresAt.UTC().Format(time.RFC3339),
		Reason:      a.Reason,
		SecondsLeft: left,
	}
}

// escalationResponse is a grant after a step of escalation, and what the
// caller now holds at "*", for the client to show (as login lists it).
type escalationResponse struct {
	Grant        grantResponse `json:"grant"`
	Capabilities []string      `json:"capabilities"`
}

// Mine lists the caller's own grants, each with its mode and any activation
// running now, so a client can offer to switch an eligible one on.
// GET /api/v1/admin/grants/mine
func (h *GrantHandler) Mine(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	now := time.Now()
	// Activations that ran out are recorded when noticed: here as well as by
	// the sweeper.
	if _, err := grants.EndExpired(h.db, now); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch grants")
		return
	}
	var rows []models.Grant
	err := h.db.Preload("Role").
		Where("holder_kind = ? AND holder_id = ?", models.GrantHolderAccount, claims.AccountID).
		Order("id").Find(&rows).Error
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch grants")
		return
	}
	out, err := toGrantResponses(h.db, rows, now)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch grants")
		return
	}
	respond.Data(w, http.StatusOK, out)
}

type activateRequest struct {
	Reason string `json:"reason"`
	// Minutes is how long it stays on: 60 when omitted, at most 240.
	Minutes *int `json:"minutes"`
	// A fresh second factor: a current code from the authenticator, or an
	// unused recovery code. The session's own second factor is not enough.
	TOTPCode     string `json:"totp_code"`
	RecoveryCode string `json:"recovery_code"`
}

// Activate switches one of the caller's eligible grants on.
// POST /api/v1/admin/grants/{id}/activate {"reason":"…","minutes":60,"totp_code":"123456"}
func (h *GrantHandler) Activate(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id, ok := grantIDParam(w, r)
	if !ok {
		return
	}
	var req activateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	problems := map[string]string{}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		problems["reason"] = "required: why you are switching it on"
	} else if utf8.RuneCountInString(reason) > maxReasonLength {
		problems["reason"] = "at most 500 characters"
	}
	length := grants.DefaultActivation
	if req.Minutes != nil {
		if *req.Minutes < 1 || *req.Minutes > int(grants.MaxActivation/time.Minute) {
			problems["minutes"] = "must be from 1 to 240"
		} else {
			length = time.Duration(*req.Minutes) * time.Minute
		}
	}
	if len(problems) > 0 {
		respond.ValidationError(w, problems)
		return
	}

	var g models.Grant
	var a *models.GrantActivation
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := h.freshSecondFactor(tx, claims.AccountID, req.TOTPCode, req.RecoveryCode); err != nil {
			return err
		}
		if err := grants.Lock(tx); err != nil {
			return err
		}
		if err := ownGrant(tx, claims.AccountID, id, &g); err != nil {
			return err
		}
		var err error
		a, err = grants.Activate(tx, &g, length, reason, middleware.RequestEntry(r, grants.ActionActivate, http.StatusCreated))
		return err
	})
	if escalationFailed(w, err) {
		return
	}
	h.cache.Invalidate()
	h.respondEscalation(w, http.StatusCreated, claims.AccountID, &g, a)
}

// Deactivate switches one of the caller's grants off before its activation
// runs out. It needs no second factor: it only takes access away.
// POST /api/v1/admin/grants/{id}/deactivate
func (h *GrantHandler) Deactivate(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id, ok := grantIDParam(w, r)
	if !ok {
		return
	}
	var g models.Grant
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		if err := ownGrant(tx, claims.AccountID, id, &g); err != nil {
			return err
		}
		_, err := grants.Deactivate(tx, &g, middleware.RequestEntry(r, grants.ActionDeactivate, http.StatusOK))
		return err
	})
	if escalationFailed(w, err) {
		return
	}
	h.cache.Invalidate()
	h.respondEscalation(w, http.StatusOK, claims.AccountID, &g, nil)
}

type secondFactorRequest struct {
	TOTPCode     string `json:"totp_code"`
	RecoveryCode string `json:"recovery_code"`
}

// MakeEligible turns one of the caller's active grants eligible: from then on
// it gives nothing until switched on. This is how an admin who holds
// superadmin always (the owner of a server set up before escalation existed)
// chooses to hold it only when needed. Like switching on, it needs a fresh
// second factor, so that nobody makes a grant eligible without being able to
// switch it on again.
// POST /api/v1/admin/grants/{id}/make-eligible {"totp_code":"123456"}
func (h *GrantHandler) MakeEligible(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id, ok := grantIDParam(w, r)
	if !ok {
		return
	}
	var req secondFactorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	var g models.Grant
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := h.freshSecondFactor(tx, claims.AccountID, req.TOTPCode, req.RecoveryCode); err != nil {
			return err
		}
		if err := grants.Lock(tx); err != nil {
			return err
		}
		if err := ownGrant(tx, claims.AccountID, id, &g); err != nil {
			return err
		}
		return grants.MakeEligible(tx, &g, middleware.RequestEntry(r, grants.ActionMakeEligible, http.StatusOK))
	})
	if escalationFailed(w, err) {
		return
	}
	h.cache.Invalidate()
	h.respondEscalation(w, http.StatusOK, claims.AccountID, &g, nil)
}

func (h *GrantHandler) respondEscalation(w http.ResponseWriter, status int, accountID uint, g *models.Grant, a *models.GrantActivation) {
	now := time.Now()
	held, err := grants.Load(h.db, accountID, now)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to load user capabilities")
		return
	}
	resp := escalationResponse{Grant: toGrantResponse(g), Capabilities: held.Capabilities()}
	if resp.Capabilities == nil {
		resp.Capabilities = []string{}
	}
	if a != nil {
		resp.Grant.Activation = toActivationResponse(a, now)
	}
	respond.Data(w, status, resp)
}

var (
	errNoSecondFactor    = errors.New("no second factor enrolled")
	errFreshCodeRequired = errors.New("fresh second factor required")
	errFreshCodeInvalid  = errors.New("fresh second factor invalid")
)

// freshSecondFactor checks, in tx, a code the admin has just given: a current
// code from its authenticator, or an unused recovery code, which it uses up.
// In tx, so a step refused after the check leaves that recovery code unused.
func (h *GrantHandler) freshSecondFactor(tx *gorm.DB, accountID uint, totpCode, recoveryCode string) error {
	store := repositories.NewTwoFactorRepository(tx)
	_, err := store.GetActive(accountID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		return errNoSecondFactor
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(totpCode) == "" && strings.TrimSpace(recoveryCode) == "" {
		return errFreshCodeRequired
	}
	if !verifyTOTPOrRecovery(store, h.masterKey, accountID, totpCode, recoveryCode) {
		return errFreshCodeInvalid
	}
	return nil
}

// ownGrant reads, into g, the grant with id held by the admin accountID:
// errNoSuchGrant for one that does not exist or is anyone else's.
func ownGrant(tx *gorm.DB, accountID uint, id uint64, g *models.Grant) error {
	if err := tx.Preload("Role").First(g, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errNoSuchGrant
		}
		return err
	}
	if g.HolderKind != models.GrantHolderAccount || g.HolderID != accountID {
		return errNoSuchGrant
	}
	return nil
}

func grantIDParam(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_id", "Invalid grant ID")
		return 0, false
	}
	return id, true
}

// escalationFailed answers for a step of escalation that did not happen, and
// says whether it did not. A refused code is 403, not 401: the session is
// fine, and a client must not take it as signed out.
func escalationFailed(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNoSecondFactor):
		respond.Error(w, http.StatusForbidden, "two_factor_required",
			"Switching a grant on, or making it eligible, needs two-factor authentication: enroll an authenticator first")
	case errors.Is(err, errFreshCodeRequired):
		respond.Error(w, http.StatusForbidden, "totp_required",
			"A code from your authenticator (totp_code), or a recovery code (recovery_code), is required: the session's own sign-in is not enough")
	case errors.Is(err, errFreshCodeInvalid):
		respond.Error(w, http.StatusForbidden, "invalid_totp", "Invalid two-factor authentication code")
	case errors.Is(err, grants.ErrNotEligible):
		respond.Error(w, http.StatusConflict, "not_eligible", err.Error())
	case errors.Is(err, grants.ErrAlreadySwitchedOn):
		respond.Error(w, http.StatusConflict, "already_active", err.Error())
	case errors.Is(err, grants.ErrNotSwitchedOn):
		respond.Error(w, http.StatusConflict, "not_active", err.Error())
	case errors.Is(err, grants.ErrGrantExpired):
		respond.Error(w, http.StatusConflict, "grant_expired", err.Error())
	case errors.Is(err, grants.ErrAlreadyEligible):
		respond.Error(w, http.StatusConflict, "already_eligible", err.Error())
	default:
		return grantChangeFailed(w, err)
	}
	return true
}
