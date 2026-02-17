package handlers

import (
	"net/http"

	"github.com/restmail/restmail/internal/api/respond"
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
	respond.Data(w, http.StatusOK, map[string]string{
		"status": "healthy",
		"db":     "connected",
	})
}
