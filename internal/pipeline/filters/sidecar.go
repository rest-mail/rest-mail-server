package filters

import (
	"encoding/json"
	"io/fs"
	"net/http"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// The JavaScript filter sidecar's contract (#677): what the API sends it and what it
// answers, as the Go types the API's client code reads and writes. SidecarEndpoints
// describes them; projects/js-filter-sidecar/js-filter-sidecar.json is the document they
// make (TestSidecarDescriptionIsCommitted), and CI checks the running sidecar against it
// with Vertrag.

// SidecarExecute is the request of POST /execute: run a filter script against an email.
type SidecarExecute struct {
	// Script is the filter's JavaScript source, which defines filter(email).
	Script string `json:"script" minLength:"1"`
	// Email is what filter(email) is called with: the pipeline sends the message as it
	// holds it (pipeline.EmailJSON), an admin's test the sample it was given.
	Email json.RawMessage `json:"email" type:"object"`
	// TimeoutMS is the script's CPU budget in milliseconds, 500 when it is left out. The
	// sidecar ends a run that takes three times as long, at least two seconds, whatever
	// it is doing.
	TimeoutMS int `json:"timeout_ms,omitempty" minimum:"1"`
}

// SidecarValidate is the request of POST /validate: check a script's syntax and, given
// an email, run it once with no fetch and 500 ms of CPU.
type SidecarValidate struct {
	Script string          `json:"script" minLength:"1"`
	Email  json.RawMessage `json:"email,omitempty" type:"object"`
}

// SidecarResult is a script's run: what filter(email) returned, left out when it returned
// nothing, and the lines it logged.
type SidecarResult struct {
	Result json.RawMessage `json:"result,omitempty"`
	Logs   []string        `json:"logs"`
}

// SidecarValidation is the outcome of POST /validate. A syntax error is valid false with
// the error and phase "syntax". A dry run that failed is valid true with the run's error
// and phase "execution".
type SidecarValidation struct {
	Valid  bool           `json:"valid"`
	Error  string         `json:"error,omitempty"`
	Phase  string         `json:"phase,omitempty" enums:"syntax,execution"`
	DryRun *SidecarDryRun `json:"dry_run,omitempty"`
}

// SidecarDryRun is the run POST /validate made of a script given an email: its result or
// its error, and the lines it logged.
type SidecarDryRun struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Logs   []string        `json:"logs"`
}

// SidecarRefusal is POST /validate's answer to a request it cannot read.
type SidecarRefusal struct {
	Valid bool   `json:"valid"`
	Error string `json:"error"`
}

// SidecarError is the sidecar's error: a request it refused, a script that threw or ran
// out of time, a worker that failed.
type SidecarError struct {
	Error string `json:"error"`
}

// SidecarHealth is GET /health's answer.
type SidecarHealth struct {
	Status string `json:"status" enums:"ok"`
}

// SidecarScheme is the security scheme the sidecar's operations need: the token both it
// and the API are given (JS_FILTER_TOKEN), sent as a bearer token.
const SidecarScheme = "sidecarToken"

// SidecarEndpoints describes the sidecar's operations.
func SidecarEndpoints() []oapifly.Endpoint {
	failure := func(status int, description string) oapifly.EndpointResponse {
		return oapifly.EndpointResponse{Status: status, Description: description, Type: oapifly.TypeOf[SidecarError]()}
	}
	unauthorized := oapifly.EndpointResponse{
		Status: http.StatusUnauthorized, Description: "The bearer token is missing or is not the sidecar's",
		Type:    oapifly.TypeOf[SidecarError](),
		Headers: map[string]oapifly.ResponseHeader{"WWW-Authenticate": {Description: "Bearer", Required: true}},
	}
	tooLarge := failure(http.StatusRequestEntityTooLarge, "The body is larger than JS_FILTER_MAX_BODY_BYTES")
	return []oapifly.Endpoint{
		{
			Method: http.MethodGet, Path: "/health", OperationID: "sidecarHealth",
			Summary: "Report that the sidecar is up", Security: oapifly.Public,
			Responses: []oapifly.EndpointResponse{{Status: http.StatusOK, Type: oapifly.TypeOf[SidecarHealth]()}},
		},
		{
			Method: http.MethodPost, Path: "/execute", OperationID: "sidecarExecute",
			Summary:     "Run a filter script against an email",
			Description: "Each run gets a QuickJS engine of its own in a worker thread, with the memory JS_FILTER_MEMORY_LIMIT_BYTES allows; a script may fetch only from JS_FILTER_ALLOWED_HOSTS.",
			Request:     oapifly.TypeOf[SidecarExecute](),
			Responses: []oapifly.EndpointResponse{
				{Status: http.StatusOK, Description: "The script ran", Type: oapifly.TypeOf[SidecarResult]()},
				failure(http.StatusBadRequest, "The body is not JSON, or has no script or email"),
				unauthorized,
				failure(http.StatusRequestTimeout, "The script ran out of time"),
				tooLarge,
				failure(http.StatusInternalServerError, "The script threw, or its worker failed"),
			},
		},
		{
			Method: http.MethodPost, Path: "/validate", OperationID: "sidecarValidate",
			Summary:     "Check a filter script, and run it once against an email",
			Description: "A script that does not compile is valid false. Given an email, a script that compiles is run once, with 500 ms of CPU and no fetch, and the run is reported, a failed one included.",
			Request:     oapifly.TypeOf[SidecarValidate](),
			Responses: []oapifly.EndpointResponse{
				{Status: http.StatusOK, Description: "The script was checked", Type: oapifly.TypeOf[SidecarValidation]()},
				{Status: http.StatusBadRequest, Description: "The body is not JSON, or has no script", Type: oapifly.TypeOf[SidecarRefusal]()},
				unauthorized,
				tooLarge,
				failure(http.StatusInternalServerError, "The worker failed"),
			},
		},
	}
}

// SidecarExamplesFile is the sidecar's example pairs, beside its document.
const SidecarExamplesFile = "js-filter-sidecar-example.json"

// SidecarDescription is the generator of the sidecar's document at version, its example
// pairs read from SidecarExamplesFile in examples.
func SidecarDescription(version string, examples fs.FS) *oapifly.Generator {
	g := oapifly.New(oapifly.Config{
		Title:       "REST Mail JavaScript filter sidecar",
		Description: "The service that runs REST Mail's JavaScript filters (projects/js-filter-sidecar). The API is its only client.",
		Version:     version,
		SecuritySchemes: map[string]oapifly.SecurityScheme{
			SidecarScheme: {Type: "http", Scheme: "bearer", Description: "JS_FILTER_TOKEN, given to both the sidecar and the API"},
		},
		Security:     []oapifly.SecurityRequirement{{SidecarScheme: {}}},
		Examples:     examples,
		ExamplesFile: SidecarExamplesFile,
	})
	g.Register(SidecarEndpoints()...)
	return g
}
