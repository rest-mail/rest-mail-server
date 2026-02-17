package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type AliasHandler struct {
	db *gorm.DB
}

func NewAliasHandler(db *gorm.DB) *AliasHandler {
	return &AliasHandler{db: db}
}

func (h *AliasHandler) List(w http.ResponseWriter, r *http.Request) {
	var aliases []models.Alias
	query := h.db.Preload("Domain")

	if domainID := r.URL.Query().Get("domain_id"); domainID != "" {
		query = query.Where("domain_id = ?", domainID)
	}

	if err := query.Order("source_address ASC").Find(&aliases).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list aliases")
		return
	}
	respond.List(w, aliases, nil)
}

type createAliasRequest struct {
	SourceAddress      string `json:"source_address"`
	DestinationAddress string `json:"destination_address"`
}

func (h *AliasHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createAliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	errors := map[string]string{}
	if req.SourceAddress == "" {
		errors["source_address"] = "required"
	}
	if req.DestinationAddress == "" {
		errors["destination_address"] = "required"
	}
	if len(errors) > 0 {
		respond.ValidationError(w, errors)
		return
	}

	parts := strings.SplitN(req.SourceAddress, "@", 2)
	if len(parts) != 2 {
		respond.ValidationError(w, map[string]string{"source_address": "must be a valid email address"})
		return
	}

	var domain models.Domain
	if err := h.db.Where("name = ?", parts[1]).First(&domain).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain not found for source address")
		return
	}

	alias := models.Alias{
		DomainID:           domain.ID,
		SourceAddress:      req.SourceAddress,
		DestinationAddress: req.DestinationAddress,
		Active:             true,
	}

	if err := h.db.Create(&alias).Error; err != nil {
		respond.Error(w, http.StatusConflict, "already_exists", "Alias already exists")
		return
	}

	respond.Data(w, http.StatusCreated, alias)
}

func (h *AliasHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid alias ID")
		return
	}

	var alias models.Alias
	if err := h.db.Preload("Domain").First(&alias, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Alias not found")
		return
	}

	respond.Data(w, http.StatusOK, alias)
}

type updateAliasRequest struct {
	DestinationAddress *string `json:"destination_address"`
	Active             *bool   `json:"active"`
}

func (h *AliasHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid alias ID")
		return
	}

	var alias models.Alias
	if err := h.db.First(&alias, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Alias not found")
		return
	}

	var req updateAliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.DestinationAddress != nil {
		updates["destination_address"] = *req.DestinationAddress
	}
	if req.Active != nil {
		updates["active"] = *req.Active
	}

	if len(updates) > 0 {
		if err := h.db.Model(&alias).Updates(updates).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update alias")
			return
		}
	}

	h.db.Preload("Domain").First(&alias, id)
	respond.Data(w, http.StatusOK, alias)
}

func (h *AliasHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid alias ID")
		return
	}

	var alias models.Alias
	if err := h.db.First(&alias, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Alias not found")
		return
	}

	if err := h.db.Delete(&alias).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete alias")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
