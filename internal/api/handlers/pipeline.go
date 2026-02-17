package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type PipelineHandler struct {
	db     *gorm.DB
	engine *pipeline.Engine
}

func NewPipelineHandler(db *gorm.DB, engine *pipeline.Engine) *PipelineHandler {
	return &PipelineHandler{db: db, engine: engine}
}

// ListPipelines returns all pipelines for a domain.
func (h *PipelineHandler) ListPipelines(w http.ResponseWriter, r *http.Request) {
	domainID := r.URL.Query().Get("domain_id")

	var pipelines []models.Pipeline
	query := h.db.Order("direction ASC, id ASC")
	if domainID != "" {
		query = query.Where("domain_id = ?", domainID)
	}

	if err := query.Find(&pipelines).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list pipelines")
		return
	}
	respond.List(w, pipelines, nil)
}

// CreatePipeline creates a new pipeline.
func (h *PipelineHandler) CreatePipeline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainID  uint            `json:"domain_id"`
		Direction string          `json:"direction"`
		Filters   json.RawMessage `json:"filters"`
		Active    bool            `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Direction != "inbound" && req.Direction != "outbound" {
		respond.ValidationError(w, map[string]string{"direction": "must be 'inbound' or 'outbound'"})
		return
	}

	p := models.Pipeline{
		DomainID:  req.DomainID,
		Direction: req.Direction,
		Filters:   req.Filters,
		Active:    req.Active,
	}

	if err := h.db.Create(&p).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create pipeline")
		return
	}

	respond.Data(w, http.StatusCreated, p)
}

// UpdatePipeline updates a pipeline's configuration.
func (h *PipelineHandler) UpdatePipeline(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid pipeline ID")
		return
	}

	var p models.Pipeline
	if err := h.db.First(&p, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Pipeline not found")
		return
	}

	var req struct {
		Filters json.RawMessage `json:"filters"`
		Active  *bool           `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Filters != nil {
		p.Filters = req.Filters
	}
	if req.Active != nil {
		p.Active = *req.Active
	}

	if err := h.db.Save(&p).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update pipeline")
		return
	}

	respond.Data(w, http.StatusOK, p)
}

// DeletePipeline removes a pipeline.
func (h *PipelineHandler) DeletePipeline(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid pipeline ID")
		return
	}

	if err := h.db.Delete(&models.Pipeline{}, id).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete pipeline")
		return
	}

	respond.Data(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// TestPipeline runs a sample email through a pipeline and returns step-by-step results.
func (h *PipelineHandler) TestPipeline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PipelineID uint              `json:"pipeline_id"`
		Email      *pipeline.EmailJSON `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Email == nil {
		respond.ValidationError(w, map[string]string{"email": "required"})
		return
	}

	var p models.Pipeline
	if err := h.db.First(&p, req.PipelineID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Pipeline not found")
		return
	}

	// Parse pipeline filters
	var filterConfigs []pipeline.FilterConfig
	if err := json.Unmarshal(p.Filters, &filterConfigs); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Invalid pipeline filter config")
		return
	}

	pipelineCfg := &pipeline.PipelineConfig{
		ID:        p.ID,
		DomainID:  p.DomainID,
		Direction: p.Direction,
		Filters:   filterConfigs,
		Active:    p.Active,
	}

	result, err := h.engine.Execute(r.Context(), pipelineCfg, req.Email)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "execution_error", err.Error())
		return
	}

	respond.Data(w, http.StatusOK, result)
}

// TestFilter runs a single filter against a sample email.
func (h *PipelineHandler) TestFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FilterName string              `json:"filter_name"`
		Config     json.RawMessage     `json:"config"`
		Email      *pipeline.EmailJSON `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.FilterName == "" || req.Email == nil {
		respond.ValidationError(w, map[string]string{
			"filter_name": "required",
			"email":       "required",
		})
		return
	}

	result, err := h.engine.TestFilter(r.Context(), req.FilterName, req.Config, req.Email)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "filter_error", err.Error())
		return
	}

	respond.Data(w, http.StatusOK, result)
}

// ── Custom Filters ───────────────────────────────────────────────────

// ListCustomFilters returns custom filters for a domain.
func (h *PipelineHandler) ListCustomFilters(w http.ResponseWriter, r *http.Request) {
	domainID := r.URL.Query().Get("domain_id")

	var filters []models.CustomFilter
	query := h.db.Order("name ASC")
	if domainID != "" {
		query = query.Where("domain_id = ?", domainID)
	}

	if err := query.Find(&filters).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list custom filters")
		return
	}
	respond.List(w, filters, nil)
}

// CreateCustomFilter creates a new custom filter definition.
func (h *PipelineHandler) CreateCustomFilter(w http.ResponseWriter, r *http.Request) {
	var cf models.CustomFilter
	if err := json.NewDecoder(r.Body).Decode(&cf); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if cf.Name == "" || cf.FilterType == "" || cf.Direction == "" {
		respond.ValidationError(w, map[string]string{
			"name":        "required",
			"filter_type": "required",
			"direction":   "required",
		})
		return
	}

	if err := h.db.Create(&cf).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create custom filter")
		return
	}

	respond.Data(w, http.StatusCreated, cf)
}

// DeleteCustomFilter removes a custom filter.
func (h *PipelineHandler) DeleteCustomFilter(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid filter ID")
		return
	}

	if err := h.db.Delete(&models.CustomFilter{}, id).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete custom filter")
		return
	}

	respond.Data(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Quarantine ───────────────────────────────────────────────────────

// ListQuarantine returns quarantined messages for a mailbox.
func (h *PipelineHandler) ListQuarantine(w http.ResponseWriter, r *http.Request) {
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var items []models.Quarantine
	h.db.Where("mailbox_id = ? AND released = false", accountID).
		Order("received_at DESC").
		Find(&items)

	respond.List(w, items, nil)
}

// ReleaseQuarantine releases a quarantined message to the inbox.
func (h *PipelineHandler) ReleaseQuarantine(w http.ResponseWriter, r *http.Request) {
	mid, err := strconv.ParseUint(chi.URLParam(r, "mid"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var item models.Quarantine
	if err := h.db.First(&item, mid).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Quarantined message not found")
		return
	}

	item.Released = true
	h.db.Save(&item)

	// TODO: Re-inject raw_message into pipeline with skip for spam filters

	respond.Data(w, http.StatusOK, map[string]string{"status": "released"})
}

// DeleteQuarantine permanently deletes a quarantined message.
func (h *PipelineHandler) DeleteQuarantine(w http.ResponseWriter, r *http.Request) {
	mid, err := strconv.ParseUint(chi.URLParam(r, "mid"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	if err := h.db.Delete(&models.Quarantine{}, mid).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete")
		return
	}

	respond.Data(w, http.StatusOK, map[string]string{"status": "deleted"})
}
