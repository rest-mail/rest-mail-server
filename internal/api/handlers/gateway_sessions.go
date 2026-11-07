package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

// GatewaySessionOpened is the session a gateway opened for a mail client's connection.
type GatewaySessionOpened = apitypes.GatewaySessionOpened

// gatewayServiceProtocols is the protocol each gateway service signs mail
// clients in over. The SMTP delivery service acts for no account, so it opens
// no sessions.
var gatewayServiceProtocols = map[string]string{
	models.GatewayServiceIMAP:           "imap",
	models.GatewayServicePOP3:           "pop3",
	models.GatewayServiceSMTPSubmission: "smtp",
}

// GatewaySessionHandler opens and closes the protocol gateways' connection
// sessions (#1586), on the internal listener.
type GatewaySessionHandler struct {
	db       *gorm.DB
	services middleware.GatewayServices
	now      func() time.Time
}

func NewGatewaySessionHandler(db *gorm.DB, services middleware.GatewayServices) *GatewaySessionHandler {
	return &GatewaySessionHandler{db: db, services: services, now: time.Now}
}

// GatewaySessionRouteChecks are the open route's checks: the mail client's
// address and password, sent once in the body (middleware.SessionPasswords:
// refusals recorded and throttled), login's throttle, and the audit.
type GatewaySessionRouteChecks struct {
	Credential endpoint.Middleware
	Throttle   endpoint.Middleware
	Audit      func(action string, options ...middleware.AuditOption) endpoint.Middleware
}

// Register registers the open and close routes. They are internal: a gateway
// reaches them only with its own mTLS client certificate, which names the
// service (middleware.GatewayServices); any other caller is refused.
func (h *GatewaySessionHandler) Register(rs endpoint.Routes, checks GatewaySessionRouteChecks) {
	tags := []string{"Auth"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/gateway-sessions",
		OperationID: "openGatewaySession", Summary: "Open a mail client's connection session (protocol gateways)", Tags: tags,
		Description: "Internal: served on the mTLS listener, for a gateway presenting its own client certificate, which names its service " +
			"(IMAP, POP3 or SMTP submission). The mail client's address and password or app password come once, in the body: " +
			"this is the only request that takes a mail client's password. A refused one is recorded as auth.credential_refused and " +
			"refusals are throttled; an account with two-factor authentication needs an app password. The answer's token is sent as Bearer on every call the gateway then " +
			"makes for that connection, over the internal listener with the same certificate; the session is checked on every " +
			"request, so revoking the credential or the account's sessions ends it at the next one. protocol must be the one the " +
			"service serves; a caller without a gateway certificate is 403 gateway_required.",
		Status:   http.StatusCreated,
		Security: oapifly.Public, Errors: []int{http.StatusForbidden},
		Middleware: []endpoint.Middleware{checks.Credential, checks.Throttle,
			checks.Audit("auth.gateway_session_open", middleware.AuditBodyFields("protocol"))}}, h.open)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/gateway-sessions/current",
		OperationID: "closeGatewaySession", Summary: "Close a mail client's connection session (protocol gateways)", Tags: tags,
		Description: "Internal: the gateway that opened the session closes it at logout or disconnect, sending its token as Bearer " +
			"with the same client certificate. Closing one already closed is not an error. A caller without a gateway certificate " +
			"is 403 gateway_required.",
		Status: http.StatusNoContent, Security: []oapifly.SecurityRequirement{{"GatewaySessionAuth": nil}},
		Errors: []int{http.StatusBadRequest, http.StatusForbidden}}, h.close)
}

func errGatewayRequired() error {
	return endpoint.Refuse(http.StatusForbidden, "gateway_required", "Only a protocol gateway, with its own client certificate, may do this")
}

// POST /api/v1/gateway-sessions
func (h *GatewaySessionHandler) open(r *http.Request, req apitypes.GatewaySessionOpenRequest) (GatewaySessionOpened, error) {
	service := h.services.Service(r)
	protocol, ok := gatewayServiceProtocols[service]
	if !ok {
		return GatewaySessionOpened{}, errGatewayRequired()
	}
	claims := middleware.GetClaims(r)
	if claims == nil || claims.CredentialID == 0 || claims.IsScopedCredential() || claims.GatewaySessionID != 0 {
		return GatewaySessionOpened{}, endpoint.Refuse(http.StatusBadRequest, "credential_required",
			"Send the account's address, and its password or an app password")
	}
	if req.Protocol != protocol {
		return GatewaySessionOpened{}, endpoint.Invalid(map[string]string{"protocol": `must be "` + protocol + `", the protocol this gateway serves`})
	}
	db := h.db.WithContext(r.Context())
	var account models.Mailbox
	if err := db.Select("id", "address", "display_name").Take(&account, claims.AccountID).Error; err != nil {
		return GatewaySessionOpened{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to load the account")
	}
	// A mail client reaches only the account's own mailbox: being an
	// administrator never stands in for mailbox:session (#324).
	held, err := grants.Load(db, account.ID, h.now())
	if err != nil {
		return GatewaySessionOpened{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to load the account's capabilities")
	}
	if !held.MailboxSession(account.Address) {
		return GatewaySessionOpened{}, errAccessRestricted()
	}
	_, token, err := repositories.NewGatewaySessionRepository(db).Open(account.ID, claims.CredentialID, claims.CredentialKind, service, protocol, h.now())
	if err != nil {
		return GatewaySessionOpened{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to open the session")
	}
	return GatewaySessionOpened{
		Token: token, Protocol: protocol,
		CredentialID: claims.CredentialID, CredentialKind: claims.CredentialKind,
		User: userInfo{ID: account.ID, Email: account.Address, DisplayName: account.DisplayName},
	}, nil
}

// DELETE /api/v1/gateway-sessions/current
func (h *GatewaySessionHandler) close(r *http.Request, _ struct{}) (endpoint.Empty, error) {
	service := h.services.Service(r)
	if service == "" {
		return endpoint.Empty{}, errGatewayRequired()
	}
	token, ok := middleware.GatewaySessionToken(r, repositories.GatewaySessionTokenPrefix)
	if !ok {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusBadRequest, "session_required", "Send the session's token as Bearer")
	}
	if err := repositories.NewGatewaySessionRepository(h.db.WithContext(r.Context())).Close(token, service, h.now()); err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to close the session")
	}
	return endpoint.Empty{}, nil
}

// GatewaySessionVerifier checks a gateway session token on every request for
// middleware.GatewaySessions, and builds the claims the request runs with: the
// account's, with the session's credential, so the session reaches no more than the credential
// does (an app password only its own mailbox, #344), and never the admin
// routes, which take no gateway session.
func GatewaySessionVerifier(db *gorm.DB) middleware.GatewaySessionUse {
	return func(token, service string) (*auth.Claims, bool, error) {
		s, err := repositories.NewGatewaySessionRepository(db).Use(token, service, time.Now())
		if errors.Is(err, repositories.ErrGatewaySessionEnded) {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, err
		}
		var account models.Mailbox
		if err := db.Select("id", "address").Take(&account, s.AccountID).Error; err != nil {
			return nil, false, err
		}
		claims := &auth.Claims{
			AccountID:        account.ID,
			Address:          account.Address,
			TokenType:        "access",
			CredentialID:     s.CredentialID,
			CredentialKind:   s.CredentialKind,
			GatewaySessionID: s.ID,
			GatewayService:   s.Service,
			GatewayProtocol:  s.Protocol,
		}
		claims.Subject = "account:" + strconv.FormatUint(uint64(account.ID), 10)
		return claims, false, nil
	}
}
