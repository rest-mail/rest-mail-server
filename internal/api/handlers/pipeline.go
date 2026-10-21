package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

// validateSievePipelineFilters install-validates every embedded Sieve script in
// a domain pipeline's raw filter list, using the SAME gate the per-mailbox Sieve
// save path uses (filters.ValidateSieveForInstall: parse + a safe, side-effect-
// free dry-run). raw is the pipelines.filters JSON — an array of
// pipeline.FilterConfig; each element whose Name is "sieve" carries a
// {"script": ...} config.
//
// It returns the FIRST script's *filters.SieveInstallError (nil when all are
// safe) so the admin save can be rejected with an actionable HTTP 400 BEFORE the
// config is persisted. Without this an admin's broken domain Sieve is stored and
// only fails at DELIVERY — where the filter fails to build and the engine's
// fail-closed policy DEFERS every message for the entire domain (see
// engine.Execute / filterErrorAction), silently, for as long as the bad config
// lives.
//
// Scope is deliberately narrow: only Sieve blocks are inspected. Unrelated
// malformed filter JSON is NOT this helper's concern (it is handled at build /
// test time), so a filter list that does not unmarshal is treated as "no Sieve
// to validate" (nil) rather than being rejected here — that preserves the
// existing create/update contract for every non-Sieve shape. Every Sieve block
// is validated regardless of its `enabled` flag: a disabled-but-invalid block
// would fail the moment an operator enables it, so it is rejected at save too.
func validateSievePipelineFilters(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var fcs []pipeline.FilterConfig
	if err := json.Unmarshal(raw, &fcs); err != nil {
		return nil // not our concern: only surface Sieve validation errors
	}
	for _, fc := range fcs {
		if fc.Name != "sieve" || len(fc.Config) == 0 {
			continue
		}
		var cfg struct {
			Script string `json:"script"`
		}
		if err := json.Unmarshal(fc.Config, &cfg); err != nil {
			continue // a config that is not a sieve script object: nothing to validate
		}
		if err := filters.ValidateSieveForInstall(cfg.Script); err != nil {
			return err
		}
	}
	return nil
}

// validateDuplicatePipelineFilters rejects a domain pipeline that configures the
// `duplicate` filter with a queue_recipient. The filter only forks to a webhook
// (issue #201): copying to a mailbox reads like a working option, so a config
// asking for it is refused at HTTP 400 with an actionable message rather than
// saved and silently ignored. Every duplicate block is inspected regardless of
// its `enabled` flag — a disabled block would be ignored the moment it is
// enabled.
//
// Scope mirrors validateSievePipelineFilters: a filter list that does not
// unmarshal is "nothing to validate" (nil), preserving the existing contract for
// unrelated shapes.
func validateDuplicatePipelineFilters(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var fcs []pipeline.FilterConfig
	if err := json.Unmarshal(raw, &fcs); err != nil {
		return nil
	}
	for _, fc := range fcs {
		if fc.Name != "duplicate" || len(fc.Config) == 0 {
			continue
		}
		var cfg struct {
			QueueRecipient string `json:"queue_recipient"`
		}
		if err := json.Unmarshal(fc.Config, &cfg); err != nil {
			continue
		}
		if strings.TrimSpace(cfg.QueueRecipient) != "" {
			return fmt.Errorf("the duplicate filter's queue_recipient is not supported; use webhook_url to fork a copy")
		}
	}
	return nil
}

// isFilterObjectArray reports whether raw is a JSON array whose every item is an
// object with an optional string name and object config, as documented by the
// create and update schemas. Missing properties and extra keys are allowed;
// explicit nulls are not. Filter-specific rules remain with the Sieve and
// duplicate gates and the build at delivery.
func isFilterObjectArray(raw json.RawMessage) bool {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return false
	}
	for _, item := range items {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err != nil || obj == nil {
			return false
		}
		if name, ok := obj["name"]; ok {
			var value *string
			if err := json.Unmarshal(name, &value); err != nil || value == nil {
				return false
			}
		}
		if config, ok := obj["config"]; ok {
			var value map[string]json.RawMessage
			if err := json.Unmarshal(config, &value); err != nil || value == nil {
				return false
			}
		}
	}
	return true
}

