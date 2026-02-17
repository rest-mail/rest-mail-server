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

			claims, err := jwtService.ValidateToken(parts[1])
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
// Checks the is_admin flag on the JWT claims (set from WebmailAccount.IsAdmin).
// In development mode, all authenticated users are allowed through.
func AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r)
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			return
		}
		if !claims.IsAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "Admin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
