package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

type HealthHandler struct {
	db  *gorm.DB
	jwt *auth.JWTService
}

func NewHealthHandler(db *gorm.DB, jwtService *auth.JWTService) *HealthHandler {
	return &HealthHandler{db: db, jwt: jwtService}
}

// Health is the body of GET /api/health. Its fields are in the order the map it replaced
// was written in, so the bytes are unchanged.
type Health struct {
	CertWarnings []CertWarning `json:"cert_warnings,omitempty" description:"Only for a bearer token of a current session holding domains:read at *, and only while a certificate expires within 30 days"`
	DB           string        `json:"db"`
	Status       string        `json:"status"`
}

// CertWarning is a certificate that has expired or expires within 30 days.
type CertWarning struct {
	CertID    uint   `json:"cert_id"`
	DaysUntil int    `json:"days_until" description:"Whole days until it expires; 0 or less once it has"`
	Domain    string `json:"domain"`
	Issuer    string `json:"issuer"`
	NotAfter  string `json:"not_after" format:"date-time"`
	Status    string `json:"status" enums:"expiring_soon,expired"`
}

func (h *HealthHandler) health(r *http.Request, _ struct{}) (Health, error) {
	sqlDB, err := h.db.DB()
	if err != nil {
		return Health{}, endpoint.Refuse(http.StatusServiceUnavailable, "unhealthy", "Database connection unavailable")
	}
	if err := sqlDB.PingContext(r.Context()); err != nil {
		return Health{}, endpoint.Refuse(http.StatusServiceUnavailable, "unhealthy", "Database ping failed")
	}

	result := Health{Status: "healthy", DB: "connected"}

	// The certificate inventory (issuer, hosted domain, expiry) is operational
	// detail that must not be enumerable by unauthenticated callers. Disclose it
	// only to an authenticated admin; everyone else gets a minimal liveness
	// response. /api/health always returns 200 either way, so the container
	// healthcheck (which carries no token) and external liveness probes keep
	// working — the gate only controls how much detail is included.
	if h.isAdmin(r) {
		result.CertWarnings = h.checkCertExpiry()
	}
	return result, nil
}

// isAdmin reports whether the request carries a current access token of an
// account holding domains:read at "*", which certificates come under. It is
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
	if err != nil || claims.SetupPending || h.db == nil {
		return false
	}
	current, err := SessionCurrent(h.db)(claims.AccountID, claims.CredentialEpoch)
	if err != nil || !current {
		return false
	}
	held, err := grants.Load(h.db, claims.AccountID, time.Now())
	return err == nil && held.Allows(middleware.CapDomainsRead, grants.All)
}

// checkCertExpiry returns warnings for certificates expiring within 30 days.
func (h *HealthHandler) checkCertExpiry() []CertWarning {
	threshold := time.Now().Add(30 * 24 * time.Hour)

	var certs []models.Certificate
	h.db.Preload("Domain").
		Where("not_after < ?", threshold).
		Find(&certs)

	if len(certs) == 0 {
		return nil
	}

	warnings := make([]CertWarning, 0, len(certs))
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
		warnings = append(warnings, CertWarning{
			CertID:    cert.ID,
			DaysUntil: int(time.Until(cert.NotAfter).Hours() / 24),
			Domain:    domain,
			Issuer:    cert.Issuer,
			NotAfter:  cert.NotAfter.Format(time.RFC3339),
			Status:    status,
		})
	}
	return warnings
}