type PipelineHandler struct {
	db     *gorm.DB
	engine *pipeline.Engine
	// jsSidecar runs custom filter scripts for the test and validate routes.
	jsSidecar filters.JSSidecar
}

func NewPipelineHandler(db *gorm.DB, engine *pipeline.Engine, jsSidecar filters.JSSidecar) *PipelineHandler {
	return &PipelineHandler{db: db, engine: engine, jsSidecar: jsSidecar}
}

// jsSidecarRefused is the error when the sidecar refuses the API's token. It is
// a 503, never the sidecar's 401, which would read as the admin's own session
// having ended.
const jsSidecarRefused = "JS filter sidecar refused the API's token (JS_FILTER_TOKEN differs between the API and the sidecar)"

// ListPipelines returns all pipelines for a domain.
func (h *PipelineHandler) ListPipelines(w http.ResponseWriter, r *http.Request) {
	if !validateIntegerQuery(w, r, 0, "domain_id") {
		return
	}
	limit, offset := parsePagination(r, defaultListLimit, maxListLimit)
	domainID := r.URL.Query().Get("domain_id")

	query := h.db.Model(&models.Pipeline{})
	if domainID != "" {
		query = query.Where("domain_id = ?", domainID)
	}

	var total int64
	query.Count(&total)

	var pipelines []models.Pipeline
	if err := query.Order("direction ASC, id ASC").Limit(limit).Offset(offset).Find(&pipelines).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list pipelines")
		return
	}
	respond.List(w, pipelines, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}

// ListPipelineLogs returns recent per-message pipeline traces, newest first,
// from message_traces: outcome, reason_code, transport,
// mail_from/rcpt_to/client_ip, the stages timeline and duration. It filters on
// pipeline_id, direction, action (the trace's final_action column), and on
// rfc_message_id / outcome / reason_code for message-lifecycle drill-down.
func (h *PipelineHandler) ListPipelineLogs(w http.ResponseWriter, r *http.Request) {
	if !validateIntegerQuery(w, r, 200, "pipeline_id", "limit", "offset") {
		return
	}
	params := r.URL.Query()
	if params.Has("direction") {
		switch params.Get("direction") {
		case "inbound", "outbound":
		default:
			respond.ValidationError(w, map[string]string{"direction": "must be one of inbound, outbound"})
			return
		}
	}
	if params.Has("action") {
		switch params.Get("action") {
		case "continue", "reject", "quarantine", "discard", "defer":
		default:
			respond.ValidationError(w, map[string]string{"action": "must be one of continue, reject, quarantine, discard, defer"})
			return
		}
	}
	if params.Has("outcome") {
		switch params.Get("outcome") {
		case "delivered", "queued", "rejected", "quarantined", "discarded", "deferred":
		default:
			respond.ValidationError(w, map[string]string{"outcome": "must be one of delivered, queued, rejected, quarantined, discarded, deferred"})
			return
		}
	}
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	query := h.db.Model(&models.MessageTrace{})
	if pipelineID := r.URL.Query().Get("pipeline_id"); pipelineID != "" {
		query = query.Where("pipeline_id = ?", pipelineID)
	}
	if direction := r.URL.Query().Get("direction"); direction != "" {
		query = query.Where("direction = ?", direction)
	}
	// action filters on the pipeline's final_action.
	if action := r.URL.Query().Get("action"); action != "" {
		query = query.Where("final_action = ?", action)
	}
	if rfcMessageID := r.URL.Query().Get("rfc_message_id"); rfcMessageID != "" {
		query = query.Where("rfc_message_id = ?", rfcMessageID)
	}
	if outcome := r.URL.Query().Get("outcome"); outcome != "" {
		query = query.Where("outcome = ?", outcome)
	}
	if reasonCode := r.URL.Query().Get("reason_code"); reasonCode != "" {
		query = query.Where("reason_code = ?", reasonCode)
	}

	var total int64
	query.Count(&total)

	var traces []models.MessageTrace
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&traces).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list pipeline traces")
		return
	}
	respond.List(w, traces, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}

