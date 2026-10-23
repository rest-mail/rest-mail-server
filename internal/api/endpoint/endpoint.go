// Package endpoint registers each API route with the Go types it takes and answers (#623).
//
// Registration does the work as well as describing it. It binds the request into the
// request type, runs the handler, and writes what the handler returns in respond's envelope,
// so the types the OpenAPI document describes are the types the server executes. The same
// registration is the operation's entry in the generated document (go-oapifly), so a route
// cannot be served without being described.
//
// A request type names its parameters with `path:"name"`, `query:"name"` and `header:"name"`
// tags, and its body with a `body:""` field; a struct with no parameter fields is the body
// itself. Its constraints are struct tags (minLength, maximum, pattern, enums, format and so
// on), described in the document and enforced by go-oapifly's Bind before the handler runs:
// a request that breaks them is answered 422 with the fields at fault. The handler returns
// the response value, or an *Error to refuse the request.
package endpoint

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/respond"
)

// Operation is how a route is registered: where it is, what it is called, and what it can
// answer besides its success.
type Operation struct {
	Method      string
	Path        string
	OperationID string
	Summary     string
	Description string
	Tags        []string

	// Status is the success status. 204 writes no body.
	Status int
	// Errors are the error statuses the operation can answer. Each is described with
	// respond's error envelope. 422 for a request that breaks its type's rules is added when
	// the request type has anything to bind, 400 for a body that is not JSON and 413 for one
	// over the size cap when it has a body, and 500 always.
	Errors []int

	// Security is the operation's own requirement, when it differs from what its middleware
	// enforces: alternatives of schemes, each with the roles held there. nil leaves it to the
	// middleware and the document's default; oapifly.Public needs no credential.
	Security []oapifly.SecurityRequirement

	// Middleware runs before binding, in order, after the middleware its Routes already
	// apply: a capability check, a rate limit. Each describes what it adds.
	Middleware []Middleware
}

// Middleware is a middleware and a description of what it adds to every operation it wraps:
// the responses it can answer, the security it enforces and the roles it requires. The two
// are kept together so a route cannot be guarded by a check the document does not state.
type Middleware struct {
	Wrap      func(http.Handler) http.Handler
	Describes oapifly.Middleware
}

// Routes is where operations are registered: a router, the registry recording them, and what
// the middleware the router already applies to every route (its Use) adds, which chi runs but
// cannot describe.
type Routes struct {
	Router   chi.Router
	Registry *Registry
	Applied  []oapifly.Middleware
}

// Describing returns rs with the descriptions of middleware applied to rs.Router since, for
// a group that adds its own Use.
func (rs Routes) Describing(applied ...oapifly.Middleware) Routes {
	rs.Applied = append(append([]oapifly.Middleware(nil), rs.Applied...), applied...)
	return rs
}

// Error is a handler's refusal: the status, the API's error code and message, and, for a
// validation failure, the fields at fault.
type Error struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// Refuse is an *Error with a status, code and message.
func Refuse(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Invalid is the 422 validation failure, field by field.
func Invalid(fields map[string]string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Fields: fields}
}

// Data is the {"data": ...} envelope respond.Data writes, as a type the document can name.
type Data[T any] struct {
	Data T `json:"data"`
}

// List is a collection answer: written as respond.List writes it, {"data": [...],
// "pagination": {...}}, and described as such.
type List[T any] struct {
	Data       []T                 `json:"data"`
	Pagination *respond.Pagination `json:"pagination,omitempty"`
}

// listResponse is how write recognises a List, whatever its element type.
type listResponse interface{ listEnvelope() }

func (List[T]) listEnvelope() {}

// Upsert answers with Data in the usual envelope: 201 when Created, otherwise 200.
// Register describes both statuses using T, without exposing this status selector in JSON.
type Upsert[T any] struct {
	Data    T
	Created bool
}

type upsertResponse interface {
	responseTypes() (reflect.Type, reflect.Type)
	response() (int, interface{})
}

func (Upsert[T]) responseTypes() (reflect.Type, reflect.Type) {
	return reflect.TypeOf((*T)(nil)).Elem(), reflect.TypeOf(Data[T]{})
}

