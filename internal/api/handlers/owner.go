package handlers

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/version"
	"gorm.io/gorm"
)

// OwnerHandler lets whoever holds one of the server's owner keys ask for a
// reset-admin token over the public API, the way back in for an admin who has
// lost every other: the server issues a challenge, the owner signs it, the
// server checks the signature against its PostgreSQL owner registry. Works the same
// however the server is deployed.
//
// The same challenge, signed for the purpose "status" by a key listed in
// RESTMAIL_STATUS_KEYS or by an owner key, reads which version the server
// runs.
type OwnerHandler struct {
	db           *gorm.DB
	statusKeys   *bootstrap.StatusKeys
	challengeKey []byte
}

// NewOwnerHandler uses the database's current owners, the configured read-only
// status keys, and a server secret the challenge key is derived from.
func NewOwnerHandler(db *gorm.DB, statusKeys *bootstrap.StatusKeys, serverSecret string) *OwnerHandler {
	return &OwnerHandler{db: db, statusKeys: statusKeys, challengeKey: bootstrap.ChallengeKey(serverSecret)}
}

// Register mounts the owner endpoints behind throttle, a bucket of their own at login's rate:
// the CLI's requests must not use up the budget the owner then needs to sign in. A token
// minted and a status read are recorded with audit.
func (h *OwnerHandler) Register(rs endpoint.Routes, throttle endpoint.Middleware, audit func(string, ...middleware.AuditOption) endpoint.Middleware) {
	tags := []string{"Auth"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/owner/challenge",
		OperationID: "ownerChallenge", Summary: "A challenge for an owner key to sign", Tags: tags,
		Description: "The first half of proving ownership with a key in the owner registry (restmail reset-admin), or of " +
			"reading the status with a key in RESTMAIL_STATUS_KEYS (restmail status --key). Stateless, valid two " +
			"minutes, answered once. 409 no_owner_keys when the server lists neither.",
		Security: oapifly.Public, Errors: []int{http.StatusConflict},
		Middleware: []endpoint.Middleware{throttle}}, h.challenge)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/owner/token",
		OperationID: "ownerToken", Summary: "Trade a signed challenge for a reset-admin token", Tags: tags,
		Description: "signature is the SSH wire-format signature (base64), by the private half of public_key, over " +
			`"restmail-owner-v1\n" + purpose + "\n" + address + "\n" + challenge. The key must be in the owner registry; ` +
			"a status key is refused. The token is redeemed at /api/v1/auth/reset.",
		Security:   oapifly.Public,
		Errors:     []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{throttle, audit("auth.owner_token", middleware.AuditSignIn())}}, h.token)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/owner/status",
		OperationID: "ownerStatus", Summary: "Trade a signed challenge for what the server runs", Tags: tags,
		Description: "How a deploy is confirmed from outside (restmail status --key): the owner scheme with the purpose " +
			"status and no address, by a key in RESTMAIL_STATUS_KEYS or the owner registry. The challenge is spent.",
		Security:   oapifly.Public,
		Errors:     []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict},
		Middleware: []endpoint.Middleware{throttle, audit("auth.owner_status")}}, h.status)
}

func (h *OwnerHandler) ownerKeys(r *http.Request) (*bootstrap.OwnerKeys, error) {
	keys, err := bootstrap.LoadOwnerKeys(h.db.WithContext(r.Context()))
	if err != nil {
		slog.Error("reading owner key registry failed", "error", err)
		return nil, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to read owner key registry")
	}
	return keys, nil
}

