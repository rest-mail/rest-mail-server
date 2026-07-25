package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type HealthHandler struct {
	db  *gorm.DB
	jwt *auth.JWTService
}

func NewHealthHandler(db *gorm.DB, jwtService *auth.JWTService) *HealthHandler {
	return &HealthHandler{db: db, jwt: jwtService}
}

func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	sqlDB, err := h.db.DB()
	if err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "unhealthy", "Database connection unavailable")
		return
	}
	if err := sqlDB.Ping(); err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "unhealthy", "Database ping failed")
		return
	}

	result := map[string]any{
		"status": "healthy",
		"db":     "connected",
	}

	// The certificate inventory (issuer, hosted domain, expiry) is operational
	// detail that must not be enumerable by unauthenticated callers. Disclose it
	// only to an authenticated admin; everyone else gets a minimal liveness
	// response. /api/health always returns 200 either way, so the container
	// healthcheck (which carries no token) and external liveness probes keep
	// working — the gate only controls how much detail is included.
	if h.isAdmin(r) {
		warnings := h.checkCertExpiry()
		if len(warnings) > 0 {
			result["cert_warnings"] = warnings
		}
	}

	respond.Data(w, http.StatusOK, result)
}

// isAdmin reports whether the request carries a valid admin access token. It is
// deliberately non-fatal: a missing, malformed, or invalid token simply means
// "not an admin" and yields the liveness-only response, so /api/health stays a
// public probe that never 401s.
func (h *HealthHandler) isAdmin(r *http.Request) bool {
	if h.jwt == nil {
		return false
	}
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	claims, err := h.jwt.ValidateAccessToken(parts[1])
	if err != nil {
		return false
	}
	return claims.UserType == "admin"
}

// checkCertExpiry returns warnings for certificates expiring within 30 days.
func (h *HealthHandler) checkCertExpiry() []map[string]any {
	threshold := time.Now().Add(30 * 24 * time.Hour)

	var certs []models.Certificate
	h.db.Preload("Domain").
		Where("not_after < ?", threshold).
		Find(&certs)

	if len(certs) == 0 {
		return nil
	}

	warnings := make([]map[string]any, 0, len(certs))
	now := time.Now()
	for _, cert := range certs {
		domain := ""
		if cert.Domain.Name != "" {
			domain = cert.Domain.Name
		}
		status := "expiring_soon"
		if cert.NotAfter.Before(now) {
			status = "expired"
		}
		warnings = append(warnings, map[string]any{
			"cert_id":    cert.ID,
			"domain":     domain,
			"issuer":     cert.Issuer,
			"not_after":  cert.NotAfter.Format(time.RFC3339),
			"status":     status,
			"days_until": int(time.Until(cert.NotAfter).Hours() / 24),
		})
	}
	return warnings
}
