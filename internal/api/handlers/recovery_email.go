package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RecoveryEmailHandler registers and verifies methods before lockout. A link
// resets only the password: no new session and no bypass of authenticator login.
type RecoveryEmailHandler struct {
	db       *gorm.DB
	auth     *AuthHandler
	baseURL  string
	requests *middleware.FailureBudget
}

func NewRecoveryEmailHandler(db *gorm.DB, auth *AuthHandler, baseURL string) *RecoveryEmailHandler {
	return &RecoveryEmailHandler{db: db, auth: auth, baseURL: strings.TrimRight(baseURL, "/"), requests: middleware.NewFailureBudget(middleware.RateLimitConfig{RPS: 1.0 / 900, Burst: 5, TTL: 2 * time.Hour})}
}

type registerRecoveryEmailRequest struct {
	Email           string `json:"email" format:"email" maxLength:"255"`
	CurrentPassword string `json:"current_password" minLength:"1"`
}
type recoveryEmailTokenRequest struct {
	Token string `json:"token" minLength:"1" maxLength:"128"`
}
type requestRecoveryEmailRequest struct {
	Email string `json:"email" format:"email" maxLength:"255" description:"The account address, never a recovery address supplied during lockout"`
}
type redeemRecoveryEmailRequest struct {
	Token       string `json:"token" minLength:"1" maxLength:"128"`
	NewPassword string `json:"new_password" minLength:"12" maxLength:"72"`
}
type recoveryEmailStatus struct {
	Email    string `json:"email"`
	Verified bool   `json:"verified"`
}

