package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type DomainHandler struct {
	db *gorm.DB
}

func NewDomainHandler(db *gorm.DB) *DomainHandler {
	return &DomainHandler{db: db}
}

func (h *DomainHandler) List(w http.ResponseWriter, r *http.Request) {
	var domains []models.Domain
	if err := h.db.Order("name ASC").Find(&domains).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list domains")
		return
	}
	respond.List(w, domains, nil)
}

type createDomainRequest struct {
	Name              string `json:"name"`
	ServerType        string `json:"server_type"`
	DefaultQuotaBytes *int64 `json:"default_quota_bytes"`
}

func (h *DomainHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Name == "" {
		respond.ValidationError(w, map[string]string{"name": "required"})
		return
	}

	if req.ServerType == "" {
		req.ServerType = "traditional"
	}
	if req.ServerType != "traditional" && req.ServerType != "restmail" {
		respond.ValidationError(w, map[string]string{"server_type": "must be 'traditional' or 'restmail'"})
		return
	}

	domain := models.Domain{
		Name:       req.Name,
		ServerType: req.ServerType,
		Active:     true,
	}
	if req.DefaultQuotaBytes != nil {
		domain.DefaultQuotaBytes = *req.DefaultQuotaBytes
	}

	if err := h.db.Create(&domain).Error; err != nil {
		respond.Error(w, http.StatusConflict, "already_exists", "Domain already exists")
		return
	}

	respond.Data(w, http.StatusCreated, domain)
}

func (h *DomainHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found")
		return
	}

	respond.Data(w, http.StatusOK, domain)
}

type updateDomainRequest struct {
	Active            *bool  `json:"active"`
	DefaultQuotaBytes *int64 `json:"default_quota_bytes"`
}

func (h *DomainHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found")
		return
	}

	var req updateDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.Active != nil {
		updates["active"] = *req.Active
	}
	if req.DefaultQuotaBytes != nil {
		updates["default_quota_bytes"] = *req.DefaultQuotaBytes
	}

	if len(updates) > 0 {
		if err := h.db.Model(&domain).Updates(updates).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update domain")
			return
		}
	}

	h.db.First(&domain, id)
	respond.Data(w, http.StatusOK, domain)
}

func (h *DomainHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found")
		return
	}

	if err := h.db.Delete(&domain).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete domain")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
