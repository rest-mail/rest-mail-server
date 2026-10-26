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
	apitypes "github.com/restmail/restmail/internal/api/types"
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

var invitationHeaders = map[string]oapifly.ResponseHeader{
	"Cache-Control":   {Description: "no-store: invitation secrets are never cacheable"},
	"Referrer-Policy": {Description: "no-referrer"},
}

var invitationSessionHeaders = map[string]oapifly.ResponseHeader{
	"Cache-Control":   invitationHeaders["Cache-Control"],
	"Referrer-Policy": invitationHeaders["Referrer-Policy"],
	"Set-Cookie":      sessionCookies["Set-Cookie"],
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
	return http.Header{"Cache-Control": {"no-store"}, "Referrer-Policy": {"no-referrer"}}
}

var errInvitationUnavailable = endpoint.Refuse(http.StatusServiceUnavailable, "unavailable", "Invitations require authenticator enrollment and server encryption to be enabled")

func (h *InvitationHandler) issue(r *http.Request, in adminAddressPath) (endpoint.Headed[invitationResponse], error) {
	var zero endpoint.Headed[invitationResponse]
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

type acceptInvitationRequest = apitypes.ResetRequest

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
	account, err := bootstrap.AcceptInvitation(r.Context(), h.db, req.Token, req.Password)
	if err != nil {
		return zero, redeemRefusal(err)
	}
	h.auth.sessions.Forget(account.ID)
	// Keep capability, enrollment and session-ledger reads on the HTTP context
	// as well. The password transaction has already committed at this point.
	sessionHandler := NewAuthHandler(h.db.WithContext(r.Context()), h.auth.jwtService, h.auth.masterKey, h.auth.sessions)
	session, err := sessionHandler.newSession(account, req.Client == clientNative, nil)
	if err != nil {
		return zero, err
	}
	for key, values := range invitationSecretHeaders() {
		session.Header[key] = values
	}
	return session, nil
}
