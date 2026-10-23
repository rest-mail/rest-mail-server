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
// itself. The handler returns the response value, or an *Error to refuse the request.
package endpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
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
	// respond's error envelope; 400 for a request that cannot be bound is added when the
	// request type has anything to bind, and 500 always.
	Errors []int

	// Middleware runs before binding, in order: authentication, capability checks.
	Middleware []func(http.Handler) http.Handler
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

// Register mounts handler on r at op.Method and op.Path, behind op.Middleware, and records
// the operation in reg. Req is bound from the request and Resp is what the handler answers.
func Register[Req, Resp any](r chi.Router, reg *Registry, op Operation, handler func(*http.Request, Req) (Resp, error)) {
	if op.Status == 0 {
		op.Status = http.StatusOK
	}
	reqType := reflect.TypeOf((*Req)(nil)).Elem()
	binder := newBinder(reqType)

	reg.record(op, reqType, reflect.TypeOf((*Resp)(nil)).Elem(), reflect.TypeOf((*Data[Resp])(nil)).Elem(), binder.binds())

	r.With(op.Middleware...).Method(op.Method, op.Path, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var in Req
		if err := binder.bind(req, &in); err != nil {
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

func (reg *Registry) record(op Operation, reqType, respType, envelope reflect.Type, binds bool) {
	key := strings.ToUpper(op.Method) + " " + op.Path
	if reg.routes[key] {
		panic("endpoint: " + key + " registered twice")
	}
	reg.routes[key] = true

	responses := []oapifly.EndpointResponse{successResponse(op.Status, respType, envelope)}
	errorStatuses := map[int]bool{http.StatusInternalServerError: true}
	if binds {
		errorStatuses[http.StatusBadRequest] = true
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
		responses = append(responses, oapifly.EndpointResponse{
			Status: status,
			Type:   reflect.TypeOf(respond.ErrorResponse{}),
		})
	}

	var request reflect.Type
	if binds {
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
	})
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

// binder binds one request type: which fields come from the path, the query, a header and
// the body, read once at registration.
type binder struct {
	t       reflect.Type
	params  []boundField
	bodyIdx []int // the body field, or nil
	whole   bool  // the request type is the body
}

type boundField struct {
	index []int
	in    string
	name  string
}

func newBinder(t reflect.Type) *binder {
	b := &binder{t: t}
	if t.Kind() != reflect.Struct {
		b.whole = true
		return b
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		for _, in := range []string{"path", "query", "header"} {
			if v := f.Tag.Get(in); v != "" {
				name, _, _ := strings.Cut(v, ",")
				b.params = append(b.params, boundField{index: f.Index, in: in, name: name})
			}
		}
		if _, ok := f.Tag.Lookup("body"); ok {
			b.bodyIdx = f.Index
		}
	}
	b.whole = len(b.params) == 0 && b.bodyIdx == nil && t.NumField() > 0
	return b
}

// binds reports whether the request type takes anything from the request.
func (b *binder) binds() bool {
	return b.whole || b.bodyIdx != nil || len(b.params) > 0
}

func (b *binder) bind(r *http.Request, dst interface{}) error {
	v := reflect.ValueOf(dst).Elem()
	if b.whole {
		if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
			return Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
		}
		return nil
	}
	for _, p := range b.params {
		var raw string
		var present bool
		switch p.in {
		case "path":
			raw = chi.URLParam(r, p.name)
			present = raw != ""
		case "query":
			raw = r.URL.Query().Get(p.name)
			_, present = r.URL.Query()[p.name]
		case "header":
			raw = r.Header.Get(p.name)
			present = raw != ""
		}
		if !present {
			continue
		}
		if err := setValue(v.FieldByIndex(p.index), raw); err != nil {
			return Refuse(http.StatusBadRequest, "bad_request", "Invalid "+p.name)
		}
	}
	if b.bodyIdx != nil {
		field := v.FieldByIndex(b.bodyIdx)
		if err := json.NewDecoder(r.Body).Decode(field.Addr().Interface()); err != nil {
			return Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
		}
	}
	return nil
}

// setValue parses raw into a parameter field of a scalar kind, or a pointer to one.
func setValue(field reflect.Value, raw string) error {
	if field.Kind() == reflect.Pointer {
		ptr := reflect.New(field.Type().Elem())
		if err := setValue(ptr.Elem(), raw); err != nil {
			return err
		}
		field.Set(ptr)
		return nil
	}
	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		field.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, field.Type().Bits())
		if err != nil {
			return err
		}
		field.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, field.Type().Bits())
		if err != nil {
			return err
		}
		field.SetUint(n)
	default:
		return fmt.Errorf("parameter of kind %s cannot be bound", field.Kind())
	}
	return nil
}
