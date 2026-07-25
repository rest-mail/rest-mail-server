package handlers

import (
	"encoding/json"
	"bytes"
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
// `duplicate` filter with a queue_recipient. That knob records
// duplicate_queue_recipient metadata which NO delivery consumer reads, so it
// silently no-ops (issue #201). Rather than accept a config we won't honour, the
// admin save is rejected at HTTP 400 with an actionable message; the webhook
// fork is unaffected and stays configurable.
//
// The reject deliberately lives here (config save) rather than in NewDuplicate:
// erroring at filter construction would make the engine's fail-closed policy
// DEFER live mail for any already-stored config, which is worse than the benign
// no-op. Every duplicate block is inspected regardless of its `enabled` flag —
// a disabled-but-unsupported block would no-op the moment it is enabled.
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
			return fmt.Errorf("the duplicate filter's queue_recipient is not supported (it has no delivery consumer and would silently no-op); use webhook_url to fork a copy")
		}
	}
	return nil
}

type PipelineHandler struct {
	db     *gorm.DB
	engine *pipeline.Engine
}

func NewPipelineHandler(db *gorm.DB, engine *pipeline.Engine) *PipelineHandler {
	return &PipelineHandler{db: db, engine: engine}
}

// ListPipelines returns all pipelines for a domain.
func (h *PipelineHandler) ListPipelines(w http.ResponseWriter, r *http.Request) {
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

// ListPipelineLogs returns recent per-message pipeline traces, newest first.
//
// PR5 repoint: the write path moved to message_traces (PR3), and the legacy
// pipeline_logs table is no longer written, so this READ now reads
// message_traces. The response is the richer trace shape (outcome, reason_code,
// transport, mail_from/rcpt_to/client_ip, stages timeline, duration) rather than
// the old {steps, action} log. The original pagination and the pipeline_id /
// direction / action filters are preserved (action now matches the trace's
// final_action column), and rfc_message_id / outcome / reason_code filters are
// added to support message-lifecycle drill-down.
func (h *PipelineHandler) ListPipelineLogs(w http.ResponseWriter, r *http.Request) {
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
	// action filters on the pipeline's final_action (the old PipelineLog.Action
	// column's successor), preserving the existing query contract.
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
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
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

	// Install-validate every embedded Sieve script BEFORE persisting: a broken
	// domain Sieve fails to build at delivery and, under the engine's fail-closed
	// policy, would defer the whole domain's mail. Reject it now with HTTP 400.
	if err := validateSievePipelineFilters(req.Filters); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_sieve", err.Error())
		return
	}

	// Reject a duplicate filter configured with an unsupported queue_recipient
	// (issue #201) before persisting, for the same reason: an unhonoured config
	// silently no-ops at delivery.
	if err := validateDuplicatePipelineFilters(req.Filters); err != nil {
		respond.Error(w, http.StatusBadRequest, "unsupported_filter", err.Error())
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
		Enabled     bool            `json:"enabled"`
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

	cf := models.CustomFilter{
		DomainID:    req.DomainID,
		Name:        req.Name,
		Description: req.Description,
		FilterType:  req.FilterType,
		Direction:   req.Direction,
		Config:      req.Config,
		Enabled:     req.Enabled,
	}

	if err := h.db.Create(&cf).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create custom filter")
		return
	}

	respond.Data(w, http.StatusCreated, cf)
}

// GetCustomFilter returns a single custom filter by ID.
func (h *PipelineHandler) GetCustomFilter(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
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
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid filter ID")
		return
	}

	var cf models.CustomFilter
	if err := h.db.First(&cf, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Custom filter not found")
		return
	}

	var req struct {
		Name        *string          `json:"name"`
		Description *string          `json:"description"`
		FilterType  *string          `json:"filter_type"`
		Direction   *string          `json:"direction"`
		Config      *json.RawMessage `json:"config"`
		Enabled     *bool            `json:"enabled"`
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
		cf.Config = *req.Config
	}
	if req.Enabled != nil {
		cf.Enabled = *req.Enabled
	}

	if err := h.db.Save(&cf).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update custom filter")
		return
	}

	respond.Data(w, http.StatusOK, cf)
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

// TestCustomFilter loads a custom filter from the DB and runs its script against a sample email
// via the JS filter sidecar.
func (h *PipelineHandler) TestCustomFilter(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid filter ID")
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

	// Accept optional sample email from request body
	var req struct {
		Email *json.RawMessage `json:"email,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

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

	sidecarURL := "http://js-filter:3100/execute"
	client := &http.Client{Timeout: 10 * time.Second}
	sidecarResp, err := client.Post(sidecarURL, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "service_unavailable", "JS filter sidecar unavailable")
		return
	}
	defer sidecarResp.Body.Close()

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

	sidecarURL := "http://js-filter:3100/validate"
	client := &http.Client{Timeout: 5 * time.Second}
	sidecarResp, err := client.Post(sidecarURL, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "service_unavailable", "JS filter sidecar unavailable")
		return
	}
	defer sidecarResp.Body.Close()

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
// verifying that the authenticated user owns the account (either as their
// primary account or a linked account). Quarantine rows are per-mailbox
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
	return resolveAccountMailbox(h.db, uint(accountID), claims.WebmailAccountID)
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

	mid, err := strconv.ParseUint(chi.URLParam(r, "mid"), 10, 32)
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

	mid, err := strconv.ParseUint(chi.URLParam(r, "mid"), 10, 32)
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
