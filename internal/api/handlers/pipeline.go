package handlers

import (
	"encoding/json"
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
