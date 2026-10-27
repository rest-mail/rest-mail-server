package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
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
	ActivatedAt string `json:"activated_at" format:"date-time"`
	ExpiresAt   string `json:"expires_at" format:"date-time"`
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
	Grant              grantResponse  `json:"grant"`
	Capabilities       []string       `json:"capabilities"`
	ScopedCapabilities []grants.Entry `json:"scoped_capabilities"`
}

// mine lists the caller's own grants, each with its mode and any activation
// running now, so a client can offer to switch an eligible one on.
// GET /api/v1/admin/grants/mine
func (h *GrantHandler) mine(r *http.Request, _ struct{}) ([]grantResponse, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	now := time.Now()
	// Activations that ran out are recorded when noticed: here as well as by
	// the sweeper.
	if _, err := grants.EndExpired(h.db, now); err != nil {
		return nil, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to fetch grants")
	}
	var rows []models.Grant
	err := h.db.Preload("Role").
		Where("holder_kind = ? AND holder_id = ?", models.GrantHolderAccount, claims.AccountID).
		Order("id").Find(&rows).Error
	if err != nil {
		return nil, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to fetch grants")
	}
	out, err := toGrantResponses(h.db, rows, now)
	if err != nil {
		return nil, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to fetch grants")
	}
	return out, nil
}

type activateRequest struct {
	Reason string `json:"reason,omitempty"`
	// Minutes is how long it stays on: 60 when omitted, at most 240.
	Minutes *int `json:"minutes,omitempty"`
	// A fresh second factor: a current code from the authenticator, or an
	// unused recovery code. The session's own second factor is not enough.
	TOTPCode     string `json:"totp_code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

// activate switches one of the caller's eligible grants on.
// POST /api/v1/admin/grants/{id}/activate {"reason":"…","minutes":60,"totp_code":"123456"}
func (h *GrantHandler) activate(r *http.Request, in activateGrantRequest) (escalationResponse, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	req := in.Body
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
		return escalationResponse{}, endpoint.Invalid(problems)
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
		if err := ownGrant(tx, claims.AccountID, uint64(in.ID), &g); err != nil {
			return err
		}
		var err error
		a, err = grants.Activate(tx, &g, length, reason, middleware.RequestEntry(r, grants.ActionActivate, http.StatusCreated))
		return err
	})
	if err != nil {
		return escalationResponse{}, escalationError(err)
	}
	h.cache.Invalidate()
	return h.escalation(claims.AccountID, &g, a)
}

// deactivate switches one of the caller's grants off before its activation
// runs out. It needs no second factor: it only takes access away.
// POST /api/v1/admin/grants/{id}/deactivate
func (h *GrantHandler) deactivate(r *http.Request, in grantPath) (escalationResponse, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	var g models.Grant
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		if err := ownGrant(tx, claims.AccountID, uint64(in.ID), &g); err != nil {
			return err
		}
		_, err := grants.Deactivate(tx, &g, middleware.RequestEntry(r, grants.ActionDeactivate, http.StatusOK))
		return err
	})
	if err != nil {
		return escalationResponse{}, escalationError(err)
	}
	h.cache.Invalidate()
	return h.escalation(claims.AccountID, &g, nil)
}

type secondFactorRequest struct {
	TOTPCode     string `json:"totp_code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

// makeEligible turns one of the caller's active grants eligible: from then on
// it gives nothing until switched on. This is how an admin who holds
// superadmin always (the owner of a server set up before escalation existed)
// chooses to hold it only when needed. Like switching on, it needs a fresh
// second factor, so that nobody makes a grant eligible without being able to
// switch it on again.
// POST /api/v1/admin/grants/{id}/make-eligible {"totp_code":"123456"}
func (h *GrantHandler) makeEligible(r *http.Request, in eligibleGrantRequest) (escalationResponse, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	req := in.Body
	var g models.Grant
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := h.freshSecondFactor(tx, claims.AccountID, req.TOTPCode, req.RecoveryCode); err != nil {
			return err
		}
		if err := grants.Lock(tx); err != nil {
			return err
		}
		if err := ownGrant(tx, claims.AccountID, uint64(in.ID), &g); err != nil {
			return err
		}
		return grants.MakeEligible(tx, &g, middleware.RequestEntry(r, grants.ActionMakeEligible, http.StatusOK))
	})
	if err != nil {
		return escalationResponse{}, escalationError(err)
	}
	h.cache.Invalidate()
	return h.escalation(claims.AccountID, &g, nil)
}

func (h *GrantHandler) escalation(accountID uint, g *models.Grant, a *models.GrantActivation) (escalationResponse, error) {
	now := time.Now()
	held, err := grants.Load(h.db, accountID, now)
	if err != nil {
		return escalationResponse{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to load user capabilities")
	}
	resp := escalationResponse{Grant: toGrantResponse(g), Capabilities: held.Capabilities(), ScopedCapabilities: held.ScopedCapabilities()}
	if resp.Capabilities == nil {
		resp.Capabilities = []string{}
	}
	if a != nil {
		resp.Grant.Activation = toActivationResponse(a, now)
	}
	return resp, nil
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
	verified, err := verifyTOTPOrRecovery(store, h.masterKey, accountID, totpCode, recoveryCode)
	if err != nil {
		return err
	}
	if !verified {
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

// escalationError is the refusal for a step of escalation that did not happen.
// A refused code is 403, not 401: the session is fine, and a client must not
// take it as signed out.
func escalationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNoSecondFactor):
		return endpoint.Refuse(http.StatusForbidden, "two_factor_required",
			"Switching a grant on, or making it eligible, needs two-factor authentication: enroll an authenticator first")
	case errors.Is(err, errFreshCodeRequired):
		return endpoint.Refuse(http.StatusForbidden, "totp_required",
			"A code from your authenticator (totp_code), or a recovery code (recovery_code), is required: the session's own sign-in is not enough")
	case errors.Is(err, errFreshCodeInvalid):
		return endpoint.Refuse(http.StatusForbidden, "invalid_totp", "Invalid two-factor authentication code")
	case errors.Is(err, grants.ErrNotEligible):
		return endpoint.Refuse(http.StatusConflict, "not_eligible", err.Error())
	case errors.Is(err, grants.ErrAlreadySwitchedOn):
		return endpoint.Refuse(http.StatusConflict, "already_active", err.Error())
	case errors.Is(err, grants.ErrNotSwitchedOn):
		return endpoint.Refuse(http.StatusConflict, "not_active", err.Error())
	case errors.Is(err, grants.ErrGrantExpired):
		return endpoint.Refuse(http.StatusConflict, "grant_expired", err.Error())
	case errors.Is(err, grants.ErrAlreadyEligible):
		return endpoint.Refuse(http.StatusConflict, "already_eligible", err.Error())
	default:
		return grantChangeError(err)
	}
}
