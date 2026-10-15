package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/bootstrap"
	"gorm.io/gorm"
)

// OwnerHandler lets whoever holds one of the server's owner keys ask for a
// reset-admin token over the public API, the way back in for an admin who has
// lost every other: the server issues a challenge, the owner signs it, the
// server checks the signature against RESTMAIL_OWNER_KEYS. Works the same
// however the server is deployed.
type OwnerHandler struct {
	db           *gorm.DB
	keys         *bootstrap.OwnerKeys
	challengeKey []byte
}

// NewOwnerHandler builds the handler from the configured keys and a server
// secret the challenge key is derived from.
func NewOwnerHandler(db *gorm.DB, keys *bootstrap.OwnerKeys, serverSecret string) *OwnerHandler {
	return &OwnerHandler{db: db, keys: keys, challengeKey: bootstrap.ChallengeKey(serverSecret)}
}

// Challenge issues a challenge to sign. Stateless: asking costs the server
// nothing but a hash.
func (h *OwnerHandler) Challenge(w http.ResponseWriter, _ *http.Request) {
	if h.keys.Len() == 0 {
		respond.Error(w, http.StatusConflict, "no_owner_keys", bootstrap.ErrNoOwnerKeys.Error())
		return
	}
	challenge, expires, err := bootstrap.NewChallenge(h.challengeKey, time.Now())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to issue a challenge")
		return
	}
	respond.Data(w, http.StatusOK, map[string]string{
		"challenge":  challenge,
		"expires_at": expires.UTC().Format(time.RFC3339),
	})
}

// ownerTokenRequest is the body of POST /api/v1/auth/owner/token.
type ownerTokenRequest struct {
	Challenge       string `json:"challenge"`
	PublicKey       string `json:"public_key"`
	Signature       string `json:"signature"` // base64 of the ssh wire-format signature
	Purpose         string `json:"purpose"`   // reset-admin, the only one
	Username        string `json:"username,omitempty"`
	LifetimeSeconds int    `json:"lifetime_seconds,omitempty"`
}

// Token checks a signed challenge and answers with a single-use reset-admin
// token: redeem it at /api/v1/auth/reset.
func (h *OwnerHandler) Token(w http.ResponseWriter, r *http.Request) {
	var req ownerTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Purpose != "reset-admin" {
		respond.ValidationError(w, map[string]string{"purpose": `must be "reset-admin"`})
		return
	}
	if req.Username == "" {
		respond.ValidationError(w, map[string]string{"username": "required for reset-admin"})
		return
	}
	lifetime := time.Duration(req.LifetimeSeconds) * time.Second
	if req.LifetimeSeconds < 0 || lifetime > 24*time.Hour {
		respond.ValidationError(w, map[string]string{"lifetime_seconds": "must be between 0 (the default, 15 minutes) and 86400"})
		return
	}
	sig, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		respond.ValidationError(w, map[string]string{"signature": "must be base64"})
		return
	}
	token, expires, err := bootstrap.MintForOwner(h.db, h.keys, h.challengeKey, bootstrap.OwnerRequest{
		Challenge: req.Challenge,
		PublicKey: req.PublicKey,
		Signature: sig,
		Purpose:   req.Purpose,
		Username:  req.Username,
		Lifetime:  lifetime,
	}, time.Now())
	switch {
	case errors.Is(err, bootstrap.ErrNoOwnerKeys):
		respond.Error(w, http.StatusConflict, "no_owner_keys", err.Error())
	case errors.Is(err, bootstrap.ErrChallengeInvalid):
		respond.Error(w, http.StatusUnauthorized, "invalid_challenge", err.Error())
	case errors.Is(err, bootstrap.ErrNotOwner):
		respond.Error(w, http.StatusForbidden, "not_owner", err.Error())
	case errors.Is(err, bootstrap.ErrNoSuchAdmin):
		respond.Error(w, http.StatusNotFound, "no_such_admin", err.Error())
	case err != nil:
		slog.Error("minting a token for an owner failed", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to mint the token")
	default:
		respond.Data(w, http.StatusOK, map[string]string{
			"purpose":    req.Purpose,
			"token":      token,
			"expires_at": expires.UTC().Format(time.RFC3339),
		})
	}
}
