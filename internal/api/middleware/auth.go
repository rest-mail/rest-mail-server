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

// AdminOnly restricts access to admin users.
// Checks either:
// - UserType == "admin" (for admin users authenticated via admin login)
// - IsAdmin == true (for mailbox users with admin flag - legacy)
func AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r)
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			return
		}
		// Allow access if user type is admin OR if legacy IsAdmin flag is set
		if claims.UserType != "admin" && !claims.IsAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "Admin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCapability restricts access to users with a specific capability.
// The wildcard "*" capability grants access to all endpoints.
func RequireCapability(capability string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
				return
			}

			// Admin users have capabilities, mailbox users don't
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
