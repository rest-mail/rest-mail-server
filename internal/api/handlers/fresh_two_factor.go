package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
)

const MaxFreshTwoFactorWindow = 15 * time.Minute

type twoFactorWindowKey struct{}

// TwoFactorWindow supplies the operator's shortened sudo window to handlers.
// Zero selects the default. Invalid programmatic configuration fails closed.
func TwoFactorWindow(window time.Duration) func(http.Handler) http.Handler {
	if window == 0 {
		window = MaxFreshTwoFactorWindow
	}
	if window < 0 || window > MaxFreshTwoFactorWindow {
		panic("invalid fresh two-factor window")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), twoFactorWindowKey{}, window)))
		})
	}
}

func humanTwoFactor(c *auth.Claims) bool {
	return c != nil && c.CredentialID == 0 && c.CredentialKind == "" && (c.TwoFactor || c.HasAMR(auth.AMROTP))
}

// RequireTwoFactor refuses every machine credential and any session that did
// not prove a second factor at sign-in, using the existing grants refusal.
func RequireTwoFactor(r *http.Request) error {
	if humanTwoFactor(middleware.GetClaims(r)) {
		return nil
	}
	return grantChangeError(errTwoFactorRequired)
}

// RequireFreshTwoFactor additionally checks the original proof time, never iat:
// refreshing a session cannot reopen its sudo window. Missing legacy proof and
// future timestamps require another sign-in too.
func RequireFreshTwoFactor(r *http.Request) error {
	if err := RequireTwoFactor(r); err != nil {
		return err
	}
	window, ok := r.Context().Value(twoFactorWindowKey{}).(time.Duration)
	if !ok {
		window = MaxFreshTwoFactorWindow
	}
	at := middleware.GetClaims(r).AuthTime
	now := time.Now()
	if at == nil || at.After(now) || now.Sub(at.Time) > window {
		return endpoint.Refuse(http.StatusForbidden, "recent_two_factor_required", "Sign in again with two-factor authentication: a recent second factor is required")
	}
	return nil
}

// Non-mail capabilities are administrative. Mailbox management can create
// credentials, and read/sending delegation also needs recent human proof.
func freshGrantCapability(capability string) bool {
	switch capability {
	case "mail:write", "mail:receive":
		return false
	default:
		return true
	}
}

func isEndpointError(err error) bool {
	var refusal *endpoint.Error
	return errors.As(err, &refusal)
}

// A non-positive quota means unlimited; lifting a finite cap needs 2FA.
func quotaRaised(before, after int64) bool {
	return before > 0 && (after <= 0 || after > before)
}