func (u Upsert[T]) response() (int, interface{}) {
	if u.Created {
		return http.StatusCreated, u.Data
	}
	return http.StatusOK, u.Data
}

// Empty is the response type of an operation that answers with no body.
type Empty struct{}

// Registry collects the registered operations for the generated document.
type Registry struct {
	endpoints []oapifly.Endpoint
	routes    map[string]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{routes: map[string]bool{}}
}

// Endpoints returns the registered operations, in the order they were registered.
func (reg *Registry) Endpoints() []oapifly.Endpoint {
	return append([]oapifly.Endpoint(nil), reg.endpoints...)
}

// Routes returns every registered route as "METHOD pattern", with the pattern as it was
// mounted on the router, sorted.
func (reg *Registry) Routes() []string {
	out := make([]string, 0, len(reg.routes))
	for route := range reg.routes {
		out = append(out, route)
	}
	sort.Strings(out)
	return out
}

// Has reports whether method and path were registered, so a check can find a route that
// was mounted without a registration.
func (reg *Registry) Has(method, path string) bool {
	return reg.routes[strings.ToUpper(method)+" "+path]
}

// Register mounts handler on rs.Router at op.Method and op.Path, behind op.Middleware, and
// records the operation in rs.Registry with everything its middleware adds. Req is bound from
// the request and Resp is what the handler answers.
func Register[Req, Resp any](rs Routes, op Operation, handler func(*http.Request, Req) (Resp, error)) {
	if op.Status == 0 {
		op.Status = http.StatusOK
	}
	reqType := reflect.TypeOf((*Req)(nil)).Elem()
	shape := requestShape(reqType)

	rs.Registry.record(rs.Applied, op, reqType, reflect.TypeOf((*Resp)(nil)).Elem(), reflect.TypeOf((*Data[Resp])(nil)).Elem(), shape)

	wraps := make([]func(http.Handler) http.Handler, len(op.Middleware))
	for i, m := range op.Middleware {
		wraps[i] = m.Wrap
	}
	rs.Router.With(wraps...).Method(op.Method, op.Path, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var in Req
		if err := bind(req, &in, shape); err != nil {
			writeError(w, err)
			return
		}
		out, err := handler(req, in)
		if err != nil {
			writeError(w, err)
			return
		}
		write(w, op.Status, out)
	}))
}

func (reg *Registry) record(applied []oapifly.Middleware, op Operation, reqType, respType, envelope reflect.Type, shape shape) {
	key := strings.ToUpper(op.Method) + " " + op.Path
	if reg.routes[key] {
		panic("endpoint: " + key + " registered twice")
	}
	reg.routes[key] = true

	var responses []oapifly.EndpointResponse
	if u, ok := reflect.Zero(respType).Interface().(upsertResponse); ok {
		respType, envelope = u.responseTypes()
		responses = []oapifly.EndpointResponse{
			successResponse(http.StatusOK, respType, envelope),
			successResponse(http.StatusCreated, respType, envelope),
		}
	} else {
		responses = []oapifly.EndpointResponse{successResponse(op.Status, respType, envelope)}
	}
	errorStatuses := map[int]bool{http.StatusInternalServerError: true}
	if shape.binds {
		errorStatuses[http.StatusUnprocessableEntity] = true
	}
	if shape.body {
		errorStatuses[http.StatusBadRequest] = true
		errorStatuses[http.StatusRequestEntityTooLarge] = true
	}
	for _, status := range op.Errors {
		errorStatuses[status] = true
	}
	statuses := make([]int, 0, len(errorStatuses))
	for status := range errorStatuses {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses)
	for _, status := range statuses {
		body := reflect.TypeOf(respond.ErrorResponse{})
		if status == http.StatusUnprocessableEntity {
			body = reflect.TypeOf(respond.ValidationErrorResponse{})
		}
		responses = append(responses, oapifly.EndpointResponse{Status: status, Type: body})
	}

	var request reflect.Type
	if shape.binds {
		request = reqType
	}
	reg.endpoints = append(reg.endpoints, oapifly.Endpoint{
		Method:      op.Method,
		Path:        documentedPath(op.Path),
		OperationID: op.OperationID,
		Summary:     op.Summary,
		Description: op.Description,
		Tags:        op.Tags,
		Request:     request,
		Responses:   responses,
		Security:    op.Security,
		Middleware:  described(applied, op.Middleware),
	})
}