// GetMessageTrace returns the ordered pipeline stage timeline for a delivered
// message — the delivered-message case of the trace read surface.
// GET /api/v1/admin/messages/{id}/trace
//
// It joins message_traces on message_id (set ONLY on the delivered/queued path;
// nil for rejected/quarantined/discarded/deferred mail, which never became a
// Message row and is instead correlated via rfc_message_id on ListPipelineLogs).
// A non-delivered or unknown id therefore has no trace here → 404.
func (h *PipelineHandler) GetMessageTrace(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var trace models.MessageTrace
	if err := h.db.Where("message_id = ?", uint(id)).Order("created_at DESC").First(&trace).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "No pipeline trace for this message")
		return
	}
	respond.Data(w, http.StatusOK, trace)
}

// CreatePipeline creates a new pipeline.
func (h *PipelineHandler) CreatePipeline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainID  uint            `json:"domain_id"`
		Direction string          `json:"direction"`
		Filters   json.RawMessage `json:"filters"`
		Active    *bool           `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Direction != "inbound" && req.Direction != "outbound" {
		respond.ValidationError(w, map[string]string{"direction": "must be 'inbound' or 'outbound'"})
		return
	}

	// filters is required and is an array of objects (CreatePipelineRequest,
	// #388). The column takes any JSON, so a missing or misshapen list would be
	// stored as it came and fail only when the pipeline is built for delivery.
	if !isFilterObjectArray(req.Filters) {
		respond.ValidationError(w, map[string]string{"filters": "must be an array of filter objects"})
		return
	}

	// Install-validate every embedded Sieve script BEFORE persisting: a broken
	// domain Sieve fails to build at delivery and, under the engine's fail-closed
	// policy, would defer the whole domain's mail. Reject it now with HTTP 400.
	if err := validateSievePipelineFilters(req.Filters); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_sieve", err.Error())
		return
	}

	// Reject a duplicate filter configured with an unsupported queue_recipient
	// (issue #201) before persisting: an unhonoured config would be silently
	// ignored at delivery.
	if err := validateDuplicatePipelineFilters(req.Filters); err != nil {
		respond.Error(w, http.StatusBadRequest, "unsupported_filter", err.Error())
		return
	}

	p := models.Pipeline{
		DomainID:  req.DomainID,
		Direction: req.Direction,
		Filters:   req.Filters,
		Active:    true,
	}
	if req.Active != nil {
		p.Active = *req.Active
	}

	// A non-nil pointer keeps GORM from replacing explicit false with the
	// model's default:true. Overlay it for this single insert only; other
	// creation paths retain the model default, with no follow-up update needed.
	// The embedded Pipeline supplies the table name.
	row := struct {
		models.Pipeline
		Active *bool
	}{Pipeline: p, Active: &p.Active}
	if err := h.db.Create(&row).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create pipeline")
		return
	}

	respond.Data(w, http.StatusCreated, row.Pipeline)
}

// UpdatePipeline updates a pipeline's configuration.
func (h *PipelineHandler) UpdatePipeline(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
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
		// A supplied list has the same shape as CreatePipeline's (#388); null is
		// not one, as UpdatePipelineRequest's filters are not nullable. Only an
		// omitted list leaves the stored one as it was.
		if !isFilterObjectArray(req.Filters) {
			respond.ValidationError(w, map[string]string{"filters": "must be an array of filter objects"})
			return
		}
		// Same install-time Sieve gate as CreatePipeline: reject a broken embedded
		// Sieve with HTTP 400 and leave the stored config untouched.
		if err := validateSievePipelineFilters(req.Filters); err != nil {
			respond.Error(w, http.StatusBadRequest, "invalid_sieve", err.Error())
			return
		}
		if err := validateDuplicatePipelineFilters(req.Filters); err != nil {
			respond.Error(w, http.StatusBadRequest, "unsupported_filter", err.Error())
			return
		}
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
	id, err := pathID(r, "id")
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
		PipelineID uint                `json:"pipeline_id"`
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
	if !validateIntegerQuery(w, r, 0, "domain_id") {
		return
	}
	limit, offset := parsePagination(r, defaultListLimit, maxListLimit)
	domainID := r.URL.Query().Get("domain_id")

	query := h.db.Model(&models.CustomFilter{})
	if domainID != "" {
		query = query.Where("domain_id = ?", domainID)
	}

	var total int64
	query.Count(&total)

	var filters []models.CustomFilter
	if err := query.Order("name ASC").Limit(limit).Offset(offset).Find(&filters).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list custom filters")
		return
	}
	respond.List(w, filters, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}

// CreateCustomFilter creates a new custom filter definition.
func (h *PipelineHandler) CreateCustomFilter(w http.ResponseWriter, r *http.Request) {
	// Decode only the client-settable fields into a dedicated request struct.
	// Never bind straight into models.CustomFilter: its exported Domain
	// association would be auto-saved by GORM on Create, letting a caller that
	// holds only pipelines:write insert a Domain row without domains:write (a
	// privilege escalation), and a client-supplied ID/timestamps would be
	// honoured too. The domain is bound by foreign key only, never via a nested
	// Domain object — mirroring CreatePipeline and the update handlers.
	var req struct {
		DomainID    uint            `json:"domain_id"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		FilterType  string          `json:"filter_type"`
		Direction   string          `json:"direction"`
		Config      json.RawMessage `json:"config"`
		Enabled     *bool           `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Name == "" || req.FilterType == "" || req.Direction == "" {
		respond.ValidationError(w, map[string]string{
			"name":        "required",
			"filter_type": "required",
			"direction":   "required",
		})
		return
	}
	if errs := customFilterFieldErrors(req.FilterType, req.Direction); errs != nil {
		respond.ValidationError(w, errs)
		return
	}
	// config is required and is an object (CreateCustomFilterRequest, #388).
	// The column takes any JSON, so a missing or misshapen config would be
	// stored as it came. What the object holds is the filter's own concern.
	if !isJSONObject(req.Config) {
		respond.ValidationError(w, map[string]string{"config": "must be an object"})
		return
	}

	cf := models.CustomFilter{
		DomainID:    req.DomainID,
		Name:        req.Name,
		Description: req.Description,
		FilterType:  req.FilterType,
		Direction:   req.Direction,
		Config:      req.Config,
		Enabled:     true,
	}

	if req.Enabled != nil {
		cf.Enabled = *req.Enabled
	}

	// A non-nil pointer preserves explicit false despite the model's
	// default:true. Overlay only this insert so other creation paths retain
	// that default, without a follow-up update or partial-write window.
	row := struct {
		models.CustomFilter
		Enabled *bool
	}{CustomFilter: cf, Enabled: &cf.Enabled}
	if err := h.db.Table(cf.TableName()).Create(&row).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create custom filter")
		return
	}

	respond.Data(w, http.StatusCreated, row.CustomFilter)
}

// customFilterFieldErrors checks a custom filter's type and direction against
// the values the spec allows, or returns nil when both are allowed.
func customFilterFieldErrors(filterType, direction string) map[string]string {
	errs := map[string]string{}
	if filterType != "action" && filterType != "transform" {
		errs["filter_type"] = "must be action or transform"
	}
	if direction != "inbound" && direction != "outbound" && direction != "both" {
		errs["direction"] = "must be inbound, outbound or both"
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// isJSONObject reports whether raw is a JSON object, the shape of a custom
// filter's config. A missing or null config is not one; an empty object is.
func isJSONObject(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

// GetCustomFilter returns a single custom filter by ID.
func (h *PipelineHandler) GetCustomFilter(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid filter ID")
		return
	}

	var cf models.CustomFilter
	if err := h.db.First(&cf, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Custom filter not found")
		return
	}

	respond.Data(w, http.StatusOK, cf)
}

// UpdateCustomFilter updates a custom filter's properties (enable/disable, config, etc.).
func (h *PipelineHandler) UpdateCustomFilter(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid filter ID")
		return
	}

	var cf models.CustomFilter
	if err := h.db.First(&cf, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Custom filter not found")
		return
	}

	// Config is not a pointer: a pointer leaves an explicit null nil, the same
	// as an omitted config, so a null would be passed over instead of refused.
	// Omitted, it is nil; null, it is the bytes "null".
	var req struct {
		Name        *string         `json:"name"`
		Description *string         `json:"description"`
		FilterType  *string         `json:"filter_type"`
		Direction   *string         `json:"direction"`
		Config      json.RawMessage `json:"config"`
		Enabled     *bool           `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Name != nil {
		cf.Name = *req.Name
	}
	if req.Description != nil {
		cf.Description = *req.Description
	}
	if req.FilterType != nil {
		cf.FilterType = *req.FilterType
	}
	if req.Direction != nil {
		cf.Direction = *req.Direction
	}
	if req.Config != nil {
		cf.Config = req.Config
	}
	if req.Enabled != nil {
		cf.Enabled = *req.Enabled
	}
	if cf.Name == "" {
		respond.ValidationError(w, map[string]string{"name": "required"})
		return
	}
	if errs := customFilterFieldErrors(cf.FilterType, cf.Direction); errs != nil {
		respond.ValidationError(w, errs)
		return
	}
	// A config, when sent, is an object (UpdateCustomFilterRequest, #388), as
	// on create: null, an array or a scalar is refused, not stored.
	if req.Config != nil && !isJSONObject(req.Config) {
		respond.ValidationError(w, map[string]string{"config": "must be an object"})
		return
	}

	if err := h.db.Save(&cf).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update custom filter")
		return
	}

	respond.Data(w, http.StatusOK, cf)
}

