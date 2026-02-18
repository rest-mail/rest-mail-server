package handlers

import (
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type HealthHandler struct {
	db *gorm.DB
}

func NewHealthHandler(db *gorm.DB) *HealthHandler {
	return &HealthHandler{db: db}
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

	// Check certificate expiry
	warnings := h.checkCertExpiry()
	if len(warnings) > 0 {
		result["cert_warnings"] = warnings
	}

	respond.Data(w, http.StatusOK, result)
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