// described lists what every middleware wrapping an operation adds, outermost first.
func described(applied []oapifly.Middleware, own []Middleware) []oapifly.Middleware {
	out := append([]oapifly.Middleware(nil), applied...)
	for _, m := range own {
		out = append(out, m.Describes)
	}
	return out
}

// successResponse describes the success answer in the envelope write puts it in: a List as
// itself, anything else as Data[Resp], the {"data": ...} body respond.Data writes.
func successResponse(status int, respType, envelope reflect.Type) oapifly.EndpointResponse {
	if status == http.StatusNoContent || respType == reflect.TypeOf(Empty{}) {
		return oapifly.EndpointResponse{Status: status}
	}
	if respType.Implements(reflect.TypeOf((*listResponse)(nil)).Elem()) {
		return oapifly.EndpointResponse{Status: status, Type: respType}
	}
	return oapifly.EndpointResponse{Status: status, Type: envelope}
}

var routePattern = regexp.MustCompile(`\{([^}:]+):[^}]*\}`)

// documentedPath turns a chi pattern into an OpenAPI path template: {id:[0-9]+} is {id}.
func documentedPath(path string) string {
	return routePattern.ReplaceAllString(path, "{$1}")
}

func write(w http.ResponseWriter, status int, out interface{}) {
	if u, ok := out.(upsertResponse); ok {
		status, out = u.response()
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	if _, empty := out.(Empty); empty {
		w.WriteHeader(status)
		return
	}
	// The document says a list is never null, so a list the handler left nil is written as
	// [] and a map as {}.
	out = oapifly.EmptyLists(out)
	if _, isList := out.(listResponse); isList {
		respond.JSON(w, status, out)
		return
	}
	respond.Data(w, status, out)
}

func writeError(w http.ResponseWriter, err error) {
	var refusal *Error
	if !errors.As(err, &refusal) {
		slog.Error("api: handler failed", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	if refusal.Fields != nil {
		respond.ValidationError(w, refusal.Fields)
		return
	}
	respond.Error(w, refusal.Status, refusal.Code, refusal.Message)
}

// shape is what a request type takes from a request, read once at registration by the rules
// go-oapifly's Endpoint.Request describes it with.
type shape struct {
	binds bool // anything at all: a parameter or a body
	body  bool // a body
}

func requestShape(t reflect.Type) shape {
	if t.Kind() != reflect.Struct {
		return shape{binds: true, body: true}
	}
	params, bodyField := false, false
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		for _, in := range []string{"path", "query", "header"} {
			if f.Tag.Get(in) != "" {
				params = true
			}
		}
		if _, ok := f.Tag.Lookup("body"); ok {
			bodyField = true
		}
	}
	whole := !params && !bodyField && t.NumField() > 0
	return shape{binds: params || bodyField || whole, body: bodyField || whole}
}

// bind reads the request into dst with go-oapifly, which enforces the rules the document
// describes: a violation is the 422 validation failure, field by field.
func bind(r *http.Request, dst interface{}, s shape) error {
	if !s.binds {
		return nil
	}
	var body []byte
	if s.body && r.Body != nil {
		read, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return Refuse(http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
			}
			return Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
		}
		body = read
	}

	err := oapifly.Bind(dst, func(in, name string) (string, bool) {
		switch in {
		case "path":
			v := chi.URLParam(r, name)
			return v, v != ""
		case "query":
			values, ok := r.URL.Query()[name]
			if !ok || len(values) == 0 {
				return "", false
			}
			return values[0], true
		}
		v := r.Header.Get(name)
		return v, v != ""
	}, body)

	var violations oapifly.Violations
	switch {
	case err == nil:
		return nil
	case errors.As(err, &violations):
		return Invalid(violations)
	case errors.Is(err, oapifly.ErrMalformedBody):
		return Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
	}
	return err
}