// DeleteCustomFilter removes a custom filter.
func (h *PipelineHandler) DeleteCustomFilter(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
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

// TestCustomFilter loads a custom filter from the DB and runs its script against a sample email
// via the JS filter sidecar.
func (h *PipelineHandler) TestCustomFilter(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid filter ID")
		return
	}

	// Accept optional sample email from request body. No body is fine; a body
	// that does not decode is refused before the filter is looked up (#386).
	var req struct {
		Email *json.RawMessage `json:"email,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	var cf models.CustomFilter
	if err := h.db.First(&cf, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Custom filter not found")
		return
	}

	// Parse config to extract the script
	var config struct {
		Script string `json:"script"`
	}
	if err := json.Unmarshal(cf.Config, &config); err != nil || config.Script == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Filter config does not contain a script")
		return
	}

	// Build sidecar request
	sidecarBody := map[string]any{"script": config.Script}
	if req.Email != nil {
		sidecarBody["email"] = req.Email
	} else {
		// Provide a minimal sample email for testing
		sidecarBody["email"] = map[string]any{
			"from":    "test@example.com",
			"to":      []string{"user@example.com"},
			"subject": "Test email",
			"body":    "This is a test email for filter testing.",
		}
	}
	bodyBytes, _ := json.Marshal(sidecarBody)

	client := &http.Client{Timeout: 10 * time.Second}
	sidecarResp, err := h.jsSidecar.Post(r.Context(), client, "/execute", bodyBytes)
	if err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "service_unavailable", "JS filter sidecar unavailable")
		return
	}
	defer sidecarResp.Body.Close()
	if sidecarResp.StatusCode == http.StatusUnauthorized {
		respond.Error(w, http.StatusServiceUnavailable, "service_unavailable", jsSidecarRefused)
		return
	}

	respBody, _ := io.ReadAll(sidecarResp.Body)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(sidecarResp.StatusCode)
	_, _ = w.Write(respBody)
}

// ValidateCustomFilter syntax-checks a JavaScript filter script via the sidecar.
func (h *PipelineHandler) ValidateCustomFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Script string           `json:"script"`
		Email  *json.RawMessage `json:"email,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Script == "" {
		respond.ValidationError(w, map[string]string{"script": "required"})
		return
	}

	// Build sidecar request
	sidecarBody := map[string]any{"script": req.Script}
	if req.Email != nil {
		sidecarBody["email"] = req.Email
	}
	bodyBytes, _ := json.Marshal(sidecarBody)

	client := &http.Client{Timeout: 5 * time.Second}
	sidecarResp, err := h.jsSidecar.Post(r.Context(), client, "/validate", bodyBytes)
	if err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "service_unavailable", "JS filter sidecar unavailable")
		return
	}
	defer sidecarResp.Body.Close()
	if sidecarResp.StatusCode == http.StatusUnauthorized {
		respond.Error(w, http.StatusServiceUnavailable, "service_unavailable", jsSidecarRefused)
		return
	}

	respBody, _ := io.ReadAll(sidecarResp.Body)

	var result json.RawMessage
	if err := json.Unmarshal(respBody, &result); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Invalid sidecar response")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)
}

