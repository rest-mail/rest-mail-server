package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
)

// PipelineDomain preserves the unloaded domain association the old model serialized.
// Its server_type may be empty because these queries do not preload a domain.
type PipelineDomain struct {
	ID                uint      `json:"id"`
	Name              string    `json:"name"`
	ServerType        string    `json:"server_type" enums:",traditional,restmail"`
	Active            bool      `json:"active"`
	DefaultQuotaBytes int64     `json:"default_quota_bytes"`
	DKIMSelector      string    `json:"dkim_selector"`
	SendingPolicy     string    `json:"sending_policy" enums:",transactional,bulk"`
	DMARCPolicy       string    `json:"dmarc_policy" enums:",none,quarantine,reject"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Pipeline and CustomFilter are API values, independent of persistence models.
type Pipeline struct {
	ID        uint            `json:"id"`
	DomainID  uint            `json:"domain_id"`
	Direction string          `json:"direction" enums:"inbound,outbound"`
	Filters   json.RawMessage `json:"filters"`
	Active    bool            `json:"active"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`

	Domain PipelineDomain `json:"domain,omitempty"`
}
type CustomFilter struct {
	ID          uint            `json:"id"`
	DomainID    uint            `json:"domain_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	FilterType  string          `json:"filter_type" enums:"action,transform"`
	Direction   string          `json:"direction" enums:"inbound,outbound,both"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`

	Domain PipelineDomain `json:"domain,omitempty"`
}
type MessageTrace struct {
	ID uint `json:"id"`

	MessageID *uint `json:"message_id"`

	RFCMessageID string `json:"rfc_message_id"`

	Direction string `json:"direction" enums:"inbound,outbound"`
	Transport string `json:"transport" enums:",plaintext,tls"`

	MailFrom string `json:"mail_from"`
	RcptTo   string `json:"rcpt_to"` // first recipient
	ClientIP string `json:"client_ip"`

	PipelineID  uint   `json:"pipeline_id"`
	FinalAction string `json:"final_action" enums:"continue,defer,discard,quarantine,reject"`

	Outcome string `json:"outcome" enums:"deferred,delivered,discarded,quarantined,queued,rejected"`

	ReasonCode string `json:"reason_code"`

	SpamScore  *float32        `json:"spam_score,omitempty"`
	DurationMS int64           `json:"duration_ms"`
	Stages     json.RawMessage `json:"stages"` // []pipeline.StepResult

	Sampled bool `json:"sampled"`

	CreatedAt time.Time `json:"created_at"`

	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func pipelineOf(p models.Pipeline) Pipeline {
	return Pipeline{ID: p.ID, DomainID: p.DomainID, Direction: p.Direction, Filters: p.Filters, Active: p.Active, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Domain: PipelineDomain(domainOf(p.Domain))}
}
func customFilterOf(f models.CustomFilter) CustomFilter {
	return CustomFilter{ID: f.ID, DomainID: f.DomainID, Name: f.Name, Description: f.Description, FilterType: f.FilterType, Direction: f.Direction, Config: f.Config, Enabled: f.Enabled, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt, Domain: PipelineDomain(domainOf(f.Domain))}
}

type deletedStatus struct {
	Status string `json:"status"`
}
type pipelinePath struct {
	ID uint32 `path:"id" minimum:"1"`
}
type listPipelineResourcesRequest struct {
	DomainID *int `query:"domain_id"`
	Limit    *int `query:"limit"`
	Offset   *int `query:"offset"`
}
type listPipelineLogsRequest struct {
	PipelineID   *int    `query:"pipeline_id"`
	Limit        *int    `query:"limit" maximum:"200"`
	Offset       *int    `query:"offset"`
	Direction    *string `query:"direction" enums:"inbound,outbound" message:"must be one of inbound, outbound"`
	Action       *string `query:"action" enums:"continue,reject,quarantine,discard,defer" message:"must be one of continue, reject, quarantine, discard, defer"`
	Outcome      *string `query:"outcome" enums:"delivered,queued,rejected,quarantined,discarded,deferred" message:"must be one of delivered, queued, rejected, quarantined, discarded, deferred"`
	ReasonCode   string  `query:"reason_code"`
	RFCMessageID string  `query:"rfc_message_id"`
}

// ScriptResult carries the sidecar dry run; JavaScript determines the result shape.
type ScriptResult struct {
	Result json.RawMessage `json:"result,omitempty"`
	Logs   []string        `json:"logs"`
	Error  string          `json:"error,omitempty"`
}
type ScriptExecution struct {
	Result json.RawMessage `json:"result"`
	Logs   []string        `json:"logs"`
}

// A sidecar worker failure is also forwarded by validate as 200, as before; valid
// can therefore be absent when only error is present.
type ScriptValidation struct {
	Valid  bool          `json:"valid,omitempty"`
	Error  string        `json:"error,omitempty"`
	Phase  string        `json:"phase,omitempty"`
	DryRun *ScriptResult `json:"dry_run,omitempty"`
}

type ScriptFailure struct {
	Error string `json:"error"`
}

// Execute can answer the sidecar's bare error as well as the API error envelope. The
// mixed statuses intentionally permit arbitrary JSON; 408 has only the sidecar error.
var scriptExecutionResponses = []oapifly.EndpointResponse{
	{Status: http.StatusBadRequest, Type: oapifly.TypeOf[json.RawMessage](), Description: "API bad_request or the sidecar's bare error"},
	{Status: http.StatusRequestTimeout, Type: oapifly.TypeOf[ScriptFailure](), Description: "The sidecar's execution timeout"},
	{Status: http.StatusRequestEntityTooLarge, Type: oapifly.TypeOf[json.RawMessage](), Description: "API request_too_large or the sidecar's bare error"},
	{Status: http.StatusInternalServerError, Type: oapifly.TypeOf[json.RawMessage](), Description: "API internal_error or the sidecar's bare worker failure"},
}

// PipelineRouteChecks describes the global capability and per-IP preview checks.
type PipelineRouteChecks struct {
	At           func(string) endpoint.Middleware
	TestThrottle endpoint.Middleware
}

func (h *PipelineHandler) Register(rs endpoint.Routes, checks PipelineRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/pipelines",
		OperationID: "listPipelines", Summary: "List pipelines", Tags: []string{"Admin: Pipelines"},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesRead)}}, h.listPipelines)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/pipelines/logs",
		OperationID: "listPipelineLogs", Summary: "List pipeline logs", Tags: []string{"Admin: Pipelines"},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesRead)}}, h.listPipelineLogs)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/messages/{id}/trace",
		OperationID: "getMessageTrace", Summary: "Get message trace", Tags: []string{"Admin: Pipelines"},
		Errors:     []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapObservabilityRead)}}, h.getMessageTrace)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/pipelines",
		OperationID: "createPipeline", Summary: "Create pipeline", Tags: []string{"Admin: Pipelines"},
		Status:     http.StatusCreated,
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesWrite)}}, h.createPipeline)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPatch, Path: "/api/v1/admin/pipelines/{id}",
		OperationID: "updatePipeline", Summary: "Update pipeline", Tags: []string{"Admin: Pipelines"},
		Errors:     []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesWrite)}}, h.updatePipeline)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/pipelines/{id}",
		OperationID: "deletePipeline", Summary: "Delete pipeline", Tags: []string{"Admin: Pipelines"},
		Errors:     []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesDel)}}, h.deletePipeline)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/pipelines/test",
		OperationID: "testPipeline", Summary: "Test pipeline", Tags: []string{"Admin: Pipelines"},
		Errors:     []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesWrite), checks.TestThrottle}}, h.testPipeline)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/pipelines/test-filter",
		OperationID: "testFilter", Summary: "Test filter", Tags: []string{"Admin: Pipelines"},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesWrite), checks.TestThrottle}}, h.testFilter)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/custom-filters",
		OperationID: "listCustomFilters", Summary: "List custom filters", Tags: []string{"Admin: Pipelines"},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesRead)}}, h.listCustomFilters)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/custom-filters",
		OperationID: "createCustomFilter", Summary: "Create custom filter", Tags: []string{"Admin: Pipelines"},
		Status:     http.StatusCreated,
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesWrite)}}, h.createCustomFilter)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/custom-filters/{id}",
		OperationID: "getCustomFilter", Summary: "Get custom filter", Tags: []string{"Admin: Pipelines"},
		Errors:     []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesRead)}}, h.getCustomFilter)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPatch, Path: "/api/v1/admin/custom-filters/{id}",
		OperationID: "updateCustomFilter", Summary: "Update custom filter", Tags: []string{"Admin: Pipelines"},
		Errors:     []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesWrite)}}, h.updateCustomFilter)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/custom-filters/{id}",
		OperationID: "deleteCustomFilter", Summary: "Delete custom filter", Tags: []string{"Admin: Pipelines"},
		Errors:     []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesDel)}}, h.deleteCustomFilter)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/custom-filters/{id}/test",
		Responses:   scriptExecutionResponses,
		OperationID: "testCustomFilter", Summary: "Test custom filter", Tags: []string{"Admin: Pipelines"},
		Errors:     []int{http.StatusNotFound, http.StatusServiceUnavailable, http.StatusBadRequest, http.StatusRequestTimeout},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesWrite), checks.TestThrottle}}, h.testCustomFilter)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/custom-filters/validate",
		OperationID: "validateCustomFilter", Summary: "Validate custom filter", Tags: []string{"Admin: Pipelines"},
		Errors:     []int{http.StatusServiceUnavailable},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapPipelinesWrite)}}, h.validateCustomFilter)
}
