package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/bootstrap"
	"gorm.io/gorm"
)

// InvitationHandler issues first-password invitations, separately from owner
// recovery. It never resets an existing account or changes its grants.
type InvitationHandler struct {
	db                  *gorm.DB
	auth                *AuthHandler
	enrollmentAvailable bool
}

func NewInvitationHandler(database *gorm.DB, authHandler *AuthHandler, enrollmentAvailable bool) *InvitationHandler {
	return &InvitationHandler{db: database, auth: authHandler, enrollmentAvailable: enrollmentAvailable}
}

type invitationResponse struct {
	Token     string    `json:"token" description:"Shown only once; share privately and place it in a URL fragment, never a query string"`
	ExpiresAt time.Time `json:"expires_at" format:"date-time"`
}

// Referrer-Policy comes from the security headers middleware, as on every
// other response.
var invitationHeaders = map[string]oapifly.ResponseHeader{
	"Cache-Control": {Description: "no-store: invitation secrets are never cacheable"},
}

var invitationSessionHeaders = map[string]oapifly.ResponseHeader{
	"Cache-Control": invitationHeaders["Cache-Control"],
	"Set-Cookie":    sessionCookies["Set-Cookie"],
}

// RegisterAdmin keeps invitation issuance behind the same live, scoped
// mailboxes:write authority that creates an account or changes its password.
func (h *InvitationHandler) RegisterAdmin(rs endpoint.Routes, checks MailboxRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/mailboxes/{id}/invitation",
		OperationID: "inviteAccount", Summary: "Invite an account to choose its first password", Tags: []string{"Admin: Mailboxes"},
		Status: http.StatusCreated, Headers: invitationHeaders, Errors: []int{http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable},
		Description: "For an active account created without credentials. The invitation lasts 15 minutes; issuing another invalidates the previous one. Only a hash is stored. Authenticator enrollment and server encryption must be enabled before invitations can be issued.",
		Middleware:  []endpoint.Middleware{checks.OnMailbox(middleware.CapMailboxesWrite)}}, h.issue)
}

// RegisterPublic redeems a secret, so it has sign-in's throttle and audit trail.
func (h *InvitationHandler) RegisterPublic(rs endpoint.Routes, checks SessionRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/invitation",
		OperationID: "acceptInvitation", Summary: "Choose your first password with an invitation", Tags: []string{"Auth"},
		Security: oapifly.Public, Headers: invitationSessionHeaders,
		Errors:      []int{http.StatusUnauthorized, http.StatusServiceUnavailable},
		Description: "Redeems an invitation once, chooses a password of 12–72 bytes and signs in. The session can only finish authenticator enrollment until it is confirmed. Owner reset tokens cannot be used here. Native clients send client: native; browsers receive httpOnly session cookies.",
		Middleware:  []endpoint.Middleware{checks.Throttle, checks.Audit("auth.invitation", middleware.AuditStartedSession())}}, h.accept)
}

func invitationSecretHeaders() http.Header {
	return http.Header{"Cache-Control": {"no-store"}}
}

var errInvitationUnavailable = endpoint.Refuse(http.StatusServiceUnavailable, "unavailable", "Invitations require authenticator enrollment and server encryption to be enabled")

func (h *InvitationHandler) issue(r *http.Request, in adminAddressPath) (endpoint.Headed[invitationResponse], error) {
	var zero endpoint.Headed[invitationResponse]
	if err := RequireFreshTwoFactor(r); err != nil {
		return zero, err
	}
	if !h.enrollmentAvailable {
		return zero, errInvitationUnavailable
	}
	token, expires, err := bootstrap.MintInvitation(r.Context(), h.db, uint(in.ID))
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return zero, endpoint.Refuse(http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
	case errors.Is(err, bootstrap.ErrAccountNotInvitable):
		return zero, endpoint.Refuse(http.StatusConflict, "account_not_invitable", err.Error())
	case err != nil:
		return zero, fmt.Errorf("issue an invitation: %w", err)
	}
	return endpoint.Headed[invitationResponse]{Header: invitationSecretHeaders(), Value: invitationResponse{Token: token, ExpiresAt: expires}}, nil
}

// acceptInvitationRequest redeems an invitation. It is the reset body's shape, with the
// constraints an invitation states that a schema can: a password is required, and client is
// "native" or left out. The password's 12 to 72 bytes, which a character count cannot state,
// and a missing token, which is refused like a bad one (401 invalid_token), stay with the
// handler.
type acceptInvitationRequest struct {
	Token    string `json:"token,omitempty"`
	Password string `json:"password" missing:"must be between 12 and 72 bytes"`
	Client   string `json:"client,omitempty" enums:"native" message:"must be \"native\" or omitted"`
}

func (h *InvitationHandler) accept(r *http.Request, req acceptInvitationRequest) (endpoint.Headed[loginResponse], error) {
	var zero endpoint.Headed[loginResponse]
	problems := map[string]string{}
	if req.Client != "" && req.Client != clientNative {
		problems["client"] = `must be "native" or omitted`
	}
	if len(req.Password) < bootstrap.MinChosenPasswordLength || len(req.Password) > 72 {
		problems["password"] = "must be between 12 and 72 bytes"
	}
	if len(problems) > 0 {
		return zero, endpoint.Invalid(problems)
	}
	if strings.TrimSpace(req.Token) == "" {
		return zero, redeemRefusal(bootstrap.ErrTokenInvalid)
	}
	if !h.enrollmentAvailable {
		return zero, errInvitationUnavailable
	}
	if err := r.Context().Err(); err != nil {
		return zero, redeemRefusal(err)
	}
	// Spending the invitation, installing the password and preparing its session
	// are one commit. A failed session must leave the invitation retryable.
	var session endpoint.Headed[loginResponse]
	var accountID uint
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		account, err := bootstrap.AcceptInvitation(r.Context(), tx, req.Token, req.Password)
		if err != nil {
			return redeemRefusal(err)
		}
		accountID = account.ID
		sessionHandler := NewAuthHandler(tx, h.auth.jwtService, h.auth.masterKey, h.auth.sessions)
		session, err = sessionHandler.newSession(account, req.Client == clientNative, nil)
		return err
	})
	if err != nil {
		return zero, err
	}
	h.auth.sessions.Forget(accountID)
	for key, values := range invitationSecretHeaders() {
		session.Header[key] = values
	}
	return session, nil
}