// ── Quarantine ───────────────────────────────────────────────────────

// ListQuarantine returns quarantined messages for a mailbox.
// resolveMailboxID resolves the account ID from the URL to a mailbox ID,
// verifying that the session may reach the account (its own, or one it holds
// a mail grant on). Quarantine rows are per-mailbox
// confidential mail; without this check the {id} path segment was trusted as a
// raw mailbox ID and quarantine rows were released/deleted by row ID with no
// owner check, letting any authenticated caller view/release/delete another
// mailbox's quarantined mail (IDOR).
func (h *PipelineHandler) resolveMailboxID(r *http.Request, accountIDStr string) (uint, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return 0, fmt.Errorf("no claims")
	}

	accountID, err := strconv.ParseUint(accountIDStr, 10, 32)
	if err != nil {
		return 0, err
	}
	return resolveAccountMailbox(h.db, uint(accountID), claims, mailCapabilityFor(r))
}

func (h *PipelineHandler) ListQuarantine(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := h.resolveMailboxID(r, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	limit, offset := parsePagination(r, defaultListLimit, maxListLimit)

	query := h.db.Model(&models.Quarantine{}).Where("mailbox_id = ? AND released = false", mailboxID)

	var total int64
	query.Count(&total)

	var items []models.Quarantine
	query.Order("received_at DESC").Limit(limit).Offset(offset).Find(&items)

	respond.List(w, items, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}

// ReleaseQuarantine releases a quarantined message to the inbox.
func (h *PipelineHandler) ReleaseQuarantine(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := h.resolveMailboxID(r, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	mid, err := pathID(r, "mid")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	// Scope the lookup to the caller's mailbox so a quarantine row belonging to
	// another mailbox is indistinguishable from a non-existent one (404, no leak).
	var item models.Quarantine
	if err := h.db.Where("id = ? AND mailbox_id = ?", mid, mailboxID).First(&item).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Quarantined message not found")
		return
	}

	item.Released = true
	h.db.Save(&item)

	// Deliver the quarantined message to the user's INBOX directly,
	// bypassing spam filters (the user explicitly released it).
	// Extract body from raw message
	bodyText := item.BodyPreview // fallback
	bodyHTML := ""
	if item.RawMessage != "" {
		if headerEnd := strings.Index(item.RawMessage, "\r\n\r\n"); headerEnd >= 0 {
			bodyText = item.RawMessage[headerEnd+4:]
		} else if headerEnd := strings.Index(item.RawMessage, "\n\n"); headerEnd >= 0 {
			bodyText = item.RawMessage[headerEnd+2:]
		}
	}

	msg := models.Message{
		MailboxID:  item.MailboxID,
		Folder:     "INBOX",
		Sender:     item.Sender,
		Subject:    item.Subject,
		BodyText:   bodyText,
		BodyHTML:   bodyHTML,
		RawMessage: item.RawMessage,
		SizeBytes:  len(item.RawMessage),
		RawSize:    len(item.RawMessage), // exact stored-raw octet count for IMAP/POP3 size reporting
	}
	if err := h.db.Create(&msg).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to deliver released message")
		return
	}

	// Update quota
	h.db.Model(&models.Mailbox{}).Where("id = ?", item.MailboxID).
		Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", msg.SizeBytes))

	respond.Data(w, http.StatusOK, map[string]string{"status": "released", "message_id": strconv.FormatUint(uint64(msg.ID), 10)})
}

// DeleteQuarantine permanently deletes a quarantined message.
func (h *PipelineHandler) DeleteQuarantine(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := h.resolveMailboxID(r, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	mid, err := pathID(r, "mid")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	// Delete only when the row belongs to the caller's mailbox; a mismatch
	// affects zero rows and returns 404 rather than deleting another user's mail.
	res := h.db.Where("id = ? AND mailbox_id = ?", mid, mailboxID).Delete(&models.Quarantine{})
	if res.Error != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete")
		return
	}
	if res.RowsAffected == 0 {
		respond.Error(w, http.StatusNotFound, "not_found", "Quarantined message not found")
		return
	}

	respond.Data(w, http.StatusOK, map[string]string{"status": "deleted"})
}