// challenge checks the current registry and issues a challenge to sign. Issuing the challenge
// stores nothing; only a verified answer spends it.
func (h *OwnerHandler) challenge(r *http.Request, _ endpoint.Empty) (apitypes.OwnerChallenge, error) {
	keys, err := h.ownerKeys(r)
	if err != nil {
		return apitypes.OwnerChallenge{}, err
	}
	if keys.Len() == 0 && h.statusKeys.Len() == 0 {
		// no_owner_keys still, though status keys would do: it is the code a
		// reset-admin from before status keys knows.
		return apitypes.OwnerChallenge{}, endpoint.Refuse(http.StatusConflict, "no_owner_keys", bootstrap.ErrNoStatusKeys.Error())
	}
	challenge, expires, err := bootstrap.NewChallenge(h.challengeKey, time.Now())
	if err != nil {
		return apitypes.OwnerChallenge{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to issue a challenge")
	}
	return apitypes.OwnerChallenge{Challenge: challenge, ExpiresAt: wholeSecondUTC(expires)}, nil
}

// wholeSecondUTC is t as the answers write it: UTC, to the second.
func wholeSecondUTC(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// ownerTokenRequest is the body of POST /api/v1/auth/owner/token. Every field is checked in
// the handler or as the signed message, with the refusals it always gave.
type ownerTokenRequest = apitypes.OwnerTokenRequest

// OwnerToken is a single-use token for an owner's purpose, reset-admin, and when it expires.
// Its fields are in the order the map it replaced was written in, so the bytes are unchanged.
type OwnerToken = apitypes.OwnerToken

// token checks a signed challenge and answers with a single-use reset-admin token: redeem it
// at /api/v1/auth/reset.
func (h *OwnerHandler) token(r *http.Request, req ownerTokenRequest) (OwnerToken, error) {
	if req.Purpose != "reset-admin" {
		return OwnerToken{}, endpoint.Invalid(map[string]string{"purpose": `must be "reset-admin"`})
	}
	if req.Address == "" {
		return OwnerToken{}, endpoint.Invalid(map[string]string{"address": "required for reset-admin: the account to reset"})
	}
	lifetime := time.Duration(req.LifetimeSeconds) * time.Second
	if req.LifetimeSeconds < 0 || lifetime > 24*time.Hour {
		return OwnerToken{}, endpoint.Invalid(map[string]string{"lifetime_seconds": "must be between 0 (the default, 15 minutes) and 86400"})
	}
	sig, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		return OwnerToken{}, endpoint.Invalid(map[string]string{"signature": "must be base64"})
	}
	keys, err := h.ownerKeys(r)
	if err != nil {
		return OwnerToken{}, err
	}
	token, expires, err := bootstrap.MintForOwner(h.db.WithContext(r.Context()), keys, h.challengeKey, bootstrap.OwnerRequest{
		Challenge: req.Challenge,
		PublicKey: req.PublicKey,
		Signature: sig,
		Purpose:   req.Purpose,
		Address:   req.Address,
		Lifetime:  lifetime,
	}, time.Now())
	switch {
	case errors.Is(err, bootstrap.ErrNoOwnerKeys):
		return OwnerToken{}, endpoint.Refuse(http.StatusConflict, "no_owner_keys", err.Error())
	case errors.Is(err, bootstrap.ErrChallengeInvalid):
		return OwnerToken{}, endpoint.Refuse(http.StatusUnauthorized, "invalid_challenge", err.Error())
	case errors.Is(err, bootstrap.ErrNotOwner):
		return OwnerToken{}, endpoint.Refuse(http.StatusForbidden, "not_owner", err.Error())
	case errors.Is(err, bootstrap.ErrNoSuchAdmin):
		return OwnerToken{}, endpoint.Refuse(http.StatusNotFound, "no_such_account", err.Error())
	case err != nil:
		slog.Error("minting a token for an owner failed", "error", err)
		return OwnerToken{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to mint the token")
	}
	return OwnerToken{ExpiresAt: wholeSecondUTC(expires), Purpose: req.Purpose, Token: token}, nil
}

// status checks a challenge signed for "status" by a status key or an owner key, and answers
// with what the server runs and since when: how a deploy is confirmed from outside.
func (h *OwnerHandler) status(r *http.Request, req apitypes.OwnerStatusRequest) (apitypes.ServerStatus, error) {
	sig, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		return apitypes.ServerStatus{}, endpoint.Invalid(map[string]string{"signature": "must be base64"})
	}
	keys, err := h.ownerKeys(r)
	if err != nil {
		return apitypes.ServerStatus{}, err
	}
	err = bootstrap.CheckStatus(h.db.WithContext(r.Context()), h.statusKeys, keys, h.challengeKey, bootstrap.StatusRequest{
		Challenge: req.Challenge,
		PublicKey: req.PublicKey,
		Signature: sig,
	}, time.Now())
	switch {
	case errors.Is(err, bootstrap.ErrNoStatusKeys):
		return apitypes.ServerStatus{}, endpoint.Refuse(http.StatusConflict, "no_status_keys", err.Error())
	case errors.Is(err, bootstrap.ErrChallengeInvalid):
		return apitypes.ServerStatus{}, endpoint.Refuse(http.StatusUnauthorized, "invalid_challenge", err.Error())
	case errors.Is(err, bootstrap.ErrNotStatusKey):
		return apitypes.ServerStatus{}, endpoint.Refuse(http.StatusForbidden, "not_status_key", err.Error())
	case err != nil:
		slog.Error("checking a status request failed", "error", err)
		return apitypes.ServerStatus{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to check the request")
	}
	return apitypes.ServerStatus{
		Version:   version.Version,
		Commit:    version.Commit,
		BuiltAt:   version.BuildDate,
		StartedAt: wholeSecondUTC(version.Started),
	}, nil
}
