package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/restmail/restmail/internal/auth"
)

type contextKey string

const ClaimsKey contextKey = "claims"

// JWTMiddleware validates the Authorization: Bearer <token> header.
func JWTMiddleware(jwtService *auth.JWTService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if header == "" {
				writeError(w, http.StatusUnauthorized, "unauthorized", "Missing authorization header")
				return
			}

			parts := strings.SplitN(header, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid authorization header format")
				return
			}

			claims, err := jwtService.ValidateAccessToken(parts[1])
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetClaims extracts JWT claims from the request context.
func GetClaims(r *http.Request) *auth.Claims {
	claims, ok := r.Context().Value(ClaimsKey).(*auth.Claims)
	if !ok {
		return nil
	}
	return claims
}

// AdminOnly restricts access to admin users, keyed SOLELY on UserType == "admin"
// (admin users authenticated via admin login). The deprecated mailbox IsAdmin
// claim is gone (OSI-14): a mailbox token — even a stale one still carrying an
// is_admin payload from before the fix — can no longer reach the admin surface,
// closing the latent self-escalation foot-gun.
func AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r)
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			return
		}
		if claims.UserType != "admin" {
			writeError(w, http.StatusForbidden, "forbidden", "Admin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCapability restricts access to users with a specific capability.
// The wildcard "*" capability grants access to all endpoints.
//
// Only admin tokens (UserType == "admin") are eligible; they are checked against
// the Capabilities claim issued at login. Every mailbox token is denied — the
// deprecated IsAdmin mailbox-admin path was removed (OSI-14), so a mailbox token
// can no longer be treated as a wildcard admin.
func RequireCapability(capability string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
				return
			}

			if claims.UserType != "admin" {
				writeError(w, http.StatusForbidden, "forbidden", "Admin access required")
				return
			}

			// Check if user has the required capability or wildcard
			hasCapability := false
			for _, cap := range claims.Capabilities {
				if cap == "*" || cap == capability {
					hasCapability = true
					break
				}
			}

			if !hasCapability {
				writeError(w, http.StatusForbidden, "forbidden", "Insufficient permissions")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
