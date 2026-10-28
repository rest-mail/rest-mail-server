package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

const ActionTokenIssue = "oauth.token_issue"

// OAuthHandler implements the RFC 6749 client_credentials exchange. There is
// no refresh token: the client authenticates again after five minutes.
type OAuthHandler struct {
	tokens *auth.JWTService
	now    func() time.Time
	verify func(context.Context, string, string, time.Time) (*models.Credential, *models.Mailbox, error)
	record func(context.Context, *models.AuditEntry) error
}

func NewOAuthHandler(db *gorm.DB, tokens *auth.JWTService) *OAuthHandler {
	return &OAuthHandler{tokens: tokens, now: time.Now,
		verify: func(ctx context.Context, id, secret string, now time.Time) (*models.Credential, *models.Mailbox, error) {
			return repositories.NewCredentialRepository(db.WithContext(ctx)).VerifyClientSecret(id, secret, now)
		},
		record: func(ctx context.Context, e *models.AuditEntry) error {
			return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if err := repositories.NewCredentialRepository(tx).TouchLastUsed(*e.CredentialID, e.OccurredAt); err != nil {
					return err
				}
				return audit.Append(tx, e)
			})
		},
	}
}

type oauthTokenRequest struct {
	Form string `body:"application/x-www-form-urlencoded"`
}
type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}
type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

func (h *OAuthHandler) Register(rs endpoint.Routes, checks SessionRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/oauth/token", OperationID: "issueClientCredentialsToken", Summary: "Exchange a client secret for a five-minute access token", Tags: []string{"Auth"}, Security: oapifly.Public,
		Description: "RFC 6749 client_credentials grant. Send grant_type=client_credentials and authenticate with the credential's public client_id and client_secret, either in this form or with HTTP Basic (never both). Returns a 300-second bearer access token, without a refresh token or human two-factor authentication. Permissions are selected at credential creation; a scope request is refused. Every use reloads the credential and live account grants, so revocation, expiry, account disablement and removed grants take effect on the next request. Shares the sign-in throttle. Successful issue is audited as oauth.token_issue with the actor and credential id. Responses are Cache-Control: no-store and Pragma: no-cache.",
		Errors:      []int{400, 401, 500}, Responses: []oapifly.EndpointResponse{
			{Status: 400, Type: oapifly.TypeOf[oauthError]()}, {Status: 401, Type: oapifly.TypeOf[oauthError]()}, {Status: 500, Type: oapifly.TypeOf[oauthError]()},
		}, Middleware: []endpoint.Middleware{checks.Throttle, oauthNoStore}}, h.token)
}

var oauthNoStore = endpoint.Middleware{Wrap: func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		_, _, basic := r.BasicAuth()
		next.ServeHTTP(oauthResponseWriter{ResponseWriter: w, basic: basic}, r)
	})
}}

type oauthResponseWriter struct {
	http.ResponseWriter
	basic bool
}

func (w oauthResponseWriter) WriteHeader(status int) {
	if status == http.StatusUnauthorized && w.basic {
		w.Header().Set("WWW-Authenticate", `Basic realm="restmail"`)
	}
	w.ResponseWriter.WriteHeader(status)
}

func oauthReply(status int, body any) endpoint.Forwarded[oauthTokenResponse] {
	data, _ := json.Marshal(body)
	return endpoint.Forwarded[oauthTokenResponse]{Status: status, Body: data}
}

func (h *OAuthHandler) token(r *http.Request, in oauthTokenRequest) (endpoint.Forwarded[oauthTokenResponse], error) {
	refuse := func(status int, code, description string) (endpoint.Forwarded[oauthTokenResponse], error) {
		return oauthReply(status, oauthError{code, description}), nil
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		return refuse(400, "invalid_request", "Use application/x-www-form-urlencoded")
	}
	form, err := url.ParseQuery(in.Form)
	if err != nil {
		return refuse(400, "invalid_request", "Malformed form")
	}
	for _, values := range form {
		if len(values) != 1 {
			return refuse(400, "invalid_request", "Repeated parameter")
		}
	}
	if form.Get("grant_type") == "" {
		return refuse(400, "invalid_request", "grant_type is required")
	}
	if form.Get("grant_type") != "client_credentials" {
		return refuse(400, "unsupported_grant_type", "Use client_credentials")
	}
	if form.Get("scope") != "" {
		return refuse(400, "invalid_scope", "Permissions are selected when creating the credential")
	}
	id, secret := form.Get("client_id"), form.Get("client_secret")
	if r.Header.Get("Authorization") != "" {
		basicID, basicSecret, ok := r.BasicAuth()
		if !ok || form.Has("client_id") || form.Has("client_secret") {
			return refuse(400, "invalid_request", "Use one client authentication method")
		}
		id, err = url.QueryUnescape(basicID)
		if err != nil {
			return refuse(400, "invalid_request", "Malformed client id")
		}
		secret, err = url.QueryUnescape(basicSecret)
		if err != nil {
			return refuse(400, "invalid_request", "Malformed client secret")
		}
	}
	if id == "" || secret == "" {
		return refuse(400, "invalid_request", "client_id and client_secret are required")
	}
	now := h.now()
	credential, account, err := h.verify(r.Context(), id, secret, now)
	var refusal *auth.CredentialRefusal
	if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, gorm.ErrRecordNotFound) || errors.As(err, &refusal) {
		return refuse(401, "invalid_client", "Invalid client credentials")
	}
	if err != nil {
		return refuse(500, "server_error", "Could not verify client credentials")
	}
	token, err := h.tokens.GenerateMachineToken(account.ID, account.Address, credential.ID)
	if err != nil {
		return refuse(500, "server_error", "Could not issue access token")
	}
	claims := &auth.Claims{AccountID: account.ID, Address: account.Address, CredentialID: credential.ID, CredentialKind: models.CredentialKindClientSecret}
	authenticated := r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, claims))
	entry := middleware.NewAuditEntry(ActionTokenIssue, authenticated, now)
	entry.Target["credential_id"] = strconv.FormatUint(uint64(credential.ID), 10)
	entry.Target["public_id"] = credential.PublicID
	if err := h.record(r.Context(), entry); err != nil {
		return refuse(500, "server_error", "Could not record token issue")
	}
	return oauthReply(200, oauthTokenResponse{token, "Bearer", 300}), nil
}