func (h *RecoveryEmailHandler) RegisterOwn(rs endpoint.Routes, checks SessionRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: "GET", Path: "/api/v1/auth/recovery-email", OperationID: "getRecoveryEmail", Summary: "Read your registered recovery email", Tags: []string{"Auth"}}, h.status)
	endpoint.Register(rs, endpoint.Operation{Method: "POST", Path: "/api/v1/auth/recovery-email", OperationID: "registerRecoveryEmail", Summary: "Register an external recovery email before lockout", Tags: []string{"Auth"},
		Description: "Requires a signed-in session and the current password. Replacing the address clears verification and invalidates previous email tokens. Verification lasts 15 minutes. Recovery address changes freeze for three days after email recovery.", Errors: []int{403, 503}, Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.recovery_email.register", middleware.AuditFailuresOnly())}}, h.register)
}
func (h *RecoveryEmailHandler) RegisterPublic(rs endpoint.Routes, checks SessionRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: "POST", Path: "/api/v1/auth/recovery-email/verify", OperationID: "verifyRecoveryEmail", Summary: "Verify a recovery email with its single-use link", Tags: []string{"Auth"}, Security: oapifly.Public, Errors: []int{401}, Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.recovery_email.verify", middleware.AuditFailuresOnly())}}, h.verify)
	endpoint.Register(rs, endpoint.Operation{Method: "POST", Path: "/api/v1/auth/recover/email", OperationID: "requestRecoveryEmail", Summary: "Request a password recovery link", Tags: []string{"Auth"}, Security: oapifly.Public,
		Description: "Supply the account address. The response is identical for unknown, disabled and unverified accounts. Every request uses the same per-address budget (five, then one per 15 minutes), in addition to the shared per-IP authentication throttle. Links expire after 15 minutes; requesting another invalidates the previous link.", Errors: []int{429, 503}, Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.recovery_email.issue", middleware.AuditFailuresOnly())}}, h.request)
	endpoint.Register(rs, endpoint.Operation{Method: "POST", Path: "/api/v1/auth/recover/email/redeem", OperationID: "redeemRecoveryEmail", Summary: "Choose a new password with a recovery email link", Tags: []string{"Auth"}, Security: oapifly.Public,
		Description: "Redeems a verified recovery email token once. Changes the password and ends every session and app password atomically. The authenticator and recovery codes remain; sign in normally with the new password and any required second factor. Does not sign in or enroll new authentication methods.", Errors: []int{401}, Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.recovery_email.redeem", middleware.AuditFailuresOnly())}}, h.redeem)
	endpoint.Register(rs, endpoint.Operation{Method: "GET", Path: "/api/v1/auth/recovery-email/link", OperationID: "recoveryEmailLink", Summary: "Open a private recovery email link", Tags: []string{"Auth"}, Security: oapifly.Public, MediaType: "text/html"}, h.linkPage)
}

func (h *RecoveryEmailHandler) status(r *http.Request, _ struct{}) (recoveryEmailStatus, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return recoveryEmailStatus{}, errAuthenticationRequired
	}
	var method models.RecoveryEmail
	err := h.db.WithContext(r.Context()).First(&method, "account_id = ?", claims.AccountID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return recoveryEmailStatus{}, nil
	}
	if err != nil {
		return recoveryEmailStatus{}, err
	}
	external, err := bootstrap.RecoveryEmailExternal(h.db.WithContext(r.Context()), method.Address)
	return recoveryEmailStatus{Email: method.Address, Verified: method.VerifiedAt != nil && external}, err
}

func (h *RecoveryEmailHandler) available() error {
	u, err := url.Parse(h.baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return endpoint.Refuse(503, "unavailable", "Email recovery requires an HTTPS RESTMAIL_RECOVERY_EMAIL_BASE_URL")
	}
	return nil
}

func normalizeRecoveryAddress(address string) (string, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	if err := rmail.ValidateAddress(address); err != nil {
		return "", err
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Address != address {
		return "", fmt.Errorf("a bare email address is required")
	}
	return address, nil
}

func emailRecoveryAudit(tx *gorm.DB, r *http.Request, action string, account *models.Mailbox) error {
	e := middleware.NewAuditEntry(action, r, time.Now())
	// Requesting delivery proves no identity. Verification and redemption do.
	if action != "auth.recovery_email.issue" {
		e.ActorKind = models.AuditActorAccount
		e.ActorID = &account.ID
		e.ActorName = account.Address
	}
	e.Target["account_id"] = strconv.FormatUint(uint64(account.ID), 10)
	return audit.Append(tx, e)
}

func (h *RecoveryEmailHandler) queue(tx *gorm.DB, account *models.Mailbox, address, token, action string, expires time.Time) error {
	if err := rmail.ValidateAddress(account.Address); err != nil {
		return err
	}
	if err := rmail.ValidateAddress(address); err != nil {
		return err
	}
	link := h.baseURL + "/api/v1/auth/recovery-email/link?mode=" + action + "#" + token
	subject := "Verify your REST Mail recovery email"
	if action == "recover" {
		subject = "Reset your REST Mail password"
	}
	messageID, err := newCSRFToken()
	if err != nil {
		return err
	}
	raw := "From: " + account.Address + "\r\nTo: " + address + "\r\nSubject: " + subject + "\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\nMessage-ID: <" + messageID + "@" + strings.Split(account.Address, "@")[1] + ">\r\nAuto-Submitted: auto-generated\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + subject + " using this private, single-use link (expires in 15 minutes):\r\n\r\n" + link + "\r\n\r\nIf you did not request this, ignore this message.\r\n"
	return tx.Create(&models.OutboundQueue{Sender: account.Address, Recipient: address, Domain: strings.Split(address, "@")[1], RawMessage: raw, Status: "pending", NextAttempt: time.Now(), ExpiresAt: expires}).Error
}

func (h *RecoveryEmailHandler) register(r *http.Request, req registerRecoveryEmailRequest) (passwordChanged, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return passwordChanged{}, errAuthenticationRequired
	}
	address, err := normalizeRecoveryAddress(req.Email)
	if err != nil {
		return passwordChanged{}, endpoint.Invalid(map[string]string{"email": "must be a bare external email address"})
	}
	if err := h.available(); err != nil {
		return passwordChanged{}, err
	}
	db := h.db.WithContext(r.Context())
	err = db.Transaction(func(tx *gorm.DB) error {
		// Lock before checking the password, so a concurrent reset cannot authorize
		// registering a method with the old password.
		var account models.Mailbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, claims.AccountID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errAuthenticationRequired
			}
			return err
		}
		if account.ID == 0 || !account.Active || account.BootstrapExpired(time.Now()) {
			return errAuthenticationRequired
		}
		cred, err := repositories.NewCredentialRepository(tx).MatchPassword(account.ID, req.CurrentPassword, time.Now())
		if err != nil {
			return err
		}
		if cred == nil {
			return endpoint.Refuse(403, "invalid_password", "Current password is incorrect")
		}
		external, err := bootstrap.RecoveryEmailExternal(tx, address)
		if err != nil {
			return err
		}
		if !external || strings.EqualFold(address, account.Address) {
			return endpoint.Invalid(map[string]string{"email": "must be outside this server's hosted domains"})
		}
		token, expires, err := bootstrap.RegisterRecoveryEmail(r.Context(), tx, account.ID, address)
		if err != nil {
			return err
		}
		if err := h.queue(tx, &account, address, token, "verify", expires); err != nil {
			return err
		}
		return emailRecoveryAudit(tx, r, "auth.recovery_email.register", &account)
	})
	if errors.Is(err, bootstrap.ErrRecoveryEmailFrozen) {
		err = endpoint.Refuse(403, "recovery_method_frozen", err.Error())
	}
	return passwordChanged{Message: "Verification link sent. The address cannot recover your account until verified."}, err
}

func (h *RecoveryEmailHandler) verify(r *http.Request, req recoveryEmailTokenRequest) (passwordChanged, error) {
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		account, _, err := bootstrap.RecoveryEmailTokenAccount(r.Context(), tx, req.Token, models.OneTimeTokenVerifyRecoveryEmail)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.RecoveryEmail{}).Where("account_id = ?", account.ID).Update("verified_at", time.Now()).Error; err != nil {
			return err
		}
		return emailRecoveryAudit(tx, r, "auth.recovery_email.verify", account)
	})
	if errors.Is(err, bootstrap.ErrTokenInvalid) {
		err = redeemRefusal(err)
	}
	return passwordChanged{Message: "Recovery email verified."}, err
}

func (h *RecoveryEmailHandler) request(r *http.Request, req requestRecoveryEmailRequest) (passwordChanged, error) {
	answer := passwordChanged{Message: "If this account has a verified recovery email, a recovery link will be sent."}
	address, err := normalizeRecoveryAddress(req.Email)
	if err != nil {
		return passwordChanged{}, endpoint.Invalid(map[string]string{"email": "must be a bare email address"})
	}
	// Charge known and unknown accounts identically, including successful sends.
	// Atomic reservation uses Charge's limiter directly to avoid concurrent
	// requests all passing Available before any has charged.
	if ok, retry := h.requests.Take(address); !ok {
		return passwordChanged{}, &endpoint.Error{Status: 429, Code: "rate_limited", Message: "Too many recovery requests for this address; try again later", Header: http.Header{"Retry-After": {strconv.Itoa(max(1, int(math.Ceil(retry.Seconds()))))}}}
	}
	if err := h.available(); err != nil {
		return passwordChanged{}, err
	}
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		account, destination, token, expires, err := bootstrap.MintRecoveryEmail(r.Context(), tx, address)
		if err != nil {
			return err
		}
		if err := h.queue(tx, account, destination, token, "recover", expires); err != nil {
			return err
		}
		return emailRecoveryAudit(tx, r, "auth.recovery_email.issue", account)
	})
	if errors.Is(err, bootstrap.ErrTokenInvalid) {
		return answer, nil
	}
	// Delivery/storage failure is private too: an account's existence must not
	// change this endpoint's response when the outbound queue fails.
	if err != nil {
		slog.Error("recovery email delivery failed", "error", err)
		return answer, nil
	}
	return answer, nil
}

func (h *RecoveryEmailHandler) redeem(r *http.Request, req redeemRecoveryEmailRequest) (passwordChanged, error) {
	if bootstrap.PasswordTooShort(req.NewPassword) || len(req.NewPassword) > 72 {
		return passwordChanged{}, endpoint.Invalid(map[string]string{"new_password": "must be at least 12 characters and at most 72 bytes"})
	}
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		return passwordChanged{}, err
	}
	var accountID uint
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		account, _, err := bootstrap.RecoveryEmailTokenAccount(r.Context(), tx, req.Token, models.OneTimeTokenRecoverEmail)
		if err != nil {
			return err
		}
		accountID = account.ID
		now := time.Now()
		if err := tx.Model(account).Updates(map[string]any{"last_password_change": now, "password_change_required": false, "bootstrap_expires_at": nil}).Error; err != nil {
			return err
		}
		if err := setAccountPassword(tx, r, account.ID, hash, &account.ID, now); err != nil {
			return err
		}
		if err := tx.Model(&models.RecoveryEmail{}).Where("account_id = ?", account.ID).Update("frozen_until", now.Add(72*time.Hour)).Error; err != nil {
			return err
		}
		// Notify the account's original mailbox; an existing signed-in device can
		// still see this notice after signing in again. No recovery secret included.
		raw := "From: " + account.Address + "\r\nTo: " + account.Address + "\r\nSubject: REST Mail password changed\r\nAuto-Submitted: auto-generated\r\n\r\nYour password was reset using your verified recovery email. Sign in with your new password and existing authenticator. Recovery email changes are frozen for three days. Contact your administrator if this was not you.\r\n"
		if err := tx.Create(&models.OutboundQueue{Sender: account.Address, Recipient: account.Address, Domain: strings.Split(account.Address, "@")[1], RawMessage: raw, Status: "pending", NextAttempt: now, ExpiresAt: now.Add(24 * time.Hour)}).Error; err != nil {
			return err
		}
		return emailRecoveryAudit(tx, r, "auth.recovery_email.redeem", account)
	})
	if errors.Is(err, bootstrap.ErrTokenInvalid) {
		err = redeemRefusal(err)
	}
	if err == nil {
		h.auth.sessions.Forget(accountID)
	}
	return passwordChanged{Message: "Password changed. Sign in again with the new password and your existing second factor."}, err
}
